package durable

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

const childTestStep = "delegate"

func startChildStep(t *testing.T, store *Store, parent *Run) *Run {
	t.Helper()
	claimed := claim(t, store, "parent-worker")
	if claimed.ID != parent.ID {
		t.Fatalf("claimed %s, want parent %s", claimed.ID, parent.ID)
	}
	if _, err := store.BeginStep(context.Background(), claimed.Lease(), StepIntent{
		ID: childTestStep, Input: "delegate task", Policy: Idempotent,
	}); err != nil {
		t.Fatal(err)
	}
	return claimed
}

func updateChildTestSpec(t *testing.T, store *Store, parent *Run, spec Spec) {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.db.Model(&table.Run{}).
		Where("id = ?", parent.ID).
		Update("spec", string(data)).
		Error; err != nil {
		t.Fatal(err)
	}
}

func spawnAtomicChild(t *testing.T, store *Store, parent *Run, req ChildSpawnRequest) *Run {
	t.Helper()
	req.StepID = childTestStep
	child, err := store.SpawnChildAndAwait(
		context.Background(),
		parent.Lease(),
		req,
		json.RawMessage(`{"turn":1}`),
		"awaiting child",
	)
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func TestSpawnChildAndAwaitFrozenModes(t *testing.T) {
	for _, mode := range []string{"configured", "self", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			parent := createRun(t, store)
			specialistSession, _ := testConversation(t, store, Spec{MaxTurns: 9})
			spec := parent.Spec
			spec.SystemPrompt = "parent system"
			spec.History = []Message{{Role: "user", Content: "private parent history"}}
			spec.PricingFrozen = true
			spec.PricingSnapshot = &execution.PricingSnapshot{InputPerMillion: 1, OutputPerMillion: 2}
			spec.SelfDelegate = mode != "legacy"
			spec.Filesystem = &FilesystemBinding{
				Provider:             "Sandbox",
				URL:                  "https://sandbox.test",
				TokenEnv:             "SANDBOX_TOKEN",
				Workspace:            "/workspace",
				ProfileID:            "coding",
				RestoreFromSandboxID: "sbx-parent-prev",
			}
			specialist := SubAgentSnapshot{
				Name:            "review",
				Description:     "Review work",
				AgentID:         specialistSession.AgentID,
				ConfigVersion:   "7",
				Model:           ModelSpec{Name: "review-model", BaseURL: "https://review.test"},
				MaxTurns:        9,
				SystemPrompt:    "review system",
				PricingFrozen:   true,
				PricingSnapshot: &execution.PricingSnapshot{InputPerMillion: 3, OutputPerMillion: 5},
				Toolsets:        []string{"filesystem"},
			}
			spec.SubAgents = []SubAgentSnapshot{specialist}
			updateChildTestSpec(t, store, parent, spec)
			claimed := startChildStep(t, store, parent)

			req := ChildSpawnRequest{Query: "review the change", TTL: time.Minute}
			wantAgent, wantVersion, wantPrompt := spec.AgentID, spec.ConfigVersion, spec.SystemPrompt
			wantModel, wantPrice, wantQuery := spec.Model, spec.PricingSnapshot, req.Query
			var child *Run
			switch mode {
			case "configured":
				req.SubAgent = specialist.Name
				wantAgent, wantVersion, wantPrompt = specialist.AgentID, specialist.ConfigVersion, specialist.SystemPrompt
				wantModel, wantPrice = specialist.Model, specialist.PricingSnapshot
				child = spawnAtomicChild(t, store, claimed, req)
			case "self":
				req.Instruction = "  Check for regressions. \n"
				wantQuery = "[Subtask Instructions]\nCheck for regressions.\n\n[Task]\n" + req.Query
				child = spawnAtomicChild(t, store, claimed, req)
			case "legacy":
				var err error
				if _, err = store.SpawnChildAndAwait(ctx, claimed.Lease(), ChildSpawnRequest{
					StepID: childTestStep, Query: req.Query, TTL: req.TTL,
				}, json.RawMessage(`{}`), "awaiting child"); !errors.Is(err, ErrInvalid) {
					t.Fatalf("disabled self-delegation accepted: %v", err)
				}
				child, err = store.SpawnChildRun(
					ctx,
					claimed.Lease(),
					childTestStep,
					req.Query,
					req.TTL,
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			if child.EventSeq != 1 || child.Status != Queued {
				t.Fatalf("new child status=%s eventSeq=%d", child.Status, child.EventSeq)
			}
			stored, err := store.Get(ctx, "owner", child.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := stored.Spec
			if got.AgentID != wantAgent || got.ConfigVersion != wantVersion {
				t.Fatalf("child spec = %+v", got)
			}
			if got.SystemPrompt != wantPrompt {
				t.Fatalf("child system prompt = %q, want %q", got.SystemPrompt, wantPrompt)
			}
			if !reflect.DeepEqual(got.Model, wantModel) {
				t.Fatalf("child model = %+v, want %+v", got.Model, wantModel)
			}
			if !got.PricingFrozen || !reflect.DeepEqual(got.PricingSnapshot, wantPrice) {
				t.Fatalf("child pricing = %+v, want frozen %+v", got.PricingSnapshot, wantPrice)
			}
			wantFilesystem := *spec.Filesystem
			wantFilesystem.RestoreFromSandboxID = ""
			if got.Query != wantQuery || !reflect.DeepEqual(got.Filesystem, &wantFilesystem) {
				t.Fatalf("child query or filesystem differs: %+v, want fs %+v", got, &wantFilesystem)
			}
			if len(got.History) != 0 || len(got.SubAgents) != 0 || got.SelfDelegate {
				t.Fatalf("child inherited parent history or delegation: %+v", got)
			}
			if err = validateContextSnapshot(stored.ContextSnapshot); err != nil {
				t.Fatal(err)
			}
			payload := string(stored.ContextSnapshot.Payload)
			if stored.ContextSnapshot.Digest == parent.ContextSnapshot.Digest ||
				!strings.Contains(payload, `"memoryMode":"Off"`) ||
				!strings.Contains(payload, wantPrompt) ||
				strings.Contains(payload, "private parent history") {
				t.Fatalf("child context = %s", payload)
			}
			session, err := store.Session(ctx, "owner", stored.SessionID)
			if err != nil || session.AgentID != wantAgent || session.ID == parent.SessionID {
				t.Fatalf("child session = %+v, error = %v", session, err)
			}
			messages, err := store.Messages(ctx, "owner", session.ID, 0, 10)
			if err != nil || len(messages) != 1 || messages[0].RunID != child.ID ||
				messages[0].Kind != "User" ||
				messages[0].Role != "user" ||
				messages[0].Content != wantQuery {
				t.Fatalf("child session messages = %+v, error = %v", messages, err)
			}
			if mode != "legacy" {
				waiting, err := store.Get(ctx, "owner", parent.ID)
				if err != nil || waiting.Status != WaitingExternal ||
					waiting.WaitingStep != childTestStep ||
					waiting.WaitingExternalRef != childExternalRefPrefix+child.ID ||
					waiting.WorkerID != "" ||
					waiting.Error != "awaiting child" ||
					waiting.Output != "" {
					t.Fatalf("waiting parent = %+v, error = %v", waiting, err)
				}
				assertJSONEqual(t, waiting.State, `{"turn":1}`)
				events, err := store.Events(ctx, "owner", parent.ID, waiting.EventSeq-2)
				if err != nil || len(events) != 2 || events[0].Kind != "ChildRunSpawned" ||
					events[1].Kind != "ExternalWaitRequested" {
					t.Fatalf("parent outbox = %+v, error = %v", events, err)
				}
				spawned, err := json.Marshal(map[string]string{
					"agentID":  wantAgent,
					"childID":  child.ID,
					"stepID":   childTestStep,
					"subAgent": req.SubAgent,
				})
				if err != nil {
					t.Fatal(err)
				}
				assertJSONEqual(t, events[0].Payload, string(spawned))
				assertJSONEqual(
					t,
					events[1].Payload,
					`{"status":"WaitingExternal","output":"","detail":"awaiting child","waitingStep":"delegate","waitingKind":""}`,
				)
			}
			childLease := startChildStep(t, store, child).Lease()
			req.StepID = childTestStep
			if _, err = store.SpawnChildAndAwait(
				ctx,
				childLease,
				req,
				json.RawMessage(`{}`),
				"awaiting grandchild",
			); !errors.Is(
				err,
				ErrInvalid,
			) {
				t.Fatalf("atomic grandchild error = %v", err)
			}
			if _, err = store.SpawnChildRun(
				ctx,
				childLease,
				req.StepID,
				req.Query,
				req.TTL,
			); !errors.Is(
				err,
				ErrInvalid,
			) {
				t.Fatalf("legacy grandchild error = %v", err)
			}
		})
	}
}

func TestChildWaitRejectsExternalCallbacks(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	parent := createRun(t, store)
	spec := parent.Spec
	spec.SelfDelegate = true
	updateChildTestSpec(t, store, parent, spec)
	claimed := startChildStep(t, store, parent)
	if _, err := store.BeginStep(ctx, claimed.Lease(), StepIntent{
		ID: "forged", Input: "payload", Policy: Idempotent, ExternalRef: "child:forged",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("BeginStep accepted child ref: %v", err)
	}
	child := spawnAtomicChild(t, store, claimed, ChildSpawnRequest{Query: "work", TTL: time.Hour})
	for _, ref := range []string{childExternalRefPrefix + child.ID, "unrelated-ref"} {
		if err := store.SupplyExternalResult(
			ctx,
			"owner",
			parent.ID,
			childTestStep,
			ref,
			"forged",
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("SupplyExternalResult(%q) = %v", ref, err)
		}
		if err := store.SupplyExternalResultByRunID(
			ctx,
			parent.ID,
			childTestStep,
			ref,
			"forged",
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("SupplyExternalResultByRunID(%q) = %v", ref, err)
		}
	}
	waiting, err := store.Get(ctx, "owner", parent.ID)
	if err != nil || waiting.Status != WaitingExternal {
		t.Fatalf("parent = %+v, error = %v", waiting, err)
	}
}

func TestChildRunAbnormalTerminalPaths(t *testing.T) {
	for _, scenario := range []string{"child expires", "child attempts exhausted", "final retry", "parent expires"} {
		t.Run(scenario, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			parent, child := spawnWaitingParent(t, store, ctx, "work")
			endRun := child.ID
			updates := map[string]any{"deadline": gorm.Expr("clock_timestamp() - interval '1 second'")}
			wantParent, wantChild, wantEvent := Queued, Failed, "RunFailed"
			switch scenario {
			case "child attempts exhausted":
				updates = map[string]any{"attempt": gorm.Expr("max_attempts")}
			case "final retry":
				updates = map[string]any{"max_attempts": 1}
				wantEvent = "RunRetry"
			case "parent expires":
				endRun = parent.ID
				wantParent, wantChild = Failed, Canceled
				wantEvent = "RunCanceled"
			}
			if err := store.db.Model(&table.Run{}).
				Where("id = ?", endRun).
				Updates(updates).
				Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "final retry" {
				claimed := claim(t, store, "child-worker")
				if claimed.ID != child.ID {
					t.Fatalf("claimed %s, want child %s", claimed.ID, child.ID)
				}
				if err := store.Retry(
					ctx,
					claimed.Lease(),
					json.RawMessage(`{}`),
					"provider unavailable",
					0,
				); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.Claim(ctx, "sweeper", time.Minute); !errors.Is(err, ErrNoWork) {
				t.Fatalf("sweep error = %v", err)
			}
			parentAfter, err := store.Get(ctx, "owner", parent.ID)
			if err != nil || parentAfter.Status != wantParent {
				t.Fatalf("parent = %+v, error = %v", parentAfter, err)
			}
			childAfter, err := store.Get(ctx, "owner", child.ID)
			if err != nil || childAfter.Status != wantChild || childAfter.WorkerID != "" ||
				childAfter.LeaseUntil != nil {
				t.Fatalf("child = %+v, error = %v", childAfter, err)
			}
			counts := childEventCounts(t, store, child.ID)
			if counts[wantEvent] != 1 {
				t.Fatalf("child event counts = %v, want one %s", counts, wantEvent)
			}
			var step table.Step
			if err = store.db.Where("run_id = ? AND step_id = ?", parent.ID, child.ParentStepID).
				Take(&step).
				Error; err != nil {
				t.Fatal(err)
			}
			if wantParent == Queued &&
				(step.Status != "Completed" || !strings.Contains(step.Result, string(Failed))) {
				t.Fatalf("failed child result = %+v", step)
			}
			if wantParent == Failed && (step.Status != "Started" || counts["ChildOrphaned"] != 1) {
				t.Fatalf("expired parent step = %+v, child event counts = %v", step, counts)
			}
		})
	}
}

func childEventCounts(t *testing.T, store *Store, runID string) map[string]int {
	t.Helper()
	events, err := store.Events(context.Background(), "owner", runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, event := range events {
		counts[event.Kind]++
	}
	return counts
}

func TestChildCompleteAndParentCancelUseSameLockOrder(t *testing.T) {
	store := testStore(t)
	for attempt := range 12 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		parent, child := spawnWaitingParent(t, store, ctx, "work")
		claimed := claim(t, store, "child-worker")
		if claimed.ID != child.ID {
			cancel()
			t.Fatalf("claimed %s, want child %s", claimed.ID, child.ID)
		}
		ready := make(chan struct{}, 2)
		start := make(chan struct{})
		parentResult := make(chan error, 1)
		childResult := make(chan error, 1)
		runOnConnection := func(operation func(*Store) error, result chan<- error) {
			result <- store.db.WithContext(ctx).Connection(func(connection *gorm.DB) error {
				ready <- struct{}{}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-start:
					return operation(&Store{db: connection})
				}
			})
		}
		go runOnConnection(func(connection *Store) error {
			return connection.Cancel(ctx, "owner", parent.ID)
		}, parentResult)
		go runOnConnection(func(connection *Store) error {
			return connection.CommitBoundary(ctx, claimed.Lease(), Boundary{
				Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "answer",
			})
		}, childResult)
		for range 2 {
			select {
			case <-ready:
			case <-ctx.Done():
				cancel()
				t.Fatalf("attempt %d: could not acquire two PostgreSQL connections", attempt)
			}
		}
		close(start)
		cancelErr, completeErr := <-parentResult, <-childResult
		cancel()
		if cancelErr != nil || (completeErr != nil && !errors.Is(completeErr, ErrLeaseLost)) {
			t.Fatalf(
				"attempt %d: cancel error = %v, child complete error = %v",
				attempt,
				cancelErr,
				completeErr,
			)
		}
		parentAfter, err := store.Get(context.Background(), "owner", parent.ID)
		if err != nil || parentAfter.Status != Canceled {
			t.Fatalf("parent revived: %+v, error = %v", parentAfter, err)
		}
		childAfter, err := store.Get(context.Background(), "owner", child.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Cancel(context.Background(), "owner", parent.ID); err != nil {
			t.Fatal(err)
		}
		parentEvents := childEventCounts(t, store, parent.ID)
		childEvents := childEventCounts(t, store, child.ID)
		if parentEvents["RunCanceled"] != 1 {
			t.Fatalf("parent events = %v", parentEvents)
		}
		if completeErr == nil {
			if childAfter.Status != Completed || childAfter.Output != "answer" ||
				childEvents["RunCompleted"] != 1 ||
				childEvents["ChildRunCompleted"] != 1 ||
				childEvents["RunCanceled"] != 0 ||
				parentEvents["RunQueued"] != 2 {
				t.Fatalf(
					"completed child=%+v, child events=%v, parent events=%v",
					childAfter,
					childEvents,
					parentEvents,
				)
			}
		} else if childAfter.Status != Canceled || childEvents["RunCanceled"] != 1 || childEvents["ChildOrphaned"] != 1 ||
			childEvents["RunCompleted"] != 0 || parentEvents["RunQueued"] != 1 {
			t.Fatalf(
				"canceled child=%+v, child events=%v, parent events=%v",
				childAfter,
				childEvents,
				parentEvents,
			)
		}
	}
}

func TestChildSpawnUsesGoalDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		store := testStore(t)
		ctx := context.Background()
		parent := createRun(t, store)
		spec := parent.Spec
		spec.SelfDelegate = true
		updateChildTestSpec(t, store, parent, spec)
		goal := newTestGoal(parent.SessionID, NewID(), "delegate within the budget")
		goal.Budget = BudgetSpec{MaxWallClockSeconds: 120}
		bound, _, err := store.CreateGoal(ctx, goal)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.BindGoalToRun(ctx, "owner", bound.ID, parent.ID); err != nil {
			t.Fatal(err)
		}
		claimed := startChildStep(t, store, parent)
		req := ChildSpawnRequest{StepID: childTestStep, Query: "work", TTL: time.Hour}
		if expired {
			if err = store.db.Model(&table.Goal{}).Where("id = ?", bound.ID).
				Update("created_at", gorm.Expr("clock_timestamp() - interval '3 minutes'")).
				Error; err != nil {
				t.Fatal(err)
			}
			if child, err := store.SpawnChildAndAwait(
				ctx,
				claimed.Lease(),
				req,
				json.RawMessage(`{}`),
				"awaiting child",
			); !errors.Is(err, ErrConflict) ||
				child != nil {
				t.Fatalf("expired Goal allowed spawn: child=%+v error=%v", child, err)
			}
			if child, err := store.SpawnChildRun(
				ctx,
				claimed.Lease(),
				req.StepID,
				req.Query,
				req.TTL,
			); !errors.Is(err, ErrConflict) ||
				child != nil {
				t.Fatalf("expired Goal allowed legacy spawn: child=%+v error=%v", child, err)
			}
			continue
		}
		child := spawnAtomicChild(t, store, claimed, req)
		if child.GoalID != bound.ID || !child.Deadline.Equal(bound.CreatedAt.Add(2*time.Minute)) {
			t.Fatalf(
				"child goal=%s deadline=%s, want %s / %s",
				child.GoalID,
				child.Deadline,
				bound.ID,
				bound.CreatedAt.Add(2*time.Minute),
			)
		}
	}
}

func TestGoalIterationWithChildOnlyFilesystem(t *testing.T) {
	store := testStore(t)
	goal, previous := failedGoalIteration(t, store)
	spec := previous.Spec
	spec.Filesystem = &FilesystemBinding{
		Provider:  "Sandbox",
		URL:       "http://sandbox-center",
		TokenEnv:  "SANDBOX_TOKEN",
		Workspace: "/workspace/repo",
	}
	spec.SubAgents = []SubAgentSnapshot{
		{
			Name:          "workspace",
			AgentID:       "child",
			ConfigVersion: "1",
			Description:   "Use files",
			Toolsets:      []string{"filesystem"},
		},
	}
	updateChildTestSpec(t, store, previous, spec)
	next, err := store.QueueGoalIteration(context.Background())
	if err != nil || next == nil || next.Status != Queued || next.GoalID != goal.ID ||
		len(next.Spec.Toolsets) != 0 || !reflect.DeepEqual(next.Spec.SubAgents, spec.SubAgents) ||
		!reflect.DeepEqual(next.Spec.Filesystem, spec.Filesystem) {
		t.Fatalf("child-only workspace iteration = %+v, error = %v", next, err)
	}
	if err = validateContextSnapshot(next.ContextSnapshot); err != nil {
		t.Fatal(err)
	}
}

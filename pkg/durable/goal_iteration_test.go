package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func failedGoalIteration(t *testing.T, store *Store) (*Goal, *Run) {
	t.Helper()
	ctx := context.Background()
	session, cfg := testConversation(t, store, Spec{MaxTurns: 5, SystemPrompt: "frozen instruction"})
	goal := newTestGoal(session.ID, NewID(), "fix the acceptance failure")
	goal.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "check"}
	goal.Budget = BudgetSpec{MaxWallClockSeconds: 3600, MaxTokens: 100, MaxCostUSD: 10}
	current, _, err := store.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := submitFixture(t, store, "owner", session.ID, NewID(), "original request", time.Hour, 3, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindGoalToRun(ctx, "owner", current.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, store, "worker")
	if err = store.RecordUsageWithCost(
		ctx,
		claimed.Lease(),
		1,
		"previous-model",
		7,
		0.25,
		json.RawMessage(`{"version":3}`),
	); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind: BoundaryComplete, State: json.RawMessage(`{"version":3}`), Output: "previous answer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RecordEvaluation(
		ctx,
		"owner",
		current.ID,
		run.ID,
		EvaluationFailed,
		"missing required field",
	); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CheckGoalProgress(ctx, "owner", current.ID); err != nil {
		t.Fatal(err)
	}
	return current, run
}

func TestGoalIterationConcurrentAndFrozen(t *testing.T) {
	store := testStore(t)
	goal, previous := failedGoalIteration(t, store)
	ctx := context.Background()
	var workers sync.WaitGroup
	var mutex sync.Mutex
	var created []*Run
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			next, err := store.QueueGoalIteration(ctx)
			if errors.Is(err, ErrNoWork) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mutex.Lock()
			created = append(created, next)
			mutex.Unlock()
		}()
	}
	workers.Wait()
	if len(created) != 1 || created[0] == nil {
		t.Fatalf("created iterations=%+v, want one", created)
	}
	next := created[0]
	if next.Status != Queued || next.GoalID != goal.ID || next.SessionID != goal.SessionID ||
		next.Spec.ConfigVersion != previous.Spec.ConfigVersion || next.Spec.SystemPrompt != previous.Spec.SystemPrompt ||
		next.Deadline.After(previous.Deadline) || next.Deadline.After(goal.CreatedAt.Add(time.Hour)) ||
		next.TokensUsed != 0 || next.CostUSD != 0 || next.Epoch != 0 || next.EvaluationEpoch != 0 ||
		string(next.State) != "{}" || next.ParentRunID != "" {
		t.Fatalf("invalid next run: %+v", next)
	}
	if !strings.Contains(next.Spec.Query, goal.Objective) ||
		!strings.Contains(next.Spec.Query, "missing required field") {
		t.Fatalf("missing objective or feedback: %q", next.Spec.Query)
	}
	if len(next.Spec.History) != 2 || next.Spec.History[0].Content != "original request" ||
		next.Spec.History[1].Content != "previous answer" {
		t.Fatalf("missing prior conversation: %+v", next.Spec.History)
	}
	current, err := store.GetGoal(ctx, "owner", goal.ID)
	if err != nil || current.ActiveRunID != next.ID || current.TokensUsed != 7 || current.CostUSD != 0.25 ||
		current.StallCount != 1 {
		t.Fatalf("goal budget or binding changed: %+v %v", current, err)
	}
	events, err := store.Events(ctx, "owner", previous.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != "GoalIterationQueued" || !strings.Contains(string(last.Payload), next.ID) {
		t.Fatalf("missing iteration link: %+v", last)
	}
	if _, err = store.QueueGoalIteration(ctx); !errors.Is(err, ErrNoWork) {
		t.Fatalf("iteration duplicated: %v", err)
	}
}

func TestGoalIterationEligibility(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Store, *Goal, *Run) error
	}{
		{name: "paused", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.TransitionGoal(context.Background(), "owner", goal.ID, GoalPaused)
		}},
		{name: "error receipt", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Model(&table.Evaluation{}).
				Where("run_id = ?", run.ID).
				Update("status", EvaluationError).
				Error
		}},
		{name: "passed receipt", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Model(&table.Evaluation{}).
				Where("run_id = ?", run.ID).
				Update("status", EvaluationPassed).
				Error
		}},
		{name: "unaccounted run", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Model(&table.Run{}).Where("id = ?", run.ID).Update("stall_mark", "").Error
		}},
		{name: "active environment", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Create(&table.RunEnvironment{
				ID: NewID(), RunID: run.ID, RequestKey: NewID(), RunEpoch: 1, ProfileID: "coding",
				SandboxID: "sandbox", LeaseID: "lease", Generation: 1, Status: EnvironmentActive,
			}).Error
		}},
		{name: "busy session", mutate: func(store *Store, goal *Goal, _ *Run) error {
			_, _, err := submitFixture(
				t,
				store,
				"owner",
				goal.SessionID,
				NewID(),
				"new user work",
				time.Hour,
				3,
				fixtureConfig(Spec{MaxTurns: 5}),
			)
			return err
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			goal, previous := failedGoalIteration(t, store)
			if err := test.mutate(store, goal, previous); err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueGoalIteration(context.Background()); !errors.Is(err, ErrNoWork) {
				t.Fatalf("ineligible goal continued: %v", err)
			}
		})
	}
}

func TestGoalIterationBlocksAtLimits(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Store, *Goal, *Run) error
	}{
		{name: "run deadline", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Model(&table.Run{}).Where("id = ?", run.ID).
				Update("deadline", gorm.Expr("clock_timestamp() - interval '1 second'")).Error
		}},
		{name: "wall clock", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.db.Model(&table.Goal{}).Where("id = ?", goal.ID).
				Update("created_at", gorm.Expr("clock_timestamp() - interval '2 hours'")).Error
		}},
		{name: "tokens exactly exhausted", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.db.Model(&table.Goal{}).Where("id = ?", goal.ID).Update("tokens_used", 100).Error
		}},
		{name: "cost exactly exhausted", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.db.Model(&table.Goal{}).Where("id = ?", goal.ID).Update("cost_usd", 10).Error
		}},
		{name: "stalled", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.db.Model(&table.Goal{}).Where("id = ?", goal.ID).Update("stall_count", 3).Error
		}},
		{name: "sandbox workspace", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Model(&table.Run{}).Where("id = ?", run.ID).
				Update("spec", gorm.Expr(`jsonb_set(spec, '{filesystem}', '{"provider":"Sandbox","workspace":"/workspace"}')`)).
				Error
		}},
		{name: "aio workspace", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.db.Model(&table.Run{}).Where("id = ?", run.ID).
				Update("spec", gorm.Expr(`jsonb_set(spec, '{filesystem}', '{"provider":"AIO","workspace":"/workspace"}')`)).
				Error
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			goal, previous := failedGoalIteration(t, store)
			if err := test.mutate(store, goal, previous); err != nil {
				t.Fatal(err)
			}
			next, err := store.QueueGoalIteration(context.Background())
			if err != nil || next != nil {
				t.Fatalf("limit did not block: next=%+v err=%v", next, err)
			}
			current, err := store.GetGoal(context.Background(), "owner", goal.ID)
			if err != nil || current.Status != GoalBlocked || current.ActiveRunID != previous.ID {
				t.Fatalf("invalid blocked goal: %+v %v", current, err)
			}
			events, err := store.Events(context.Background(), "owner", previous.ID, 0)
			if err != nil || events[len(events)-1].Kind != "GoalBlocked" {
				t.Fatalf("missing block event: %+v %v", events, err)
			}
		})
	}
}

func TestGoalIterationRollsBackWithEvent(t *testing.T) {
	store := testStore(t)
	goal, previous := failedGoalIteration(t, store)
	if err := store.db.Exec(
		`ALTER TABLE durable_event ADD CONSTRAINT injected_iteration_failure CHECK (kind <> 'GoalIterationQueued')`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueGoalIteration(context.Background()); err == nil {
		t.Fatal("injected event failure was ignored")
	}
	current, err := store.GetGoal(context.Background(), "owner", goal.ID)
	if err != nil || current.ActiveRunID != previous.ID || current.TokensUsed != 7 {
		t.Fatalf("goal changed after rollback: %+v %v", current, err)
	}
	runs, err := store.SessionRuns(context.Background(), "owner", goal.SessionID, "", 100)
	if err != nil || len(runs) != 1 {
		t.Fatalf("partial iteration persisted: %+v %v", runs, err)
	}
	messages, err := store.Messages(context.Background(), "owner", goal.SessionID, 0, 100)
	if err != nil || len(messages) != 2 {
		t.Fatalf("partial iteration message persisted: %+v %v", messages, err)
	}
	if err = store.db.Exec(
		`ALTER TABLE durable_event DROP CONSTRAINT injected_iteration_failure`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if next, err := store.QueueGoalIteration(context.Background()); err != nil || next == nil {
		t.Fatalf("iteration did not recover after rollback: %+v %v", next, err)
	}
}

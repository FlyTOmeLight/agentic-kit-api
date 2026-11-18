package durable

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
)

const blockedDispatchState = `{"version":3,"turn":2,"checkpoint":"cHJlc2VydmU=","future":{"number":9007199254740993},"inflightModel":{"kind":"model","sequence":2,"providerRef":"unknown"}}`

func parkDispatch(t *testing.T, store *Store, run *Run) {
	t.Helper()
	if err := store.CommitBoundary(context.Background(), run.Lease(), Boundary{
		Kind: BoundaryBlock, State: json.RawMessage(blockedDispatchState),
		Detail: "unknown consumption; automatic replay is disabled",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnblockResumesAndAudits(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, store, BudgetSpec{MaxTokens: 100})
	if err := store.RecordUsageWithCost(ctx, run.Lease(), 1, "model", 12, 0.25, run.State); err != nil {
		t.Fatal(err)
	}
	parkDispatch(t, store, run)
	if err := store.Unblock(ctx, "owner", run.ID, run.Epoch, " accepted unknown consumption "); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Queued || got.Epoch != run.Epoch+1 || got.WorkerID != "" || got.LeaseUntil != nil ||
		got.Error != "" || got.Attempt != 0 || got.TokensUsed != 12 || got.CostUSD != 0.25 {
		t.Fatalf("unexpected resumed run: %+v", got)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(got.State, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["inflightModel"] != nil || string(fields["future"]) != `{"number": 9007199254740993}` &&
		string(fields["future"]) != `{"number":9007199254740993}` {
		t.Fatalf("state lost control boundary or precision: %s", got.State)
	}
	if string(fields["checkpoint"]) != `"cHJlc2VydmU="` || string(fields["turn"]) != "2" {
		t.Fatalf("checkpoint changed: %s", got.State)
	}
	updatedGoal, err := store.GetGoal(ctx, "owner", goal.ID)
	if err != nil || updatedGoal.Status != GoalActive || updatedGoal.TokensUsed != 12 ||
		updatedGoal.CostUSD != 0.25 {
		t.Fatalf("goal=%+v err=%v", updatedGoal, err)
	}
	events, err := store.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind != "RunUnblocked" {
			continue
		}
		count++
		var payload struct {
			ActorID       string                     `json:"actorID"`
			Reason        string                     `json:"reason"`
			PreviousError string                     `json:"previousError"`
			BlockedEpoch  int64                      `json:"blockedEpoch"`
			GoalResumed   bool                       `json:"goalResumed"`
			Marker        map[string]json.RawMessage `json:"inflightModel"`
		}
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ActorID != "owner" || payload.Reason != "accepted unknown consumption" ||
			payload.PreviousError == "" || payload.BlockedEpoch != run.Epoch || !payload.GoalResumed ||
			string(payload.Marker["providerRef"]) != `"unknown"` {
			t.Fatalf("audit=%s", event.Payload)
		}
	}
	if count != 1 || len(usageEvents(t, store, run.ID)) != 1 {
		t.Fatalf("unblock events=%d; usage changed", count)
	}
	if err = store.Unblock(ctx, "owner", run.ID, run.Epoch, "duplicate"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate=%v", err)
	}
	replacement := claim(t, store, "replacement")
	if replacement.Epoch <= run.Epoch || replacement.ID != run.ID {
		t.Fatalf("claim=%+v", replacement)
	}
	if err = store.SaveState(ctx, run.Lease(), json.RawMessage(`{}`)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old worker=%v", err)
	}
	parkDispatch(t, store, replacement)
	if err = store.Unblock(ctx, "owner", run.ID, run.Epoch, "stale approval"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale approval=%v", err)
	}
}

func TestUnblockRejectsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		change map[string]any
		goal   map[string]any
		owner  string
		scope  string
		epoch  int64
		reason string
		want   error
	}{
		{name: "wrong owner", owner: "other", want: ErrNotFound},
		{name: "wrong tenant", scope: "other", want: ErrNotFound},
		{name: "old epoch", epoch: 99, want: ErrConflict},
		{name: "invalid epoch", epoch: -1, want: ErrInvalid},
		{name: "blank reason", reason: "   ", want: ErrInvalid},
		{name: "expired", change: map[string]any{"deadline": time.Now().Add(-time.Minute)}, want: ErrConflict},
		{name: "completed", change: map[string]any{"status": Completed}, want: ErrConflict},
		{name: "running", change: map[string]any{"status": Running}, want: ErrConflict},
		{name: "no marker", change: map[string]any{"state": `{"version":3}`}, want: ErrConflict},
		{
			name:   "unknown version",
			change: map[string]any{"state": `{"version":4,"inflightModel":{"kind":"model","sequence":0}}`},
			want:   ErrConflict,
		},
		{name: "token budget", goal: map[string]any{"tokens_used": 100}, want: ErrConflict},
		{name: "cost budget", goal: map[string]any{"cost_usd": 10}, want: ErrConflict},
		{name: "stalled", goal: map[string]any{"stall_count": 3}, want: ErrConflict},
		{name: "goal paused", goal: map[string]any{"status": GoalPaused}, want: ErrConflict},
		{name: "goal complete", goal: map[string]any{"status": GoalComplete}, want: ErrConflict},
		{
			name: "goal expired",
			goal: map[string]any{"created_at": time.Now().Add(-2 * time.Hour)},
			want: ErrConflict,
		},
		{name: "goal changed run", goal: map[string]any{"active_run_id": "other"}, want: ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			run, goal := claimedRunWithGoal(
				t,
				store,
				BudgetSpec{MaxTokens: 100, MaxCostUSD: 10, MaxWallClockSeconds: 3600},
			)
			parkDispatch(t, store, run)
			if len(test.change) > 0 {
				if err := store.db.Model(&table.Run{}).
					Where("id = ?", run.ID).
					Updates(test.change).
					Error; err != nil {
					t.Fatal(err)
				}
			}
			if len(test.goal) > 0 {
				if err := store.db.Model(&table.Goal{}).
					Where("id = ?", goal.ID).
					Updates(test.goal).
					Error; err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.Get(ctx, "owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			owner, epoch, reason := "owner", run.Epoch, "accept"
			if test.owner != "" {
				owner = test.owner
			}
			if test.scope != "" {
				ctx = access.WithScope(ctx, access.Scope{TenantID: test.scope})
			}
			if test.epoch != 0 {
				epoch = test.epoch
			}
			if test.reason != "" {
				reason = test.reason
			}
			if err = store.Unblock(ctx, owner, run.ID, epoch, reason); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			after, err := store.Get(context.Background(), "owner", run.ID)
			if err != nil || after.Status != before.Status || after.EventSeq != before.EventSeq ||
				after.Epoch != before.Epoch || string(after.State) != string(before.State) {
				t.Fatalf("rejection changed run: before=%+v after=%+v error=%v", before, after, err)
			}
		})
	}
}

func TestUnblockBusySessionAndRollback(t *testing.T) {
	for _, scenario := range []string{"busy", "locked session", "event failure"} {
		t.Run(scenario, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			run, goal := claimedRunWithGoal(t, store, BudgetSpec{})
			parkDispatch(t, store, run)
			want := ErrConflict
			switch scenario {
			case "busy":
				busy := createRun(t, store)
				if err := store.db.Model(&table.Run{}).Where("id = ?", busy.ID).
					Update("session_id", run.SessionID).Error; err != nil {
					t.Fatal(err)
				}
			case "locked session":
				tx := store.db.Begin()
				defer tx.Rollback()
				if err := tx.Exec(
					"SELECT id FROM agent_session WHERE id = ? FOR UPDATE",
					run.SessionID,
				).Error; err != nil {
					t.Fatal(err)
				}
			case "event failure":
				want = nil
				if err := store.db.Exec(
					`ALTER TABLE durable_event ADD CONSTRAINT reject_unblock CHECK (kind <> 'RunUnblocked')`,
				).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := store.Unblock(ctx, "owner", run.ID, run.Epoch, "accept")
			if err == nil || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("unblock=%v", err)
			}
			got, err := store.Get(ctx, "owner", run.ID)
			if err != nil || got.Status != Blocked || got.Epoch != run.Epoch {
				t.Fatalf("run=%+v err=%v", got, err)
			}
			if status := getGoalStatus(t, store, goal.ID); status != GoalBlocked {
				t.Fatalf("goal=%s", status)
			}
		})
	}
}

func TestUnblockConcurrentSingleWinner(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, _ := claimedRunWithGoal(t, store, BudgetSpec{})
	parkDispatch(t, store, run)
	start := make(chan struct{})
	results := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- store.Unblock(ctx, "owner", run.ID, run.Epoch, "accept")
		}()
	}
	close(start)
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

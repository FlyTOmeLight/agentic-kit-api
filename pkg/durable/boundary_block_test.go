package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
)

func blockBoundary(detail string) Boundary {
	return Boundary{Kind: BoundaryBlock, State: json.RawMessage(`{}`), Detail: detail}
}

func getGoalStatus(t *testing.T, s *Store, goalID string) GoalStatus {
	t.Helper()
	var goal table.Goal
	if err := s.db.Model(&table.Goal{}).Where("id = ?", goalID).Take(&goal).Error; err != nil {
		t.Fatal(err)
	}
	return goal.Status
}

func TestBoundaryBlockBlocksRunAndGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{})

	err := s.CommitBoundary(ctx, run.Lease(), Boundary{
		Kind:    BoundaryBlock,
		State:   json.RawMessage(`{"v":2}`),
		Detail:  "model usage unavailable",
		Context: &ContextInfo{InputTokens: 10, InputBytes: 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Blocked {
		t.Fatalf("status = %s, want Blocked", got.Status)
	}
	if got.Status.Terminal() {
		t.Fatal("Blocked must not be terminal")
	}
	if got.Error != "model usage unavailable" {
		t.Fatalf("error = %q", got.Error)
	}
	assertJSONEqual(t, got.State, `{"v":2}`)
	if got.WorkerID != "" {
		t.Fatalf("worker = %q, want released", got.WorkerID)
	}
	if got.LeaseUntil != nil {
		t.Fatalf("lease = %v, want released", got.LeaseUntil)
	}
	if got.WaitingStep != "" || got.WaitingKind != "" {
		t.Fatalf("waiting metadata = %q/%q, want cleared", got.WaitingStep, got.WaitingKind)
	}
	if status := getGoalStatus(t, s, goal.ID); status != GoalBlocked {
		t.Fatalf("goal status = %s, want Blocked", status)
	}
	events, err := s.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var blocked *Event
	for i := range events {
		if events[i].Kind == "RunBlocked" {
			blocked = &events[i]
		}
	}
	if blocked == nil {
		t.Fatal("missing RunBlocked event")
	}
	var payload map[string]any
	if err := json.Unmarshal(blocked.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != string(Blocked) || payload["detail"] != "model usage unavailable" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestBoundaryBlockRejectsInvalidCombination(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{})
	cases := []Boundary{
		{Kind: BoundaryBlock, State: json.RawMessage(`{}`)},
		{Kind: BoundaryBlock, State: json.RawMessage(`{}`), Detail: "d", Output: "x"},
		{Kind: BoundaryBlock, State: json.RawMessage(`{}`), Detail: "d", WaitingStep: "s"},
		{Kind: BoundaryBlock, Detail: "d"},
	}
	for _, b := range cases {
		if err := s.CommitBoundary(ctx, run.Lease(), b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("boundary %+v: got %v, want ErrInvalid", b, err)
		}
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running || got.WorkerID != run.WorkerID {
		t.Fatalf("invalid block wrote state: %+v", got)
	}
	if status := getGoalStatus(t, s, goal.ID); status != GoalActive {
		t.Fatalf("goal status = %s, want Active", status)
	}
	events, err := s.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "RunBlocked" {
			t.Fatal("invalid block emitted RunBlocked")
		}
	}
}

func TestBoundaryBlockStaleLeaseKeepsReplacement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{})
	stale := run.Lease()
	if err := s.db.Model(&table.Run{}).Where("id = ?", run.ID).
		Update("lease_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	replacement := claim(t, s, "replacement")
	if replacement.ID != run.ID {
		t.Fatalf("claimed %s, want %s", replacement.ID, run.ID)
	}
	if err := s.CommitBoundary(ctx, stale, blockBoundary("stale")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("got %v, want ErrLeaseLost", err)
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running || got.WorkerID != "replacement" || got.Epoch != replacement.Epoch {
		t.Fatalf("stale block damaged replacement: %+v", got)
	}
	if status := getGoalStatus(t, s, goal.ID); status != GoalActive {
		t.Fatalf("goal status = %s, want Active", status)
	}
	events, err := s.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "RunBlocked" {
			t.Fatal("stale block emitted RunBlocked")
		}
	}
}

func TestBoundaryBlockEventFailureRollsBack(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{})
	if err := s.db.Exec(`CREATE FUNCTION reject_run_blocked() RETURNS trigger AS $$
BEGIN
  IF NEW.kind = 'RunBlocked' THEN
    RAISE EXCEPTION 'reject RunBlocked';
  END IF;
  RETURN NEW;
END
$$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`CREATE TRIGGER reject_run_blocked BEFORE INSERT ON durable_event
FOR EACH ROW EXECUTE FUNCTION reject_run_blocked()`).Error; err != nil {
		t.Fatal(err)
	}
	err := s.CommitBoundary(ctx, run.Lease(), blockBoundary("usage unknown"))
	if err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrLeaseLost) {
		t.Fatalf("got %v, want trigger failure", err)
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running || got.WorkerID != run.WorkerID {
		t.Fatalf("event failure leaked run update: %+v", got)
	}
	if status := getGoalStatus(t, s, goal.ID); status != GoalActive {
		t.Fatalf("goal status = %s, want Active", status)
	}
	events, err := s.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "RunBlocked" {
			t.Fatal("rolled back transaction emitted RunBlocked")
		}
	}
}

func TestBoundaryBlockChildRunBlocksBoundGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parent, goal := claimedRunWithGoal(t, s, BudgetSpec{})
	intent := StepIntent{ID: "spawn-1", Input: "child question", Policy: Idempotent}
	if _, err := s.BeginStep(ctx, parent.Lease(), intent); err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChildRun(ctx, parent.Lease(), intent.ID, intent.Input, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claimedChild := claim(t, s, "child-worker")
	if claimedChild.ID != child.ID {
		t.Fatalf("claimed %s, want child %s", claimedChild.ID, child.ID)
	}
	if err := s.CommitBoundary(ctx, claimedChild.Lease(), blockBoundary("child blocked")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Blocked {
		t.Fatalf("child status = %s, want Blocked", got.Status)
	}
	if got.WorkerID != "" {
		t.Fatalf("child worker = %q, want released", got.WorkerID)
	}
	if status := getGoalStatus(t, s, goal.ID); status != GoalBlocked {
		t.Fatalf("goal status = %s, want Blocked", status)
	}
	parentRun, err := s.Get(ctx, "owner", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parentRun.Status != Running {
		t.Fatalf("parent status = %s, want Running (no terminal cascade)", parentRun.Status)
	}
}

func TestBoundaryBlockConcurrentWithUsage(t *testing.T) {
	for i := 0; i < 4; i++ {
		t.Run(fmt.Sprintf("round-%d", i), func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 1 << 60})
			start := make(chan struct{})
			var wg sync.WaitGroup
			var usageErr, blockErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				usageErr = s.RecordUsage(ctx, run.Lease(), 1, "chat", 4, json.RawMessage(`{}`))
			}()
			go func() {
				defer wg.Done()
				<-start
				blockErr = s.CommitBoundary(ctx, run.Lease(), blockBoundary("usage unknown"))
			}()
			close(start)
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("concurrent usage and block deadlocked")
			}
			if blockErr != nil {
				t.Fatalf("block failed: %v", blockErr)
			}
			got, err := s.Get(ctx, "owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != Blocked {
				t.Fatalf("status = %s, want Blocked", got.Status)
			}
			if status := getGoalStatus(t, s, goal.ID); status != GoalBlocked {
				t.Fatalf("goal status = %s, want Blocked", status)
			}
			switch {
			case usageErr == nil:
				if got.TokensUsed != 4 {
					t.Fatalf("usage committed but tokens = %d, want 4", got.TokensUsed)
				}
				if events := usageEvents(t, s, run.ID); len(events) != 1 {
					t.Fatalf("usage events = %d, want 1", len(events))
				}
			case errors.Is(usageErr, ErrLeaseLost):
				if got.TokensUsed != 0 {
					t.Fatalf("usage rejected but tokens = %d, want 0", got.TokensUsed)
				}
				if events := usageEvents(t, s, run.ID); len(events) != 0 {
					t.Fatalf("usage events = %d, want 0", len(events))
				}
			default:
				t.Fatalf("usage failed with unexpected error: %v", usageErr)
			}
		})
	}
}

func TestBoundaryBlockTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{})
	setRunTenant(t, s, run, "tenant-a", "project-a")
	if err := s.db.Model(&table.Goal{}).Where("id = ?", goal.ID).
		Updates(map[string]any{"tenant_id": "tenant-a", "project_id": "project-a"}).Error; err != nil {
		t.Fatal(err)
	}
	ctxB := access.WithScope(ctx, access.Scope{TenantID: "tenant-b", ProjectID: "project-b"})
	if err := s.CommitBoundary(ctxB, run.Lease(), blockBoundary("cross tenant")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("got %v, want ErrLeaseLost", err)
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running || got.WorkerID != run.WorkerID {
		t.Fatalf("cross-tenant block wrote state: %+v", got)
	}
	if status := getGoalStatus(t, s, goal.ID); status != GoalActive {
		t.Fatalf("goal status = %s, want Active", status)
	}
	events, err := s.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "RunBlocked" {
			t.Fatal("cross-tenant block emitted RunBlocked")
		}
	}
}

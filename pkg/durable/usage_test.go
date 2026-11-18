package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func claimedRunWithGoal(t *testing.T, s *Store, budget BudgetSpec) (*Run, *Goal) {
	t.Helper()
	ctx := context.Background()
	r := createRun(t, s)
	goalSpec := newTestGoal(r.SessionID, "usage-"+NewID(), "usage accounting")
	goalSpec.Budget = budget
	goal, _, err := s.CreateGoal(ctx, goalSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindGoalToRun(ctx, "owner", goal.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, s, "worker")
	if claimed.ID != r.ID {
		t.Fatalf("claimed run %s, want %s", claimed.ID, r.ID)
	}
	return claimed, goal
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid want JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("state = %s, want %s", got, want)
	}
}

func usageEvents(t *testing.T, s *Store, runID string) []Event {
	t.Helper()
	events, err := s.Events(context.Background(), "owner", runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var usage []Event
	for _, event := range events {
		if event.Kind == "UsageRecorded" {
			usage = append(usage, event)
		}
	}
	return usage
}

func TestUsageBudgetBlocksAtThresholdAndStaysBlocked(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 10})

	if err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-1",
		10,
		json.RawMessage(`{"step":1}`),
	); err != nil {
		t.Fatal(err)
	}
	blockedGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedGoal.Status != GoalBlocked || blockedGoal.TokensUsed != 10 {
		t.Fatalf("goal = %s/%d, want Blocked/10", blockedGoal.Status, blockedGoal.TokensUsed)
	}
	if err = s.CheckUsageBudget(ctx, run.Lease()); !errors.Is(err, ErrUsageBudgetExceeded) {
		t.Fatalf("budget check after block = %v, want ErrUsageBudgetExceeded", err)
	}

	if err = s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-2",
		3,
		json.RawMessage(`{"step":2}`),
	); err != nil {
		t.Fatal(err)
	}
	chargedGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chargedGoal.Status != GoalBlocked || chargedGoal.TokensUsed != 13 {
		t.Fatalf(
			"goal after in-flight charge = %s/%d, want Blocked/13",
			chargedGoal.Status,
			chargedGoal.TokensUsed,
		)
	}
	if err = s.CheckUsageBudget(ctx, run.Lease()); !errors.Is(err, ErrUsageBudgetExceeded) {
		t.Fatalf("budget check stayed open: %v", err)
	}

	events := usageEvents(t, s, run.ID)
	if len(events) != 2 {
		t.Fatalf("usage events = %d, want 2", len(events))
	}
	var blockedFlags []bool
	for _, event := range events {
		var payload struct {
			BudgetBlocked bool `json:"budgetBlocked"`
		}
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		blockedFlags = append(blockedFlags, payload.BudgetBlocked)
	}
	if !reflect.DeepEqual(blockedFlags, []bool{true, false}) {
		t.Fatalf("blocked flags = %v, want [true false]", blockedFlags)
	}
}

func TestRecordUsageAggregatesForNonActiveGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 10})
	if err := s.TransitionGoal(ctx, "owner", goal.ID, GoalPaused); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-paused",
		12,
		json.RawMessage(`{"step":"paused"}`),
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != GoalPaused || got.TokensUsed != 12 {
		t.Fatalf("goal = %s/%d, want Paused/12", got.Status, got.TokensUsed)
	}
	if err = s.CheckUsageBudget(ctx, run.Lease()); !errors.Is(err, ErrUsageBudgetExceeded) {
		t.Fatalf("paused over-budget check = %v, want ErrUsageBudgetExceeded", err)
	}
}

func TestRecordUsageKnownZeroTokens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 10})

	if err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-known-zero",
		0,
		json.RawMessage(`{"providerUsage":true,"total":0}`),
	); err != nil {
		t.Fatal(err)
	}
	gotRun, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.TokensUsed != 0 {
		t.Fatalf("run tokens = %d, want 0", gotRun.TokensUsed)
	}
	assertJSONEqual(t, gotRun.State, `{"providerUsage":true,"total":0}`)
	gotGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGoal.TokensUsed != 0 || gotGoal.Status != GoalActive {
		t.Fatalf("goal = %s/%d, want Active/0", gotGoal.Status, gotGoal.TokensUsed)
	}
	var count int64
	if err = s.db.Model(&table.Usage{}).Where("run_id = ?", run.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("usage rows = %d, want 1", count)
	}
	if err = s.CheckUsageBudget(ctx, run.Lease()); err != nil {
		t.Fatalf("known zero usage blocked the next call: %v", err)
	}
}

func TestRecordUsageReplayIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 100})

	if err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-replayed",
		4,
		json.RawMessage(`{"step":"first"}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-replayed",
		40,
		json.RawMessage(`{"step":"replayed"}`),
	); err != nil {
		t.Fatal(err)
	}

	gotRun, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.TokensUsed != 4 {
		t.Fatalf("run tokens = %d, want 4", gotRun.TokensUsed)
	}
	assertJSONEqual(t, gotRun.State, `{"step":"first"}`)
	gotGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGoal.TokensUsed != 4 {
		t.Fatalf("goal tokens = %d, want 4", gotGoal.TokensUsed)
	}
	var usageCount int64
	if err = s.db.Model(&table.Usage{}).Where("run_id = ?", run.ID).Count(&usageCount).Error; err != nil {
		t.Fatal(err)
	}
	if usageCount != 1 {
		t.Fatalf("usage rows = %d, want 1", usageCount)
	}
	if events := usageEvents(t, s, run.ID); len(events) != 1 {
		t.Fatalf("usage events = %d, want 1", len(events))
	}
}

func TestRecordUsageRejectsOldLease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 100})
	oldLease := run.Lease()
	if err := s.db.Exec(
		`UPDATE durable_run SET lease_until = clock_timestamp() - interval '1 second' WHERE id = ?`,
		run.ID,
	).Error; err != nil {
		t.Fatal(err)
	}
	replacement := claim(t, s, "replacement")
	if replacement.ID != run.ID {
		t.Fatalf("claimed run %s, want %s", replacement.ID, run.ID)
	}

	err := s.RecordUsage(
		ctx,
		oldLease,
		1,
		"model-stale",
		9,
		json.RawMessage(`{"step":"stale"}`),
	)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale usage error = %v, want ErrLeaseLost", err)
	}
	var usageCount int64
	if err = s.db.Model(&table.Usage{}).Where("run_id = ?", run.ID).Count(&usageCount).Error; err != nil {
		t.Fatal(err)
	}
	if usageCount != 0 {
		t.Fatalf("usage rows = %d, want 0", usageCount)
	}
	gotRun, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.TokensUsed != 0 {
		t.Fatalf("run tokens = %d, want 0", gotRun.TokensUsed)
	}
	gotGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGoal.TokensUsed != 0 {
		t.Fatalf("goal tokens = %d, want 0", gotGoal.TokensUsed)
	}
}

func TestRecordUsageAfterTerminalDoesNotChargeTwice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 100})
	if err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-before-terminal",
		3,
		json.RawMessage(`{"step":1}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitBoundary(ctx, run.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{"step":2}`),
		Output: "done",
	}); err != nil {
		t.Fatal(err)
	}

	err := s.RecordUsage(
		ctx,
		run.Lease(),
		1,
		"model-after-terminal",
		7,
		json.RawMessage(`{"step":3}`),
	)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("terminal usage error = %v, want ErrLeaseLost", err)
	}
	gotRun, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.TokensUsed != 3 {
		t.Fatalf("run tokens = %d, want 3", gotRun.TokensUsed)
	}
	gotGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGoal.TokensUsed != 3 {
		t.Fatalf("goal tokens = %d, want 3", gotGoal.TokensUsed)
	}
	if events := usageEvents(t, s, run.ID); len(events) != 1 {
		t.Fatalf("usage events = %d, want 1", len(events))
	}
}

func TestCheckUsageBudgetWithoutGoal(t *testing.T) {
	s := testStore(t)
	run := createRun(t, s)
	claimed := claim(t, s, "worker")
	if claimed.ID != run.ID {
		t.Fatalf("claimed run %s, want %s", claimed.ID, run.ID)
	}
	if err := s.CheckUsageBudget(context.Background(), claimed.Lease()); err != nil {
		t.Fatalf("run without goal failed budget check: %v", err)
	}
}

func TestRecordUsageWithCost(t *testing.T) {
	for _, cost := range []float64{0, 0.25, 0.5, 0.75} {
		t.Run(fmt.Sprintf("cost-%g", cost), func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			run, goal := claimedRunWithGoal(t, store, BudgetSpec{MaxCostUSD: 0.5})
			for attempt := 0; attempt < 2; attempt++ {
				if err := store.RecordUsageWithCost(
					ctx,
					run.Lease(),
					1,
					"model-cost",
					0,
					cost,
					json.RawMessage(`{"charged":true}`),
				); err != nil {
					t.Fatal(err)
				}
			}
			gotRun, err := store.Get(ctx, "owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			gotGoal, err := store.GetGoal(ctx, "owner", goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if gotRun.CostUSD != cost || gotGoal.CostUSD != cost {
				t.Fatalf("cost run=%g goal=%g want=%g", gotRun.CostUSD, gotGoal.CostUSD, cost)
			}
			wantStatus := GoalActive
			if cost >= 0.5 {
				wantStatus = GoalBlocked
			}
			if gotGoal.Status != wantStatus {
				t.Fatalf("goal status=%s want=%s", gotGoal.Status, wantStatus)
			}
			budgetErr := store.CheckUsageBudget(ctx, run.Lease())
			if cost >= 0.5 {
				if !errors.Is(budgetErr, ErrUsageBudgetExceeded) {
					t.Fatalf("cost budget check=%v", budgetErr)
				}
			} else if budgetErr != nil {
				t.Fatal(budgetErr)
			}
			var usage table.Usage
			if err := store.db.Where("run_id = ?", run.ID).Take(&usage).Error; err != nil {
				t.Fatal(err)
			}
			if usage.CostUSD != cost || len(usageEvents(t, store, run.ID)) != 1 {
				t.Fatalf("usage replay was not idempotent: %+v", usage)
			}
		})
	}
}

func waitUsageBlockedBy(t *testing.T, ctx context.Context, s *Store, blockerPID int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		if err := s.db.WithContext(ctx).Raw(`
			SELECT pid FROM pg_stat_activity
			WHERE datname = current_database()
			  AND ? = ANY(pg_blocking_pids(pid)) LIMIT 1`, blockerPID).Scan(&pid).Error; err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for a query blocked by backend %d: %v", blockerPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestRecordUsageAndSteerGoalLockOrder(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, goal := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 100})

	conn, err := s.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlConn, err := conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var barrierKey int
	if err = sqlConn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&barrierKey); err != nil {
		sqlConn.Close()
		t.Fatal(err)
	}
	locked := true
	t.Cleanup(func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), time.Second)
		defer unlockCancel()
		if locked {
			_, _ = sqlConn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, barrierKey)
		}
		sqlConn.Close()
	})
	if _, err = sqlConn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, barrierKey); err != nil {
		t.Fatal(err)
	}
	if err = s.db.WithContext(ctx).Exec(`
		CREATE FUNCTION usage_lock_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(TG_ARGV[0]::bigint);
			RETURN NEW;
		END
		$$`).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.db.WithContext(ctx).Exec(fmt.Sprintf(`
		CREATE TRIGGER usage_lock_barrier
		BEFORE UPDATE ON durable_run
		FOR EACH ROW
		WHEN (NEW.id = '%s' AND NEW.state IS DISTINCT FROM OLD.state)
		EXECUTE FUNCTION usage_lock_barrier(%d)`, run.ID, barrierKey)).Error; err != nil {
		t.Fatal(err)
	}

	waitResult := func(what string, result <-chan error) {
		t.Helper()
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s timed out", what)
		}
	}

	recorded := make(chan error, 1)
	go func() {
		recorded <- s.RecordUsage(
			ctx,
			run.Lease(),
			1,
			"model-lock-order",
			4,
			json.RawMessage(`{"barrier":"recorded"}`),
		)
	}()
	recordPID := waitUsageBlockedBy(t, ctx, s, barrierKey)

	steered := make(chan error, 1)
	go func() {
		steered <- s.SteerGoal(ctx, "owner", goal.ID, "keep going")
	}()
	steerPID := waitUsageBlockedBy(t, ctx, s, recordPID)
	var waitingQuery string
	if err = s.db.WithContext(ctx).Raw(`SELECT query FROM pg_stat_activity WHERE pid = ?`, steerPID).
		Scan(&waitingQuery).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("SteerGoal blocked by RecordUsage: %s", waitingQuery)

	if _, err = sqlConn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, barrierKey); err != nil {
		t.Fatal(err)
	}
	locked = false
	waitResult("RecordUsage", recorded)
	waitResult("SteerGoal", steered)

	gotGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGoal.TokensUsed != 4 || gotGoal.Status != GoalActive {
		t.Fatalf("goal = %s/%d, want Active/4", gotGoal.Status, gotGoal.TokensUsed)
	}
	gotRun, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.PendingSteer != "keep going" || gotRun.TokensUsed != 4 {
		t.Fatalf("run steer/tokens = %q/%d, want keep going/4", gotRun.PendingSteer, gotRun.TokensUsed)
	}
}

func TestRecordUsageRechecksGoalBinding(t *testing.T) {
	for _, change := range []string{"replace", "detach", "attach"} {
		t.Run(change, func(t *testing.T) {
			s := testStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			run, original := claimedRunWithGoal(t, s, BudgetSpec{MaxTokens: 100})
			replacement, _, err := s.CreateGoal(
				ctx,
				newTestGoal(run.SessionID, "binding-"+NewID(), "binding change"),
			)
			if err != nil {
				t.Fatal(err)
			}
			nextGoalID := replacement.ID
			if change == "detach" {
				nextGoalID = ""
			}
			if change == "attach" {
				if err = s.db.WithContext(ctx).
					Model(&table.Run{}).
					Where("id = ?", run.ID).
					Update("goal_id", "").
					Error; err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Get(ctx, "owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			tx := s.db.WithContext(ctx).Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var blockerPID int
			if err = tx.Raw(`SELECT pg_backend_pid()`).Scan(&blockerPID).Error; err != nil {
				t.Fatal(err)
			}
			if change == "attach" {
				err = tx.Exec(`SELECT id FROM durable_run WHERE id = ? FOR UPDATE`, run.ID).Error
			} else {
				err = tx.Exec(`SELECT id FROM durable_goal WHERE id = ? FOR UPDATE`, original.ID).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			recorded := make(chan error, 1)
			go func() {
				recorded <- s.RecordUsageWithCost(ctx, run.Lease(), 1, "binding-change", 4, 0.25, json.RawMessage(`{"binding":"changed"}`))
			}()
			waitUsageBlockedBy(t, ctx, s, blockerPID)
			if err = tx.Model(&table.Run{}).
				Where("id = ?", run.ID).
				Update("goal_id", nextGoalID).
				Error; err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-recorded:
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("changed binding: %v, want ErrConflict", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("RecordUsageWithCost timed out after binding change")
			}
			gotRun, err := s.Get(ctx, "owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if gotRun.GoalID != nextGoalID || gotRun.TokensUsed != 0 || gotRun.CostUSD != 0 ||
				gotRun.EventSeq != before.EventSeq {
				t.Fatalf(
					"run after binding conflict: goal=%q tokens=%d cost=%g eventSeq=%d, want %q/0/0/%d",
					gotRun.GoalID,
					gotRun.TokensUsed,
					gotRun.CostUSD,
					gotRun.EventSeq,
					nextGoalID,
					before.EventSeq,
				)
			}
			assertJSONEqual(t, gotRun.State, string(before.State))
			for _, goalID := range []string{original.ID, replacement.ID} {
				gotGoal, err := s.GetGoal(ctx, "owner", goalID)
				if err != nil {
					t.Fatal(err)
				}
				if gotGoal.TokensUsed != 0 || gotGoal.CostUSD != 0 {
					t.Fatalf("usage changed goal after binding conflict: %+v", gotGoal)
				}
			}
			var count int64
			if err = s.db.WithContext(ctx).
				Model(&table.Usage{}).
				Where("run_id = ?", run.ID).
				Count(&count).
				Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 || len(usageEvents(t, s, run.ID)) != 0 {
				t.Fatalf("binding conflict recorded usage: count=%d", count)
			}
		})
	}
}

func TestRecordUsageWithCostRejectsInvalidUsage(t *testing.T) {
	store := &Store{}
	for _, cost := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := store.RecordUsageWithCost(
			context.Background(),
			Lease{},
			1,
			"model",
			0,
			cost,
			nil,
		); !errors.Is(
			err,
			ErrInvalid,
		) {
			t.Fatalf("cost %g: %v", cost, err)
		}
	}
	if err := store.RecordUsageWithCost(
		context.Background(),
		Lease{},
		1,
		"model",
		-1,
		0,
		nil,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("negative tokens: %v", err)
	}
}

func TestParentAndChildUsageChargesSharedGoalConcurrently(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parent := createRun(t, s)
	goalSpec := newTestGoal(parent.SessionID, "usage-concurrent", "concurrent accounting")
	goalSpec.Budget = BudgetSpec{MaxTokens: 7}
	goal, _, err := s.CreateGoal(ctx, goalSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindGoalToRun(ctx, "owner", goal.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	parentRun := claim(t, s, "parent-worker")
	intent := StepIntent{ID: "spawn-usage", Input: "child usage", Policy: Idempotent}
	if _, err = s.BeginStep(ctx, parentRun.Lease(), intent); err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChildRun(ctx, parentRun.Lease(), intent.ID, "child usage", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if child.GoalID != goal.ID || child.ParentRunID != parent.ID {
		t.Fatalf("child goal/parent = %s/%s, want %s/%s", child.GoalID, child.ParentRunID, goal.ID, parent.ID)
	}
	childRun := claim(t, s, "child-worker")
	if childRun.ID != child.ID {
		t.Fatalf("claimed run %s, want child %s", childRun.ID, child.ID)
	}

	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		errorsCh <- s.RecordUsage(
			ctx,
			parentRun.Lease(),
			1,
			"parent-model",
			4,
			json.RawMessage(`{"node":"parent"}`),
		)
	}()
	go func() {
		defer workers.Done()
		<-start
		errorsCh <- s.RecordUsage(
			ctx,
			childRun.Lease(),
			1,
			"child-model",
			5,
			json.RawMessage(`{"node":"child"}`),
		)
	}()
	close(start)
	workers.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	gotGoal, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotGoal.TokensUsed != 9 || gotGoal.Status != GoalBlocked {
		t.Fatalf("goal = %s/%d, want Blocked/9", gotGoal.Status, gotGoal.TokensUsed)
	}
	gotParent, err := s.Get(ctx, "owner", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotChild, err := s.Get(ctx, "owner", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotParent.TokensUsed != 4 || gotChild.TokensUsed != 5 {
		t.Fatalf("run tokens = parent %d child %d, want 4/5", gotParent.TokensUsed, gotChild.TokensUsed)
	}

	var transitions int
	for _, runID := range []string{parent.ID, child.ID} {
		for _, event := range usageEvents(t, s, runID) {
			var payload struct {
				BudgetBlocked bool `json:"budgetBlocked"`
			}
			if err = json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.BudgetBlocked {
				transitions++
			}
		}
	}
	if transitions != 1 {
		t.Fatalf("blocking transitions = %d, want 1", transitions)
	}
}

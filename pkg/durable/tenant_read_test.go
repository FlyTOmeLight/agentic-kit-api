package durable

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"gorm.io/gorm"
)

func setRunTenant(t *testing.T, store *Store, run *Run, tenant, project string) {
	t.Helper()
	fields := map[string]any{"tenant_id": tenant, "project_id": project}
	if err := store.db.Model(&table.Session{}).Where("id = ?", run.SessionID).Updates(fields).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&table.Run{}).Where("id = ?", run.ID).Updates(fields).Error; err != nil {
		t.Fatal(err)
	}
}

func TestTenantRunAndGoalIsolation(t *testing.T) {
	store := testStore(t)
	workerCtx := context.Background()
	scopeA := access.Scope{TenantID: "tenant-a", ProjectID: "project-a"}
	scopeB := access.Scope{TenantID: "tenant-b", ProjectID: "project-b"}
	ctxA := access.WithScope(workerCtx, scopeA)
	ctxB := access.WithScope(workerCtx, scopeB)
	runA, runB := createRun(t, store), createRun(t, store)
	setRunTenant(t, store, runA, scopeA.TenantID, scopeA.ProjectID)
	setRunTenant(t, store, runB, scopeB.TenantID, scopeB.ProjectID)
	goalA, _, err := store.CreateGoal(workerCtx, newTestGoal(runA.SessionID, "shared-key", "goal"))
	if err != nil {
		t.Fatal(err)
	}
	goalB, _, err := store.CreateGoal(ctxB, newTestGoal(runB.SessionID, "shared-key", "goal"))
	if err != nil {
		t.Fatal(err)
	}
	if goalA.ID == goalB.ID {
		t.Fatal("goal idempotency crossed tenants")
	}
	var storedGoal table.Goal
	if err = store.db.Where("id = ?", goalA.ID).Take(&storedGoal).Error; err != nil {
		t.Fatal(err)
	}
	if storedGoal.TenantID != scopeA.TenantID || storedGoal.ProjectID != scopeA.ProjectID {
		t.Fatalf("goal did not inherit session scope: %+v", storedGoal)
	}
	for name, ctx := range map[string]context.Context{
		"other tenant":  ctxB,
		"invalid scope": access.WithScope(workerCtx, access.Scope{}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Get(ctx, "owner", runA.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get: %v", err)
			}
			if _, err := store.GetGoal(ctx, "owner", goalA.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetGoal: %v", err)
			}
			if err := store.Cancel(ctx, "owner", runA.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Cancel: %v", err)
			}
			if _, err := store.Trace(ctx, "owner", runA.ID, 0); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Trace: %v", err)
			}
			if _, _, err := store.CreateGoal(ctx, newTestGoal(runA.SessionID, "denied", "goal")); !errors.Is(err, ErrNotFound) {
				t.Fatalf("CreateGoal: %v", err)
			}
		})
	}
	goals, err := store.ListGoals(ctxB, "owner", "", 10)
	if err != nil || len(goals) != 1 || goals[0].ID != goalB.ID {
		t.Fatalf("ListGoals: %+v, %v", goals, err)
	}
	if _, err = store.GetGoal(ctxA, "owner", goalA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Trace(ctxA, "owner", runA.ID, 0); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		run := claim(t, store, "worker")
		if err = store.CommitBoundary(workerCtx, run.Lease(), Boundary{
			Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "done",
		}); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := store.Get(ctxA, "owner", runA.ID)
	if err != nil || finished.Status != Completed {
		t.Fatalf("worker completion: %+v, %v", finished, err)
	}
	if err = store.Cancel(ctxA, "owner", runA.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTenantSupplyResultIsolation(t *testing.T) {
	for _, kind := range []InteractionKind{InteractionClarification, InteractionReview} {
		t.Run(string(kind), func(t *testing.T) {
			store := testStore(t)
			run := pauseRun(t, store, kind, "answer")
			setRunTenant(t, store, run, "tenant-a", "project-a")
			ctxA := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
			ctxB := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-b"})
			snapshot := func() any {
				t.Helper()
				var state struct {
					Run      table.Run
					Session  table.Session
					Step     table.Step
					Messages int64
					Events   int64
				}
				if err := store.db.Where("id = ?", run.ID).Take(&state.Run).Error; err != nil {
					t.Fatal(err)
				}
				if err := store.db.Where("id = ?", run.SessionID).Take(&state.Session).Error; err != nil {
					t.Fatal(err)
				}
				if err := store.db.Where("run_id = ? AND step_id = ?", run.ID, "answer").Take(&state.Step).Error; err != nil {
					t.Fatal(err)
				}
				if err := store.db.Model(&table.Message{}).Where("run_id = ?", run.ID).Count(&state.Messages).Error; err != nil {
					t.Fatal(err)
				}
				if err := store.db.Model(&table.Event{}).Where("run_id = ?", run.ID).Count(&state.Events).Error; err != nil {
					t.Fatal(err)
				}
				return state
			}
			before := snapshot()
			for _, ctx := range []context.Context{ctxB, access.WithScope(context.Background(), access.Scope{})} {
				if err := store.SupplyResult(ctx, "owner", run.ID, "answer", "accepted", kind); !errors.Is(err, ErrNotFound) {
					t.Fatalf("cross-tenant answer: %v", err)
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("denied answer changed persisted state")
				}
			}
			if err := store.SupplyResult(ctxA, "owner", run.ID, "answer", "accepted", kind); err != nil {
				t.Fatal(err)
			}
			resumed, err := store.Get(ctxA, "owner", run.ID)
			if err != nil || resumed.Status != Queued || resumed.WaitingStep != "" {
				t.Fatalf("same-tenant resume: %+v, %v", resumed, err)
			}
			var step table.Step
			if err = store.db.Where("run_id = ? AND step_id = ?", run.ID, "answer").Take(&step).Error; err != nil {
				t.Fatal(err)
			}
			if step.Status != "Completed" || step.Result != "accepted" {
				t.Fatalf("answer not committed: %+v", step)
			}
			done := snapshot()
			if err = store.SupplyResult(ctxB, "owner", run.ID, "answer", "accepted", kind); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross-tenant completed replay: %v", err)
			}
			if err = store.SupplyResult(ctxA, "owner", run.ID, "answer", "accepted", kind); err != nil {
				t.Fatalf("same-tenant completed replay: %v", err)
			}
			if !reflect.DeepEqual(done, snapshot()) {
				t.Fatal("completed replay changed persisted state")
			}
		})
	}
}

func TestTenantSupplyExternalResultIsolation(t *testing.T) {
	store := testStore(t)
	ctxA := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	ctxB := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-b"})
	run := createRun(t, store)
	setRunTenant(t, store, run, "tenant-a", "project-a")
	claimed := claim(t, store, "worker")
	intent := StepIntent{ID: "external", Input: "deploy", Policy: Idempotent, ExternalRef: "pipelines/42"}
	if _, err := store.BeginStep(context.Background(), claimed.Lease(), intent); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitBoundary(context.Background(), claimed.Lease(), Boundary{
		Kind: BoundaryAwaitExternal, State: json.RawMessage(`{}`), Detail: "awaiting external",
		WaitingStep: intent.ID, ExternalRef: intent.ExternalRef,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() any {
		t.Helper()
		var state struct {
			Run      table.Run
			Session  table.Session
			Step     table.Step
			Messages int64
			Events   int64
		}
		if err := store.db.Where("id = ?", run.ID).Take(&state.Run).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Where("id = ?", run.SessionID).Take(&state.Session).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Where("run_id = ? AND step_id = ?", run.ID, intent.ID).Take(&state.Step).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Model(&table.Message{}).Where("run_id = ?", run.ID).Count(&state.Messages).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.db.Model(&table.Event{}).Where("run_id = ?", run.ID).Count(&state.Events).Error; err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	for _, ctx := range []context.Context{ctxB, access.WithScope(context.Background(), access.Scope{})} {
		if err := store.SupplyExternalResult(ctx, "owner", run.ID, intent.ID, intent.ExternalRef, "ok"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant external result: %v", err)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("denied external result changed persisted state")
		}
	}
	if err := store.SupplyExternalResult(ctxA, "owner", run.ID, intent.ID, intent.ExternalRef, "ok"); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.Get(ctxA, "owner", run.ID)
	if err != nil || resumed.Status != Queued || resumed.WaitingStep != "" {
		t.Fatalf("same-tenant external result: %+v, %v", resumed, err)
	}
	done := snapshot()
	if err = store.SupplyExternalResult(ctxB, "owner", run.ID, intent.ID, intent.ExternalRef, "ok"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant completed replay: %v", err)
	}
	if err = store.SupplyExternalResult(ctxA, "owner", run.ID, intent.ID, intent.ExternalRef, "ok"); err != nil {
		t.Fatalf("same-tenant completed replay: %v", err)
	}
	if !reflect.DeepEqual(done, snapshot()) {
		t.Fatal("completed replay changed persisted state")
	}
	if err = store.Cancel(ctxA, "owner", run.ID); err != nil {
		t.Fatal(err)
	}

	callbackRun := createRun(t, store)
	setRunTenant(t, store, callbackRun, "tenant-b", "project-b")
	callbackLease := claim(t, store, "worker")
	if callbackLease.ID != callbackRun.ID {
		t.Fatalf("claimed %s, want callback run %s", callbackLease.ID, callbackRun.ID)
	}
	callbackIntent := StepIntent{ID: "callback", Input: "deploy", Policy: Idempotent, ExternalRef: "pipelines/43"}
	if _, err = store.BeginStep(context.Background(), callbackLease.Lease(), callbackIntent); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitBoundary(context.Background(), callbackLease.Lease(), Boundary{
		Kind: BoundaryAwaitExternal, State: json.RawMessage(`{}`), Detail: "awaiting callback",
		WaitingStep: callbackIntent.ID, ExternalRef: callbackIntent.ExternalRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.SupplyExternalResultByRunID(
		context.Background(), callbackRun.ID, callbackIntent.ID, callbackIntent.ExternalRef, "ok",
	); err != nil {
		t.Fatalf("scope-free external callback: %v", err)
	}
	resumed, err = store.Get(ctxB, "owner", callbackRun.ID)
	if err != nil || resumed.Status != Queued {
		t.Fatalf("scope-free callback did not resume run: %+v, %v", resumed, err)
	}
}

func TestTenantGoalIterationInheritance(t *testing.T) {
	store := testStore(t)
	goalA, runA := failedGoalIteration(t, store)
	goalB, runB := failedGoalIteration(t, store)
	setRunTenant(t, store, runA, "tenant-a", "project-a")
	setRunTenant(t, store, runB, "tenant-b", "project-b")
	for _, item := range []struct {
		goal    *Goal
		tenant  string
		project string
	}{{goalA, "tenant-a", "project-a"}, {goalB, "tenant-b", "project-b"}} {
		if err := store.db.Model(&table.Goal{}).Where("id = ?", item.goal.ID).
			Updates(map[string]any{"tenant_id": item.tenant, "project_id": item.project}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Model(&table.Goal{}).Where("id = ?", goalA.ID).
		Update("updated_at", gorm.Expr("clock_timestamp()-interval '1 minute'")).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueGoalIteration(access.WithScope(context.Background(), access.Scope{})); !errors.Is(err, ErrNoWork) {
		t.Fatalf("invalid scope: %v", err)
	}
	for _, item := range []struct {
		ctx     context.Context
		goal    *Goal
		tenant  string
		project string
	}{
		{access.WithScope(context.Background(), access.Scope{TenantID: "tenant-b"}), goalB, "tenant-b", "project-b"},
		{context.Background(), goalA, "tenant-a", "project-a"},
	} {
		next, err := store.QueueGoalIteration(item.ctx)
		if err != nil || next == nil || next.GoalID != item.goal.ID {
			t.Fatalf("iteration: %+v, %v", next, err)
		}
		var row table.Run
		if err = store.db.Where("id = ?", next.ID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.TenantID != item.tenant || row.ProjectID != item.project {
			t.Fatalf("iteration lost tenant/project: %+v", row)
		}
	}
}

func TestTenantGoalIterationRejectsMismatchedRun(t *testing.T) {
	store := testStore(t)
	_, previous := failedGoalIteration(t, store)
	if err := store.db.Model(&table.Run{}).Where("id = ?", previous.ID).
		Update("tenant_id", "other-tenant").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueGoalIteration(context.Background()); !errors.Is(err, ErrNoWork) {
		t.Fatalf("cross-tenant previous run selected: %v", err)
	}
}

func TestTenantSubtaskInheritance(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	parent := createRun(t, store)
	setRunTenant(t, store, parent, "tenant-a", "project-a")
	active := claim(t, store, "worker")
	intent := StepIntent{ID: "spawn", Input: "child query", Policy: Idempotent}
	if _, err := store.BeginStep(ctx, active.Lease(), intent); err != nil {
		t.Fatal(err)
	}
	child, err := store.SpawnChildRun(ctx, active.Lease(), intent.ID, intent.Input, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var childRun table.Run
	var childSession table.Session
	if err = store.db.Where("id = ?", child.ID).Take(&childRun).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.db.Where("id = ?", child.SessionID).Take(&childSession).Error; err != nil {
		t.Fatal(err)
	}
	if childRun.TenantID != "tenant-a" || childRun.ProjectID != "project-a" ||
		childSession.TenantID != "tenant-a" || childSession.ProjectID != "project-a" {
		t.Fatalf("child scope: run=%s/%s session=%s/%s",
			childRun.TenantID, childRun.ProjectID, childSession.TenantID, childSession.ProjectID)
	}
	if err = store.CommitBoundary(ctx, active.Lease(), Boundary{
		Kind: BoundaryAwaitExternal, State: json.RawMessage(`{}`), Detail: "awaiting subtask",
		WaitingStep: intent.ID, ExternalRef: childExternalRefPrefix + child.ID,
	}); err != nil {
		t.Fatal(err)
	}
	childLease := claim(t, store, "worker")
	if childLease.ID != child.ID {
		t.Fatalf("claimed %s, want child %s", childLease.ID, child.ID)
	}
	if err = store.CommitBoundary(ctx, childLease.Lease(), Boundary{
		Kind: BoundaryComplete, State: json.RawMessage(`{}`), Output: "child result",
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.Get(ctx, "owner", parent.ID)
	if err != nil || resumed.Status != Queued {
		t.Fatalf("worker did not resume parent: %+v, %v", resumed, err)
	}
}

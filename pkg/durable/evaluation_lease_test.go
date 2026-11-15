package durable

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func pendingEvaluation(t *testing.T, store *Store) (*Goal, *Run) {
	t.Helper()
	goal, run := evaluationWindowGoalRun(t, store, EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"})
	claimed := claim(t, store, "execution-worker")
	activateEvaluationEnvironment(t, store, claimed)
	completeEvaluationWindowRun(t, store, claimed.Lease())
	return goal, run
}

func TestEvaluationClaimTakeoverFencesOldWorker(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	goal, original := pendingEvaluation(t, store)
	var winners []*Run
	var mutex sync.Mutex
	var workers sync.WaitGroup
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			run, err := store.ClaimEvaluation(ctx, NewID(), time.Minute, "")
			if err == nil {
				mutex.Lock()
				winners = append(winners, run)
				mutex.Unlock()
			} else if !errors.Is(err, ErrNoWork) {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if len(winners) != 1 {
		t.Fatalf("evaluation owners=%d, want 1", len(winners))
	}
	first := winners[0]
	if first.Status != Completed || first.WorkerID != "" || first.LeaseUntil != nil {
		t.Fatalf("evaluation claim changed run execution: %+v", first)
	}
	if err := store.RenewEvaluation(ctx, first, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().Model(&table.Run{}).Where("id = ?", original.ID).
		Update("evaluation_lease_until", gorm.Expr("clock_timestamp() - interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimEvaluation(ctx, "replacement", time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.EvaluationEpoch <= first.EvaluationEpoch ||
		second.EvaluationWorkerID != "execution-worker" ||
		!second.EvaluationDeadline.Equal(*first.EvaluationDeadline) ||
		second.EvaluationLeaseUntil.After(*second.EvaluationDeadline) {
		t.Fatalf("takeover changed binding or extended window: first=%+v second=%+v", first, second)
	}
	if err := store.RenewEvaluation(ctx, first, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old heartbeat accepted: %v", err)
	}
	if _, err := store.RecordClaimedEvaluation(
		ctx,
		first,
		EvaluationPassed,
		"stale",
	); !errors.Is(
		err,
		ErrLeaseLost,
	) {
		t.Fatalf("old receipt accepted: %v", err)
	}
	if _, err := store.RecordEvaluation(
		ctx,
		"owner",
		goal.ID,
		original.ID,
		EvaluationPassed,
		"bypass",
	); !errors.Is(
		err,
		ErrLeaseLost,
	) {
		t.Fatalf("unfenced receipt accepted: %v", err)
	}
	if _, err := store.RecordClaimedEvaluation(ctx, second, EvaluationPassed, "pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckGoalProgress(ctx, "owner", goal.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().Model(&table.Run{}).Where("id = ?", original.ID).
		Update("evaluation_lease_until", gorm.Expr("clock_timestamp() - interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	cleanup, err := store.ClaimEvaluation(ctx, "cleanup-worker", time.Minute, "")
	if err != nil {
		t.Fatalf("committed verdict with active environment must be recovered: %v", err)
	}
	receipt, err := store.GetEvaluation(ctx, "owner", goal.ID, original.ID)
	if err != nil || receipt.Output != "pass" {
		t.Fatalf("receipt changed during cleanup claim: %+v %v", receipt, err)
	}
	env, err := store.ActiveEnvironment(ctx, cleanup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MarkEnvironmentReleased(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.DB().Model(&table.Run{}).Where("id = ?", original.ID).
		Update("evaluation_lease_until", gorm.Expr("clock_timestamp() - interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimEvaluation(ctx, "finished", time.Minute, ""); !errors.Is(err, ErrNoWork) {
		t.Fatalf("finished evaluation reclaimed: %v", err)
	}
}

func TestEvaluationClaimEligibility(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Store, *Goal, *Run) error
	}{
		{name: "paused", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.TransitionGoal(context.Background(), "owner", goal.ID, GoalPaused)
		}},
		{name: "expired window", mutate: func(store *Store, _ *Goal, run *Run) error {
			return store.DB().Model(&table.Run{}).Where("id = ?", run.ID).
				Update("evaluation_deadline", gorm.Expr("clock_timestamp() - interval '1 second'")).
				Error
		}},
		{name: "expired goal budget", mutate: func(store *Store, goal *Goal, _ *Run) error {
			return store.DB().Model(&table.Goal{}).Where("id = ?", goal.ID).
				Updates(map[string]any{"budget": `{"maxWallClockSeconds":1}`, "created_at": gorm.Expr("clock_timestamp() - interval '2 seconds'")}).
				Error
		}},
		{name: "immutable error receipt", mutate: func(store *Store, goal *Goal, run *Run) error {
			_, err := store.RecordEvaluation(
				context.Background(),
				"owner",
				goal.ID,
				run.ID,
				EvaluationError,
				"unknown",
			)
			return err
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			goal, run := pendingEvaluation(t, store)
			if err := test.mutate(store, goal, run); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimEvaluation(
				context.Background(),
				"worker",
				time.Minute,
				"",
			); !errors.Is(
				err,
				ErrNoWork,
			) {
				t.Fatalf("ineligible evaluation claimed: %v", err)
			}
		})
	}
}

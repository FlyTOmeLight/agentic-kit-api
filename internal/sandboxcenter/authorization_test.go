package sandboxcenter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func TestAcquireRequiresRunClaim(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		OwnerID: "owner", RequestKey: "key", ProfileID: "coding",
		RunID: "run", WorkerID: "worker", RunEpoch: 1, HardExpiresAt: time.Now().Add(time.Hour),
	}
	if _, _, err := store.Acquire(ctx, in); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("missing run: %v", err)
	}
	in.RunEpoch = 0
	if _, _, err := store.Acquire(ctx, in); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("missing run with zero epoch: %v", err)
	}
	in.RunEpoch = 1
	createRunClaim(t, store, in)
	for _, epoch := range []int64{-1, 0, 2} {
		in.RunEpoch = epoch
		if _, _, err := store.Acquire(ctx, in); !errors.Is(err, ErrLeaseClosed) {
			t.Fatalf("invalid epoch %d: %v", epoch, err)
		}
	}
	in.RunEpoch = 1
	in.WorkerID = "other"
	if _, _, err := store.Acquire(ctx, in); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("wrong worker: %v", err)
	}
	var count int64
	if err := store.db.Model(&Sandbox{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("denied acquisition persisted sandbox: count=%d err=%v", count, err)
	}
}

func operationLease(t *testing.T, store *Store) *Lease {
	t.Helper()
	in := AcquireInput{
		OwnerID: "owner", RequestKey: "key", ProfileID: "coding",
		RunID: "run", WorkerID: "worker", RunEpoch: 1, HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ActivateForRun(
		context.Background(),
		sb.ID,
		"pod",
		in.RunID,
		in.WorkerID,
		in.RunEpoch,
		in.HardExpiresAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestDispatchRechecksAuthority(t *testing.T) {
	for _, mutation := range []string{
		"UPDATE durable_run SET epoch = epoch + 1, worker_id = 'other'",
		"UPDATE durable_run SET status = 'Completed', worker_id = '', lease_until = NULL",
		"UPDATE durable_run SET status = 'Canceled', worker_id = '', lease_until = NULL",
		"UPDATE durable_run SET lease_until = clock_timestamp() - interval '1 second'",
		"UPDATE durable_run SET deadline = clock_timestamp() - interval '1 second'",
		"UPDATE sandbox_lease SET status = 'Closed'",
		"UPDATE sandbox_lease SET hard_expires_at = clock_timestamp() - interval '1 second'",
	} {
		t.Run(mutation, func(t *testing.T) {
			store := testStore(t)
			lease := operationLease(t, store)
			ctx := context.Background()
			op, err := store.BeginOperation(ctx, lease.ID, "command", "command", payloadHashOf("true"))
			if err != nil {
				t.Fatal(err)
			}
			if err = store.db.Exec(mutation).Error; err != nil {
				t.Fatal(err)
			}
			if err = store.MarkDispatchStarted(ctx, op.ID, "exec"); !errors.Is(err, ErrLeaseClosed) {
				t.Fatalf("dispatch after loss of authority: %v", err)
			}
			fresh, err := store.GetOperation(ctx, op.ID)
			if err != nil || fresh.Status != OpStatusPending || fresh.DispatchedAt != nil {
				t.Fatalf("rejected dispatch changed receipt: %+v %v", fresh, err)
			}
			if _, err = store.BeginOperation(
				ctx,
				lease.ID,
				"new",
				"command",
				payloadHashOf("true"),
			); !errors.Is(
				err,
				ErrLeaseClosed,
			) {
				t.Fatalf("new operation after loss of authority: %v", err)
			}
		})
	}
}

func TestDispatchHasSingleWinner(t *testing.T) {
	store := testStore(t)
	lease := operationLease(t, store)
	ctx := context.Background()
	op, err := store.BeginOperation(ctx, lease.ID, "command", "command", payloadHashOf("true"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			results <- store.MarkDispatchStarted(ctx, op.ID, "exec")
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("unexpected dispatch error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("dispatch winners = %d, want 1", winners)
	}
	service := NewService(store, nil)
	if _, err := service.ExecCommand(ctx, lease.SandboxID, "command", "true"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("in-flight operation must not reach provider again: %v", err)
	}
}

func TestCompletedRunOnlyAllowsBoundEvaluation(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutation string
		key      string
		kind     string
		script   string
	}{
		{name: "ordinary command", key: "ordinary"},
		{name: "different script", script: "touch arbitrary"},
		{name: "different kind", kind: "file"},
		{name: "missing goal", mutation: "UPDATE durable_run SET goal_id = ''"},
		{name: "paused goal", mutation: "UPDATE durable_goal SET status = 'Paused'"},
		{name: "complete goal", mutation: "UPDATE durable_goal SET status = 'Complete'"},
		{name: "other active run", mutation: "UPDATE durable_goal SET active_run_id = 'other'"},
		{name: "other owner", mutation: "UPDATE durable_goal SET owner_id = 'other'"},
		{name: "missing evaluator", mutation: `UPDATE durable_goal SET evaluator = '{"kind":"none"}'`},
		{name: "empty evaluator", mutation: `UPDATE durable_goal SET evaluator = '{"kind":"command","config":"  "}'`},
		{name: "http evaluator", mutation: `UPDATE durable_goal SET evaluator = '{"kind":"http","config":"true"}'`},
		{name: "goal deadline", mutation: `UPDATE durable_goal SET created_at = clock_timestamp() - interval '2 seconds', budget = '{"maxWallClockSeconds":1}'`},
		{name: "old epoch", mutation: "UPDATE durable_run SET epoch = 2"},
		{name: "missing worker", mutation: "UPDATE sandbox_lease SET worker_id = ''"},
		{name: "canceled run", mutation: "UPDATE durable_run SET status = 'Canceled'"},
		{name: "run deadline", mutation: "UPDATE durable_run SET deadline = clock_timestamp() - interval '1 second'"},
		{name: "missing evaluation deadline", mutation: "UPDATE durable_run SET evaluation_deadline = NULL"},
		{name: "evaluation deadline", mutation: "UPDATE durable_run SET evaluation_deadline = clock_timestamp() - interval '1 second'"},
		{name: "wrong evaluation worker", mutation: "UPDATE durable_run SET evaluation_worker_id = 'other'"},
		{name: "released environment", mutation: "UPDATE durable_run_environment SET status = 'Released'"},
		{name: "wrong environment epoch", mutation: "UPDATE durable_run_environment SET run_epoch = 2"},
		{name: "wrong environment sandbox", mutation: "UPDATE durable_run_environment SET sandbox_id = 'other'"},
		{name: "wrong environment lease", mutation: "UPDATE durable_run_environment SET lease_id = 'other'"},
		{name: "wrong environment generation", mutation: "UPDATE durable_run_environment SET generation = 2"},
		{name: "sandbox owner", mutation: "UPDATE sandbox SET owner_id = 'other'"},
		{name: "sandbox status", mutation: "UPDATE sandbox SET status = 'Releasing'"},
		{name: "sandbox lease", mutation: "UPDATE sandbox SET lease_id = 'other'"},
		{name: "sandbox generation", mutation: "UPDATE sandbox SET generation = 2"},
		{name: "closed lease", mutation: "UPDATE sandbox_lease SET status = 'Closed'"},
		{name: "expired lease", mutation: "UPDATE sandbox_lease SET hard_expires_at = clock_timestamp() - interval '1 second'"},
		{name: "goal already evaluated", mutation: `INSERT INTO durable_evaluation (id, owner_id, goal_id, run_id, status, output)
			SELECT 'eval', owner_id, goal_id, id, 'Passed', '' FROM durable_run`},
		{name: "evaluation failed", mutation: `INSERT INTO durable_evaluation (id, owner_id, goal_id, run_id, status, output)
			SELECT 'eval', owner_id, goal_id, id, 'Failed', '' FROM durable_run`},
		{name: "evaluation error", mutation: `INSERT INTO durable_evaluation (id, owner_id, goal_id, run_id, status, output)
			SELECT 'eval', owner_id, goal_id, id, 'Error', '' FROM durable_run`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			lease := operationLease(t, store)
			createEvaluationBinding(t, store, lease, "owner", "true")
			ctx := context.Background()
			if err := store.db.Model(&table.Run{}).Where("id = ?", lease.HolderRunID).
				Updates(map[string]any{
					"status":               "Completed",
					"worker_id":            "",
					"lease_until":          nil,
					"evaluation_worker_id": lease.WorkerID,
					"evaluation_deadline":  time.Now().Add(time.Minute),
				}).Error; err != nil {
				t.Fatal(err)
			}
			key, kind, script := "eval-"+lease.SandboxID, "command", "true"
			claim := EvaluationClaim{WorkerID: lease.WorkerID, Epoch: 1}
			if test.key != "" {
				key = test.key
			}
			if test.kind != "" {
				kind = test.kind
			}
			if test.script != "" {
				script = test.script
			}
			var pending *Operation
			if test.mutation != "" {
				var err error
				pending, err = store.BeginOperation(
					ctx,
					lease.ID,
					key,
					kind,
					payloadHashOf(script),
					claim,
				)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.db.Exec(test.mutation).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.BeginOperation(
				ctx,
				lease.ID,
				key,
				kind,
				payloadHashOf(script),
				claim,
			); !errors.Is(
				err,
				ErrLeaseClosed,
			) {
				t.Fatalf("unauthorized evaluation: %v", err)
			}
			if pending != nil {
				if err := store.MarkDispatchStarted(
					ctx,
					pending.ID,
					"exec",
					claim,
				); !errors.Is(
					err,
					ErrLeaseClosed,
				) {
					t.Fatalf("evaluation dispatch not rechecked: %v", err)
				}
				wantErr := ErrLeaseClosed
				if test.name == "expired lease" {
					wantErr = ErrLeaseExpired
				}
				if _, _, err := store.Renew(
					ctx,
					lease.ID,
					lease.Revision+1,
					"renew",
					"renew-hash",
					time.Now().Add(time.Hour),
				); !errors.Is(
					err,
					wantErr,
				) {
					t.Fatalf("unauthorized evaluation renewal: %v, want %v", err, wantErr)
				}
				current, err := store.GetLease(ctx, lease.ID)
				if err != nil || current.Revision != lease.Revision {
					t.Fatalf("rejected renewal changed revision: lease=%+v err=%v", current, err)
				}
			}
		})
	}
}

func TestCompletedRunEvaluationRenewal(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutation string
	}{
		{name: "allowed"},
		{name: "expired window", mutation: "UPDATE durable_run SET evaluation_deadline = clock_timestamp() - interval '1 second'"},
		{name: "wrong worker", mutation: "UPDATE durable_run SET evaluation_worker_id = 'other'"},
		{name: "wrong epoch", mutation: "UPDATE durable_run SET epoch = epoch + 1"},
		{name: "goal paused", mutation: "UPDATE durable_goal SET status = 'Paused'"},
		{name: "environment released", mutation: "UPDATE durable_run_environment SET status = 'Released'"},
		{name: "already evaluated", mutation: `INSERT INTO durable_evaluation (id, owner_id, goal_id, run_id, status, output)
			SELECT 'eval', owner_id, goal_id, id, 'Passed', '' FROM durable_run`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			lease := operationLease(t, store)
			createEvaluationBinding(t, store, lease, "owner", "true")
			if err := store.db.Exec(
				"UPDATE sandbox_lease SET hard_expires_at = clock_timestamp() + interval '15 seconds' WHERE id = ?",
				lease.ID,
			).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.db.Exec(
				`UPDATE durable_run SET status = 'Completed', worker_id = '', lease_until = NULL,
				evaluation_worker_id = ?, evaluation_deadline = clock_timestamp() + interval '2 minutes'`,
				lease.WorkerID,
			).Error; err != nil {
				t.Fatal(err)
			}
			if test.mutation != "" {
				if err := store.db.Exec(test.mutation).Error; err != nil {
					t.Fatal(err)
				}
			}
			updated, _, err := store.Renew(
				context.Background(),
				lease.ID,
				2,
				"renew",
				"renew-hash",
				time.Now().Add(time.Hour),
			)
			if test.mutation != "" {
				if !errors.Is(err, ErrLeaseClosed) {
					t.Fatalf("renew after %s: %v", test.name, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var deadline time.Time
			if err := store.db.Raw("SELECT evaluation_deadline FROM durable_run WHERE id = ?", lease.HolderRunID).
				Scan(&deadline).
				Error; err != nil {
				t.Fatal(err)
			}
			if updated.Revision != 2 || !updated.HardExpiresAt.Equal(deadline) ||
				!updated.HardExpiresAt.After(time.Now().Add(time.Minute)) {
				t.Fatalf(
					"renewal not clamped to evaluation deadline: lease=%+v deadline=%v",
					updated,
					deadline,
				)
			}
			if _, err := store.BeginOperation(
				context.Background(),
				lease.ID,
				"ordinary",
				"command",
				payloadHashOf("true"),
			); !errors.Is(
				err,
				ErrLeaseClosed,
			) {
				t.Fatalf("ordinary command allowed during evaluation: %v", err)
			}
		})
	}
}

func TestCompletedEvaluationRenewsWithinDeadline(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutation   string
		deadline   string
		requestTTL time.Duration
	}{
		{name: "evaluation window", deadline: "evaluation_deadline", requestTTL: time.Hour},
		{name: "earlier run deadline", mutation: `UPDATE durable_run SET deadline = clock_timestamp() + interval '30 seconds'`, deadline: "deadline", requestTTL: time.Hour},
		{name: "earlier goal budget", mutation: `UPDATE durable_goal SET budget = '{"maxWallClockSeconds":30}'`, deadline: "goal", requestTTL: time.Hour},
		{name: "later goal budget", mutation: `UPDATE durable_goal SET budget = '{"maxWallClockSeconds":3600}'`, deadline: "evaluation_deadline", requestTTL: time.Hour},
		{name: "requested deadline", deadline: "requested", requestTTL: 20 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			lease := operationLease(t, store)
			createEvaluationBinding(t, store, lease, "owner", "true")
			if err := store.db.Exec(
				`UPDATE sandbox_lease SET hard_expires_at = clock_timestamp() + interval '10 seconds'`,
			).Error; err != nil {
				t.Fatal(err)
			}
			previous, err := store.GetLease(ctx, lease.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.db.Exec(
				`UPDATE durable_run SET status = 'Completed', worker_id = '', lease_until = NULL,
				evaluation_worker_id = ?, evaluation_deadline = clock_timestamp() + interval '2 minutes'`,
				lease.WorkerID,
			).Error; err != nil {
				t.Fatal(err)
			}
			if test.mutation != "" {
				if err := store.db.Exec(test.mutation).Error; err != nil {
					t.Fatal(err)
				}
			}
			requested := time.Now().UTC().Add(test.requestTTL).Truncate(time.Microsecond)
			expected := requested
			switch test.deadline {
			case "evaluation_deadline":
				err = store.db.Raw("SELECT evaluation_deadline FROM durable_run").Scan(&expected).Error
			case "deadline":
				err = store.db.Raw("SELECT deadline FROM durable_run").Scan(&expected).Error
			case "goal":
				err = store.db.Raw("SELECT created_at + interval '30 seconds' FROM durable_goal").
					Scan(&expected).
					Error
			}
			if err != nil {
				t.Fatal(err)
			}
			renewed, replayed, err := store.Renew(
				ctx,
				lease.ID,
				lease.Revision+1,
				"renew",
				"renew-hash",
				requested,
			)
			if err != nil || replayed || renewed == nil {
				t.Fatalf(
					"completed evaluation renewal: lease=%+v replayed=%v err=%v",
					renewed,
					replayed,
					err,
				)
			}
			if !renewed.HardExpiresAt.Equal(expected) || renewed.Revision != lease.Revision+1 {
				t.Fatalf(
					"renewed lease=%+v, want deadline=%v revision=%d",
					renewed,
					expected,
					lease.Revision+1,
				)
			}
			if !renewed.HardExpiresAt.After(previous.HardExpiresAt) {
				t.Fatalf(
					"evaluation did not extend old lease: old=%v renewed=%v",
					previous.HardExpiresAt,
					renewed.HardExpiresAt,
				)
			}
			if _, err := store.BeginOperation(
				ctx,
				lease.ID,
				"ordinary",
				"command",
				payloadHashOf("true"),
			); !errors.Is(
				err,
				ErrLeaseClosed,
			) {
				t.Fatalf("renewal granted ordinary command authority: %v", err)
			}
			claim := EvaluationClaim{WorkerID: lease.WorkerID, Epoch: 1}
			op, err := store.BeginOperation(
				ctx,
				lease.ID,
				"eval-"+lease.SandboxID,
				"command",
				payloadHashOf("true"),
				claim,
			)
			if err != nil {
				t.Fatalf("bound evaluation after renewal: %v", err)
			}
			if err := store.MarkDispatchStarted(ctx, op.ID, "exec", claim); err != nil {
				t.Fatalf("bound evaluation dispatch after renewal: %v", err)
			}
		})
	}
}

func TestEvaluationAuthorizationPreservesDatabaseError(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := operationLease(t, store)
	createEvaluationBinding(t, store, lease, "owner", "true")
	if err := store.db.Exec(`UPDATE durable_run SET status = 'Completed', worker_id = '', lease_until = NULL,
		evaluation_worker_id = ?, evaluation_deadline = clock_timestamp() + interval '2 minutes'`, lease.WorkerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Exec("ALTER TABLE durable_evaluation RENAME TO unavailable_evaluation").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Renew(
		ctx,
		lease.ID,
		lease.Revision+1,
		"renew",
		"renew-hash",
		time.Now().Add(time.Hour),
	); err == nil ||
		errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("renewal hid database failure: %v", err)
	}
	if _, err := store.BeginOperation(
		ctx,
		lease.ID,
		"eval-"+lease.SandboxID,
		"command",
		payloadHashOf("true"),
		EvaluationClaim{WorkerID: lease.WorkerID, Epoch: 1},
	); err == nil ||
		errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("command authorization hid database failure: %v", err)
	}
}

func TestCompletedEvaluationRejectsExpiredRenewalDeadline(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	lease := operationLease(t, store)
	createEvaluationBinding(t, store, lease, "owner", "true")
	if err := store.db.Exec(`UPDATE durable_run SET status = 'Completed', worker_id = '', lease_until = NULL,
		evaluation_worker_id = ?, evaluation_deadline = clock_timestamp() + interval '2 minutes'`, lease.WorkerID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Renew(
		ctx,
		lease.ID,
		lease.Revision+1,
		"renew",
		"renew-hash",
		time.Now().Add(-time.Minute),
	); !errors.Is(
		err,
		ErrLeaseExpired,
	) {
		t.Fatalf("expired renewal deadline: %v", err)
	}
	current, err := store.GetLease(ctx, lease.ID)
	if err != nil || current.Revision != lease.Revision || !current.HardExpiresAt.Equal(lease.HardExpiresAt) {
		t.Fatalf("rejected renewal changed lease: lease=%+v err=%v", current, err)
	}
}

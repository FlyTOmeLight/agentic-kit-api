package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	frameworkdb "github.com/FlyTOmeLight/server-go/db"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	db, err := frameworkdb.NewDB(ctx, &frameworkdb.Config{
		Driver:       frameworkdb.DriverPostgreSQL,
		DSN:          dsn,
		MaxOpenConns: 4,
		MaxIdleConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("sandbox_test_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + schemaName).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA " + schemaName + " CASCADE")
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	db2, err := frameworkdb.NewDB(ctx, &frameworkdb.Config{
		Driver:       frameworkdb.DriverPostgreSQL,
		DSN:          u.String(),
		MaxOpenConns: 4,
		MaxIdleConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := db2.Session(&gorm.Session{})
	t.Cleanup(func() {
		if sqlDB, err := db2.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := session.AutoMigrate(
		&table.Agent{},
		&table.AgentVersion{},
		&table.Session{},
		&table.Run{},
		&table.Goal{},
		&table.Evaluation{},
		&table.Step{},
		&table.Event{},
		&table.Message{},
		&table.RunEnvironment{},
		&Sandbox{},
		&Lease{},
		&Operation{},
	); err != nil {
		t.Fatal(err)
	}
	return NewStore(session)
}

func createRunClaim(t *testing.T, store *Store, in AcquireInput) {
	t.Helper()
	err := store.db.Exec(
		`INSERT INTO durable_run
		(id, owner_id, session_id, request_key, request_hash, spec, state, status,
		 worker_id, epoch, lease_until, deadline, max_attempts)
		VALUES (?, ?, ?, ?, 'hash', '{}', '{}', 'Running', ?, ?,
		        clock_timestamp() + interval '4 hours', clock_timestamp() + interval '5 hours', 3)`,
		in.RunID, in.OwnerID, in.RunID, in.RunID, in.WorkerID, in.RunEpoch,
	).Error
	if err != nil {
		t.Fatal(err)
	}
}

func databaseNow(t *testing.T, store *Store) time.Time {
	t.Helper()
	var now time.Time
	if err := store.db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		t.Fatal(err)
	}
	return now
}

func TestAcquireReplay(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		TenantID:      "t1",
		OwnerID:       "run-1",
		RequestKey:    "req-1",
		ProfileID:     "coding",
		WorkerID:      "w1",
		RunID:         "run-1",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb1, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	sb2, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if sb1.ID != sb2.ID {
		t.Fatalf("replay returned different sandbox: %s vs %s", sb1.ID, sb2.ID)
	}
}

func TestRenewMonotonic(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		TenantID:      "t1",
		OwnerID:       "run-2",
		RequestKey:    "req-2",
		ProfileID:     "coding",
		WorkerID:      "w1",
		RunID:         "run-2",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := store.ActivateForRun(ctx, sb.ID, "pod-uid-1", in.RunID, "w1", in.RunEpoch, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ls.Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", ls.Revision)
	}

	// Renew to revision 2
	ls2, replayed, err := store.Renew(ctx, ls.ID, 2, "renew-2", "hash2", time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("first renew should not be replayed")
	}
	if ls2.Revision != 2 {
		t.Fatalf("revision = %d, want 2", ls2.Revision)
	}

	// Replay same revision 2 with same params -> replayed
	_, replayed, err = store.Renew(ctx, ls.ID, 2, "renew-2", "hash2", time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !replayed {
		t.Fatal("replay should be marked")
	}

	// Old revision -> conflict
	_, _, err = store.Renew(ctx, ls.ID, 1, "renew-1", "hash1", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("old revision should conflict")
	}
}

func TestOperationReceiptIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		TenantID:      "t1",
		OwnerID:       "run-3",
		RequestKey:    "req-3",
		ProfileID:     "coding",
		WorkerID:      "w1",
		RunID:         "run-3",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := store.ActivateForRun(ctx, sb.ID, "pod-uid-1", in.RunID, "w1", in.RunEpoch, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	op, err := store.BeginOperation(ctx, ls.ID, "cmd-1", "command", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	op2, err := store.BeginOperation(ctx, ls.ID, "cmd-1", "command", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != op2.ID {
		t.Fatalf("replay returned different op: %s vs %s", op.ID, op2.ID)
	}

	// Dispatch started
	if err := store.MarkDispatchStarted(ctx, op.ID, "exec-123"); err != nil {
		t.Fatal(err)
	}

	// Complete
	exitCode := 0
	if err := store.CompleteOperation(ctx, op.ID, &exitCode, "hello", ""); err != nil {
		t.Fatal(err)
	}

	// Read back
	got, err := store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != OpStatusSucceeded {
		t.Fatalf("status = %s, want Succeeded", got.Status)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", got.ExitCode)
	}
}

func TestReleaseClosesLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		TenantID:      "t1",
		OwnerID:       "run-4",
		RequestKey:    "req-4",
		ProfileID:     "coding",
		WorkerID:      "w1",
		RunID:         "run-4",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ActivateForRun(ctx, sb.ID, "pod-uid-1", in.RunID, "w1", in.RunEpoch, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	sb2, err := store.Release(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sb2.Status != SandboxStatusReleasing {
		t.Fatalf("status = %s, want Releasing", sb2.Status)
	}

	if err := store.ConfirmDestroyed(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}

	var final Sandbox
	if err := store.DB().Where("id = ?", sb.ID).First(&final).Error; err != nil {
		t.Fatal(err)
	}
	if final.Status != SandboxStatusDestroyed {
		t.Fatalf("status = %s, want Destroyed", final.Status)
	}
	if final.DestroyedAt == nil {
		t.Fatal("destroyed_at not set")
	}
}

func TestRunAuthorizationClampsAndBlocksStaleWorker(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	runExpiry := time.Now().Add(2 * time.Minute).UTC()
	if err := store.DB().Exec(
		`INSERT INTO durable_run
		 (id, owner_id, session_id, request_key, request_hash, spec, state, status,
		  worker_id, epoch, lease_until, deadline, max_attempts)
		 VALUES ('run-auth-1', 'owner-1', 'sess-1', 'rk-1', 'rh-1', '{}', '{}', 'Running',
		         'worker-a', 2, ?, ?, 3)`,
		runExpiry, runExpiry.Add(10*time.Minute),
	).Error; err != nil {
		t.Fatal(err)
	}

	// Stale epoch or wrong worker is rejected before any sandbox row is created.
	staleIn := AcquireInput{
		TenantID:      "t1",
		OwnerID:       "owner-1",
		RequestKey:    "req-auth-stale",
		ProfileID:     "coding",
		WorkerID:      "worker-a",
		RunID:         "run-auth-1",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	if _, _, err := store.Acquire(ctx, staleIn); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("stale epoch acquire must fail with ErrLeaseClosed, got %v", err)
	}

	// Matching claim succeeds, and ActivateForRun clamps HardExpiresAt to
	// min(requested, LEAST(lease_until, deadline)).
	in := staleIn
	in.RequestKey = "req-auth-ok"
	in.RunEpoch = 2
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Replay with a different wall-clock HardExpiresAt still matches the same
	// acquisition (HTTP replay regression check).
	replayIn := in
	replayIn.HardExpiresAt = in.HardExpiresAt.Add(5 * time.Second)
	replaySB, _, err := store.Acquire(ctx, replayIn)
	if err != nil || replaySB.ID != sb.ID {
		t.Fatalf("acquire replay with shifted wall-clock failed: sb=%+v err=%v", replaySB, err)
	}

	ls, err := store.ActivateForRun(
		ctx,
		sb.ID,
		"pod-auth-1",
		"run-auth-1",
		"worker-a",
		2,
		time.Now().Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if ls.HolderRunID != "run-auth-1" || ls.HolderRunEpoch != 2 {
		t.Fatalf("lease holder = (%s, %d), want (run-auth-1, 2)", ls.HolderRunID, ls.HolderRunEpoch)
	}
	if ls.HardExpiresAt.After(runExpiry.Add(time.Second)) {
		t.Fatalf("lease deadline %v was not clamped to run lease %v", ls.HardExpiresAt, runExpiry)
	}

	// Complete one operation while worker-a holds epoch 2.
	op, err := store.BeginOperation(ctx, ls.ID, "call-1", "command", "sha256:1")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MarkDispatchStarted(ctx, op.ID, "exec-1"); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err = store.CompleteOperation(ctx, op.ID, &zero, "ok", ""); err != nil {
		t.Fatal(err)
	}

	// Simulate worker-b taking over the run under epoch 3: worker-a's sandbox
	// lease has not expired locally, but both Renew and new BeginOperation
	// must be rejected immediately.
	if err = store.DB().Exec(
		`UPDATE durable_run SET worker_id = 'worker-b', epoch = 3 WHERE id = 'run-auth-1'`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Renew(
		ctx,
		ls.ID,
		2,
		"renew-2",
		hashRenew(*ls, 2),
		time.Now().Add(time.Hour),
	); !errors.Is(
		err,
		ErrLeaseClosed,
	) {
		t.Fatalf("renew after run takeover must fail with ErrLeaseClosed, got %v", err)
	}
	if _, err = store.BeginOperation(ctx, ls.ID, "call-2", "command", "sha256:2"); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("new operation after run takeover must fail with ErrLeaseClosed, got %v", err)
	}

	// Reading back the already-completed operation receipt still succeeds so
	// the new worker can reconcile without re-running the side effect.
	replayedOp, err := store.BeginOperation(ctx, ls.ID, "call-1", "command", "sha256:1")
	if err != nil {
		t.Fatalf("completed receipt replay must succeed after takeover, got %v", err)
	}
	if replayedOp.ID != op.ID || replayedOp.Status != OpStatusSucceeded {
		t.Fatalf("unexpected replayed operation: %+v", replayedOp)
	}
}

func TestCompletedRunAllowsPostRunEvaluationOperation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	runExpiry := time.Now().Add(2 * time.Minute).UTC()
	if err := store.DB().Exec(
		`INSERT INTO durable_run
		 (id, owner_id, session_id, request_key, request_hash, spec, state, status,
		  worker_id, epoch, lease_until, deadline, max_attempts)
		 VALUES ('run-eval-1', 'owner-1', 'sess-1', 'rk-eval-1', 'rh-eval-1', '{}', '{}', 'Running',
		         'worker-a', 1, ?, ?, 3)`,
		runExpiry, runExpiry.Add(10*time.Minute),
	).Error; err != nil {
		t.Fatal(err)
	}

	in := AcquireInput{
		TenantID:      "t1",
		OwnerID:       "owner-1",
		RequestKey:    "req-eval-1",
		ProfileID:     "coding",
		WorkerID:      "worker-a",
		RunID:         "run-eval-1",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := store.ActivateForRun(
		ctx,
		sb.ID,
		"pod-eval-1",
		"run-eval-1",
		"worker-a",
		1,
		time.Now().Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}

	createEvaluationBinding(t, store, ls, "owner-1", "false")
	if err := store.DB().Exec(
		`UPDATE durable_run SET status = 'Completed', lease_until = NULL, worker_id = '',
		 evaluation_worker_id = 'worker-a', evaluation_deadline = clock_timestamp() + interval '1 minute'
		 WHERE id = 'run-eval-1'`,
	).Error; err != nil {
		t.Fatal(err)
	}

	claim := EvaluationClaim{WorkerID: ls.WorkerID, Epoch: 1}
	op, err := store.BeginOperation(ctx, ls.ID, "eval-"+sb.ID, "command", payloadHashOf("false"), claim)
	if err != nil {
		t.Fatalf(
			"post-run evaluation operation must be allowed for Completed run before sandbox release: %v",
			err,
		)
	}
	if err := store.MarkDispatchStarted(ctx, op.ID, "guard:pod-eval-1", claim); err != nil {
		t.Fatal(err)
	}
	stream := `{"type":"init","text":"e1"}` + "\n" +
		`{"type":"stdout","text":"FAIL\n"}` + "\n" +
		`{"type":"error","error":{"ename":"CommandExecError","evalue":"1","traceback":["exit status 1"],"exit_code":1}}`
	exitCode, errStr := parseExecdOutput(stream)
	if err := store.CompleteOperation(ctx, op.ID, exitCode, stream, errStr); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != OpStatusFailed || got.ExitCode == nil || *got.ExitCode != 1 || got.Error != "exit status 1" {
		t.Fatalf("unexpected completed operation: %+v", got)
	}
}

func createEvaluationBinding(t *testing.T, store *Store, lease *Lease, owner, command string) {
	t.Helper()
	agent := table.Agent{ID: "agent-" + lease.ID, OwnerID: owner, Name: "test", Revision: 1}
	session := table.Session{ID: "session-" + lease.ID, OwnerID: owner, AgentID: agent.ID}
	goal := table.Goal{
		ID: "goal-" + lease.ID, OwnerID: owner, SessionID: session.ID,
		RequestKey: lease.ID, RequestHash: "hash", Objective: "test",
		Status: execution.GoalActive, ActiveRunID: lease.HolderRunID,
		Evaluator: execution.EvaluatorSpec{Kind: execution.EvaluatorCommand, Config: "  " + command + "\n"},
	}
	env := table.RunEnvironment{
		ID:              "env-" + lease.ID,
		RunID:           lease.HolderRunID,
		RequestKey:      lease.ID,
		RunEpoch:        lease.HolderRunEpoch,
		ProfileID:       "coding",
		ProfileRevision: 1,
		SandboxID:       lease.SandboxID,
		LeaseID:         lease.ID,
		Generation:      lease.SandboxGeneration,
		Status:          envStatusActive,
	}
	for _, record := range []any{&agent, &session, &goal, &env} {
		if err := store.db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.Model(&table.Run{}).Where("id = ?", lease.HolderRunID).
		Updates(map[string]any{
			"goal_id": goal.ID, "evaluation_claim_worker_id": lease.WorkerID,
			"evaluation_epoch": 1, "evaluation_lease_until": time.Now().Add(time.Minute),
		}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestParseExecdOutput(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	cases := []struct {
		name     string
		raw      string
		wantCode *int
		wantErr  string
	}{
		{
			name: "complete with exit_code 0",
			raw: "{\"type\":\"init\",\"text\":\"1\"}\n" +
				"{\"type\":\"stdout\",\"text\":\"ok\\n\"}\n" +
				"{\"type\":\"execution_complete\",\"timestamp\":10,\"execution_time\":5,\"exit_code\":0}\n",
			wantCode: intPtr(0),
			wantErr:  "",
		},
		{
			name: "legacy complete without exit_code infers 0",
			raw: "data: {\"type\":\"init\",\"text\":\"1\"}\n\n" +
				"data: {\"type\":\"execution_complete\",\"timestamp\":10,\"execution_time\":5}\n\n",
			wantCode: intPtr(0),
			wantErr:  "",
		},
		{
			name: "command exec error with non-zero exit_code",
			raw: "{\"type\":\"init\",\"text\":\"2\"}\n" +
				"{\"type\":\"stdout\",\"text\":\"failing test\\n\"}\n" +
				"{\"type\":\"error\",\"error\":{\"ename\":\"CommandExecError\",\"evalue\":\"3\",\"traceback\":[\"exit status 3\"],\"exit_code\":3}}\n",
			wantCode: intPtr(3),
			wantErr:  "exit status 3",
		},
		{
			name: "signal terminated reports -1",
			raw: "{\"type\":\"init\",\"text\":\"3\"}\n" +
				"{\"type\":\"error\",\"error\":{\"ename\":\"CommandExecError\",\"evalue\":\"-1\",\"traceback\":[\"signal: terminated\"],\"exit_code\":-1}}\n",
			wantCode: intPtr(-1),
			wantErr:  "signal: terminated",
		},
		{
			name:     "legacy command exec error falls back to numeric evalue",
			raw:      "{\"type\":\"error\",\"error\":{\"ename\":\"CommandExecError\",\"evalue\":\"7\"}}\n",
			wantCode: intPtr(7),
			wantErr:  "7",
		},
		{
			name:     "start failure without exit_code keeps nil exitCode",
			raw:      "{\"type\":\"error\",\"error\":{\"ename\":\"CommandExecError\",\"evalue\":\"fork/exec /bin/sh: argument list too long\"}}\n",
			wantCode: nil,
			wantErr:  "CommandExecError: fork/exec /bin/sh: argument list too long",
		},
		{
			name:     "truncated stream without terminal frame",
			raw:      "{\"type\":\"init\",\"text\":\"4\"}\n{\"type\":\"stdout\",\"text\":\"partial\"}\n",
			wantCode: nil,
			wantErr:  "incomplete command stream: missing terminal event",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCode, gotErr := parseExecdOutput(tc.raw)
			if (gotCode == nil) != (tc.wantCode == nil) || (gotCode != nil && *gotCode != *tc.wantCode) {
				t.Fatalf("exitCode = %v, want %v", gotCode, tc.wantCode)
			}
			if gotErr != tc.wantErr {
				t.Fatalf("errStr = %q, want %q", gotErr, tc.wantErr)
			}
		})
	}
}

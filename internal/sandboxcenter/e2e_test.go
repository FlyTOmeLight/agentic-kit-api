package sandboxcenter

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestSandboxLifecycleE2E runs the phase-2 loop against a real PostgreSQL
// schema and a real Kubernetes cluster: acquire, provision, activate, admit,
// renew, exec, replay, release and confirm destruction. It is opt-in because
// it creates and deletes real pods.
//
//	SANDBOX_E2E_K8S_CONTEXT=orbstack TEST_DATABASE_URL=... \
//	  go test -run TestSandboxLifecycleE2E -v ./internal/sandboxcenter/
func TestSandboxLifecycleE2E(t *testing.T) {
	cluster := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if cluster == "" {
		t.Skip("SANDBOX_E2E_K8S_CONTEXT required for the Kubernetes e2e test")
	}
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:phase2"
	}
	store := testStore(t)
	provider, err := NewProvider(K8sConfig{
		Context:     cluster,
		Namespace:   "sandbox-center",
		GuardImage:  image,
		MaxLifetime: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	in := AcquireInput{
		TenantID:      "e2e",
		OwnerID:       "e2e-run",
		RequestKey:    fmt.Sprintf("e2e-%d", time.Now().UnixNano()),
		ProfileID:     "coding",
		WorkerID:      "e2e-worker",
		RunID:         "e2e-run",
		RunEpoch:      1,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if fresh, err := store.GetSandbox(
			cleanupCtx,
			sb.ID,
		); err == nil &&
			fresh.Status != SandboxStatusDestroyed {
			_ = provider.Destroy(cleanupCtx, *fresh)
		}
	})

	if err := svc.provision(ctx, sb, acquireRequest{
		TTLSeconds: 3600,
		WorkerID:   in.WorkerID,
		RequestKey: in.RequestKey,
		RunID:      in.RunID,
		RunEpoch:   in.RunEpoch,
	}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	active, err := store.GetSandbox(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != SandboxStatusActive || active.PodUID == "" {
		t.Fatalf("sandbox not active with pod uid: %+v", active)
	}
	t.Logf("sandbox=%s pod=%s lease=%s", active.ID, active.PodUID, active.LeaseID)

	lease, err := store.GetLease(ctx, active.LeaseID)
	if err != nil {
		t.Fatal(err)
	}

	// Renew with a new revision; guard must accept the same binding.
	renewed, replayed, err := store.Renew(
		ctx,
		lease.ID,
		lease.Revision+1,
		"renew-2",
		hashRenew(*lease, lease.Revision+1),
		provider.LeaseDeadline(lease.CreatedAt, time.Now().Add(2*time.Hour)),
	)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if replayed {
		t.Fatal("new revision must not be a replay")
	}
	if err := provider.admit(ctx, *active, admissionRequest{
		SandboxID: active.ID, RunID: lease.HolderRunID, WorkerID: lease.WorkerID,
		Epoch: lease.HolderRunEpoch, LeaseID: lease.ID, FencingToken: lease.HolderRunEpoch,
		AdmissionEpoch: 1, Generation: active.Generation, RuntimeBootID: active.ID,
		Revision: renewed.Revision, Deadline: renewed.HardExpiresAt, RequestKey: "renew-2",
	}); err != nil {
		t.Fatalf("admit renewal: %v", err)
	}
	t.Logf("renewed revision=%d deadline=%s", renewed.Revision, renewed.HardExpiresAt)

	// Exec: durable receipt, then dispatch, then terminal receipt.
	script := `echo "uid=$(id -u)"; echo ok > /workspace/e2e.txt; cat /workspace/e2e.txt`
	op, err := store.BeginOperation(ctx, lease.ID, "exec-1", "command", payloadHashOf(script))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDispatchStarted(ctx, op.ID, "guard:"+active.PodUID); err != nil {
		t.Fatal(err)
	}
	out, status, err := provider.guardCall(ctx, *active, "/command", map[string]string{
		"operationId": op.ID, "script": script,
	})
	if err != nil {
		t.Fatalf("guard exec (%d): %v", status, err)
	}
	exitCode, errStr := parseExecdOutput(string(out))
	if err := store.CompleteOperation(ctx, op.ID, exitCode, string(out), errStr); err != nil {
		t.Fatal(err)
	}
	done, err := store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != OpStatusSucceeded || done.ReceiptStoredAt == nil || done.ExitCode == nil ||
		*done.ExitCode != 0 {
		t.Fatalf("operation not terminal: %+v", done)
	}
	t.Logf("exec output: %q", done.Output)

	// Same request key must return the same operation, not a second dispatch.
	replay, err := store.BeginOperation(ctx, lease.ID, "exec-1", "command", payloadHashOf(script))
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != op.ID || replay.Status != OpStatusSucceeded {
		t.Fatalf("replay returned a different receipt: %+v", replay)
	}

	// Release: close admission, delete the pod, confirm destruction.
	if _, err := store.Release(ctx, active.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := provider.Destroy(ctx, *active); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if err := store.ConfirmDestroyed(ctx, active.ID); err != nil {
		t.Fatalf("confirm destroyed: %v", err)
	}
	final, err := store.GetSandbox(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != SandboxStatusDestroyed || final.DestroyedAt == nil {
		t.Fatalf("sandbox not destroyed: %+v", final)
	}
	if _, _, err := provider.guardCall(ctx, *active, "/health", map[string]string{}); err == nil {
		t.Fatal("guard still reachable after pod deletion")
	}
}

func TestCompletedEvaluationRenewalE2E(t *testing.T) {
	cluster := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if cluster == "" {
		t.Skip("SANDBOX_E2E_K8S_CONTEXT required for the Kubernetes e2e test")
	}
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:phase2"
	}
	store := testStore(t)
	provider, err := NewProvider(K8sConfig{
		Context: cluster, Namespace: "sandbox-center", GuardImage: image, MaxLifetime: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	in := AcquireInput{
		OwnerID: "eval-owner", RequestKey: "eval-e2e", ProfileID: "coding",
		WorkerID: "eval-worker", RunID: "eval-run", RunEpoch: 1, HardExpiresAt: time.Now().Add(time.Minute),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		if fresh, err := store.GetSandbox(
			cleanupCtx,
			sb.ID,
		); err == nil &&
			fresh.Status != SandboxStatusDestroyed {
			if err := provider.Destroy(cleanupCtx, *fresh); err != nil {
				t.Errorf("destroy test sandbox: %v", err)
			}
		}
	})
	if err := NewService(store, provider).provision(ctx, sb, acquireRequest{
		TTLSeconds: 20,
		WorkerID:   in.WorkerID,
		RequestKey: in.RequestKey,
		RunID:      in.RunID,
		RunEpoch:   in.RunEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	active, err := store.GetSandbox(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.GetLease(ctx, active.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	createEvaluationBinding(t, store, lease, in.OwnerID, "echo accepted")
	if err := store.db.Exec(`UPDATE durable_run SET status = 'Completed', worker_id = '', lease_until = NULL,
		evaluation_worker_id = ?, evaluation_deadline = clock_timestamp() + interval '2 minutes'
		WHERE id = ?`, in.WorkerID, in.RunID).Error; err != nil {
		t.Fatal(err)
	}
	renewed, _, err := store.Renew(ctx, lease.ID, 2, "eval-renew", "eval-renew-hash", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.HardExpiresAt.After(lease.HardExpiresAt) {
		t.Fatal("renewal did not extend the original lease")
	}
	if err := provider.admit(ctx, *active, admissionRequest{
		SandboxID: active.ID, RunID: lease.HolderRunID, WorkerID: lease.WorkerID,
		Epoch: lease.HolderRunEpoch, LeaseID: lease.ID, FencingToken: lease.HolderRunEpoch,
		AdmissionEpoch: 1, Generation: active.Generation, RuntimeBootID: active.ID,
		Revision: renewed.Revision, Deadline: renewed.HardExpiresAt, RequestKey: "eval-renew",
	}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(lease.HardExpiresAt) + time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	op, err := NewService(store, provider).ExecCommand(ctx, active.ID, "eval-"+active.ID, "echo accepted",
		EvaluationClaim{WorkerID: lease.WorkerID, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if time.Now().Before(lease.HardExpiresAt) || op.Status != OpStatusSucceeded || op.ExitCode == nil ||
		*op.ExitCode != 0 {
		t.Fatalf("evaluation did not finish successfully beyond the original lease: %+v", op)
	}
}

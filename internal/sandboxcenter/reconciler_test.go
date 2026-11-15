package sandboxcenter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPendingReconciliationScopes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	recent := AcquireInput{
		TenantID: "t", OwnerID: "r-recent", RequestKey: "k-recent", ProfileID: "coding",
		WorkerID: "w", RunID: "r-recent", RunEpoch: 1, HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, recent)
	recentSB, _, err := store.Acquire(ctx, recent)
	if err != nil {
		t.Fatal(err)
	}

	stale := recent
	stale.OwnerID = "r-stale"
	stale.RunID = "r-stale"
	stale.RequestKey = "k-stale"
	createRunClaim(t, store, stale)
	staleSB, _, err := store.Acquire(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the stale provisioning record past the grace window.
	if err := store.db.Model(&Sandbox{}).Where("id = ?", staleSB.ID).
		Update("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	// An active sandbox whose lease is past its hard deadline must reconcile.
	expired := recent
	expired.OwnerID = "r-expired"
	expired.RunID = "r-expired"
	expired.RequestKey = "k-expired"
	createRunClaim(t, store, expired)
	expiredSB, _, err := store.Acquire(ctx, expired)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ActivateForRun(
		ctx,
		expiredSB.ID,
		"pod-uid",
		expired.RunID,
		"w",
		expired.RunEpoch,
		time.Now().Add(-time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}

	// A releasing sandbox whose destruction was never confirmed must reconcile.
	releasing := recent
	releasing.OwnerID = "r-releasing"
	releasing.RunID = "r-releasing"
	releasing.RequestKey = "k-releasing"
	createRunClaim(t, store, releasing)
	releasingSB, _, err := store.Acquire(ctx, releasing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Release(ctx, releasingSB.ID); err != nil {
		t.Fatal(err)
	}

	pending, err := store.PendingReconciliation(ctx, 10*time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, sb := range pending {
		got[sb.ID] = true
	}
	if got[recentSB.ID] {
		t.Error("recently provisioning sandbox must not be reconciled yet")
	}
	for id, name := range map[string]string{
		staleSB.ID:     "stale provisioning",
		expiredSB.ID:   "expired lease",
		releasingSB.ID: "releasing",
	} {
		if !got[id] {
			t.Errorf("missing %s sandbox %s in reconciliation set", name, id)
		}
	}

	// After destruction is confirmed the sandbox leaves the set.
	if err = store.ConfirmDestroyed(ctx, releasingSB.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingReconciliation(ctx, 10*time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, sb := range pending {
		if sb.ID == releasingSB.ID {
			t.Error("destroyed sandbox must leave the reconciliation set")
		}
	}
	if lease.Status != LeaseStatusActive {
		t.Fatalf("test setup: lease %+v", lease)
	}
}

func TestMarkLostClosesProvisioningOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		TenantID: "t", OwnerID: "r", RequestKey: "k", ProfileID: "coding",
		WorkerID: "w", RunID: "r", RunEpoch: 1, HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sb, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.markLost(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.markLost(ctx, sb.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second markLost must conflict, got %v", err)
	}
	got, err := store.GetSandbox(ctx, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SandboxStatusLost {
		t.Fatalf("sandbox status %s, want Lost", got.Status)
	}

	// An activated sandbox is never marked lost by the orphan path.
	in.RequestKey = "k2"
	sb2, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ActivateForRun(
		ctx,
		sb2.ID,
		"pod-uid",
		in.RunID,
		"w",
		in.RunEpoch,
		time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if err = store.markLost(ctx, sb2.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("markLost on active sandbox must conflict, got %v", err)
	}
}

func TestCloseEnvironmentsForSandbox(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, sandbox := range []Sandbox{
		{ID: "sb-destroyed", OwnerID: "owner", RequestKey: "destroyed", Status: SandboxStatusDestroyed},
		{ID: "sb-orphan", OwnerID: "owner", RequestKey: "orphan", Status: SandboxStatusLost},
	} {
		if err := store.db.Create(&sandbox).Error; err != nil {
			t.Fatal(err)
		}
	}

	insert := func(id, runID, requestKey, sandboxID, status string) {
		t.Helper()
		if err := store.db.Exec(
			`INSERT INTO durable_run_environment
			 (id, run_id, request_key, run_epoch, profile_id, profile_revision, sandbox_id, status)
			 VALUES (?, ?, ?, 1, 'coding', 1, ?, ?)`,
			id, runID, requestKey, sandboxID, status,
		).Error; err != nil {
			t.Fatal(err)
		}
	}
	statusOf := func(id string) string {
		t.Helper()
		var status string
		if err := store.db.Raw(`SELECT status FROM durable_run_environment WHERE id = ?`, id).
			Scan(&status).Error; err != nil {
			t.Fatal(err)
		}
		return status
	}

	insert("env-active", "r", "k-active", "sb-destroyed", envStatusActive)
	insert("env-released", "r", "k-released", "sb-destroyed", envStatusReleased)
	insert("env-other", "r", "k-other", "sb-other", envStatusActive)
	insert("env-orphan", "r", "k-orphan", "sb-orphan", envStatusPreparing)

	// Confirmed destruction releases the workspace: unfinished records bound
	// to the sandbox become Released; terminal rows are untouched.
	if err := store.CloseEnvironmentsForSandbox(ctx, "sb-destroyed", true); err != nil {
		t.Fatal(err)
	}
	if got := statusOf("env-active"); got != envStatusReleased {
		t.Fatalf("destroyed sandbox environment must be Released, got %s", got)
	}
	if got := statusOf("env-released"); got != envStatusReleased {
		t.Fatalf("already released record must stay Released, got %s", got)
	}
	if got := statusOf("env-other"); got != envStatusActive {
		t.Fatalf("environment of another sandbox must stay Active, got %s", got)
	}

	// A lost sandbox never activated: its intents close as Lost.
	if err := store.CloseEnvironmentsForSandbox(ctx, "sb-orphan", false); err != nil {
		t.Fatal(err)
	}
	if got := statusOf("env-orphan"); got != envStatusLost {
		t.Fatalf("lost sandbox environment must be Lost, got %s", got)
	}

	// Re-closing is a no-op so repeated sweeps do not fail.
	if err := store.CloseEnvironmentsForSandbox(ctx, "sb-destroyed", true); err != nil {
		t.Fatal(err)
	}
}

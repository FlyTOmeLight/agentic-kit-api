package sandboxcenter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequiredSaveBlocksReleaseAndExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "release"
		if expired {
			name = "expiry"
		}
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			in := AcquireInput{
				OwnerID: "owner", RequestKey: "save", ProfileID: "coding",
				WorkerID: "worker", RunID: "run", RunEpoch: 1,
				HardExpiresAt: time.Now().Add(time.Hour), RequiredSave: true,
			}
			createRunClaim(t, store, in)
			sandbox, _, err := store.Acquire(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			changed := in
			changed.RequiredSave = false
			if _, _, err := store.Acquire(ctx, changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("replay changed save policy: %v", err)
			}
			if err := store.recordPhysicalIdentity(
				ctx,
				sandbox.ID,
				"pod-original",
				"pvc-original",
			); err != nil {
				t.Fatal(err)
			}
			lease, err := store.ActivateForRun(
				ctx,
				sandbox.ID,
				"pod-original",
				in.RunID,
				in.WorkerID,
				in.RunEpoch,
				in.HardExpiresAt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				if err := store.db.Model(&Lease{}).Where("id = ?", lease.ID).
					Update("hard_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
			}
			expectedStatus := ""
			if expired {
				expectedStatus = SandboxStatusActive
			}
			blocked, err := store.release(ctx, sandbox.ID, expectedStatus)
			if err != nil {
				t.Fatal(err)
			}
			if blocked.Status != SandboxStatusSaveBlocked || blocked.DestroyedAt != nil {
				t.Fatalf("required save not protected: %+v", blocked)
			}
			replay, err := store.Release(ctx, sandbox.ID)
			if err != nil || replay.Version != blocked.Version {
				t.Fatalf("release replay: %+v %v", replay, err)
			}
			if _, _, err := store.Renew(
				ctx,
				lease.ID,
				2,
				"renew",
				"hash",
				in.HardExpiresAt,
			); !errors.Is(
				err,
				ErrLeaseClosed,
			) {
				t.Fatalf("renew after release: %v", err)
			}
			if _, err := store.BeginOperation(
				ctx,
				lease.ID,
				"write",
				"command",
				"hash",
			); !errors.Is(
				err,
				ErrLeaseClosed,
			) {
				t.Fatalf("new operation after release: %v", err)
			}
			if err := (&Provider{}).Destroy(ctx, *blocked); !errors.Is(err, ErrSaveBlocked) {
				t.Fatalf("physical deletion not blocked: %v", err)
			}
			if err := store.ConfirmDestroyed(ctx, sandbox.ID); !errors.Is(err, ErrSaveBlocked) {
				t.Fatalf("destruction confirmation not blocked: %v", err)
			}
			if err := store.confirmRequiredSave(
				ctx,
				blocked.ID,
				blocked.Generation,
				blocked.Version,
				"checkpoint-1",
			); !errors.Is(
				err,
				ErrConflict,
			) {
				t.Fatalf("save accepted without stop evidence: %v", err)
			}
			pending, err := store.PendingReconciliation(ctx, time.Minute, 100)
			if err != nil || len(pending) != 1 || pending[0].ID != blocked.ID {
				t.Fatalf("stop retry missing: %+v %v", pending, err)
			}
			if err := store.confirmStopped(ctx, *blocked); err != nil {
				t.Fatal(err)
			}
			stopped, err := store.GetSandbox(ctx, blocked.ID)
			if err != nil {
				t.Fatal(err)
			}
			pending, err = store.PendingReconciliation(ctx, time.Minute, 100)
			if err != nil || len(pending) != 1 || pending[0].Status != SandboxStatusSaveBlocked {
				t.Fatalf("stopped blocked sandbox missing from save retries: %+v %v", pending, err)
			}
			for _, scope := range []struct{ generation, version int64 }{
				{generation: stopped.Generation + 1, version: stopped.Version},
				{generation: stopped.Generation, version: blocked.Version},
			} {
				if err := store.confirmRequiredSave(
					ctx,
					stopped.ID,
					scope.generation,
					scope.version,
					"checkpoint-1",
				); !errors.Is(
					err,
					ErrConflict,
				) {
					t.Fatalf("stale save scope accepted: %v", err)
				}
			}
			if err := store.confirmRequiredSave(
				ctx,
				stopped.ID,
				stopped.Generation,
				stopped.Version,
				"",
			); !errors.Is(
				err,
				ErrInvalidInput,
			) {
				t.Fatalf("empty checkpoint accepted: %v", err)
			}
			for repeat := 0; repeat < 2; repeat++ {
				if err := store.confirmRequiredSave(
					ctx,
					stopped.ID,
					stopped.Generation,
					stopped.Version,
					"checkpoint-1",
				); err != nil {
					t.Fatal(err)
				}
			}
			pending, err = store.PendingReconciliation(ctx, time.Minute, 100)
			if err != nil || len(pending) != 1 || pending[0].Status != SandboxStatusReleasing {
				t.Fatalf("saved sandbox not eligible for cleanup: %+v %v", pending, err)
			}
			if err := store.ConfirmDestroyed(ctx, stopped.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExpiryRechecksRenewedLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		OwnerID: "owner", RequestKey: "expiry", ProfileID: "coding",
		WorkerID: "worker", RunID: "run", RunEpoch: 1,
		HardExpiresAt: time.Now().Add(time.Hour), RequiredSave: true,
	}
	createRunClaim(t, store, in)
	sandbox, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateForRun(
		ctx,
		sandbox.ID,
		"pod",
		in.RunID,
		in.WorkerID,
		in.RunEpoch,
		in.HardExpiresAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.release(ctx, sandbox.ID, SandboxStatusActive); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expiry snapshot closed renewed lease: %v", err)
	}
	if _, err := store.release(ctx, sandbox.ID, SandboxStatusProvisioning); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale provisioning snapshot closed activated sandbox: %v", err)
	}
	fresh, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil || fresh.Status != SandboxStatusActive {
		t.Fatalf("active sandbox changed: %+v %v", fresh, err)
	}
}

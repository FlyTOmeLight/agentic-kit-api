package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestValidateCheckpointID(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)
	if err := validateCheckpointID(valid); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"checkpoint",
		"sha256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("a", 63) + ":" + strings.Repeat("b", 64),
		"sha256:" + strings.Repeat("A", 64) + ":" + strings.Repeat("b", 64),
		"md5:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64),
	} {
		if err := validateCheckpointID(bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid checkpoint %q accepted: %v", bad, err)
		}
	}
}

func TestSaveRequiresStoppedBlockedSandbox(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		OwnerID: "owner", RequestKey: "save-guard", ProfileID: "coding",
		WorkerID: "worker", RunID: "run", RunEpoch: 1, RequiredSave: true,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sandbox, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.recordPhysicalIdentity(ctx, sandbox.ID, "pod-1", "pvc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateForRun(
		ctx,
		sandbox.ID,
		"pod-1",
		in.RunID,
		in.WorkerID,
		in.RunEpoch,
		in.HardExpiresAt,
	); err != nil {
		t.Fatal(err)
	}
	service := NewService(
		store,
		nil,
	).WithCheckpoints(CheckpointConfig{Image: "saver:test", StoreHostPath: "/tmp/checkpoints"})
	active, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Save(ctx, *active); !errors.Is(err, ErrConflict) {
		t.Fatalf("save on active sandbox: %v", err)
	}
	blocked, err := store.release(ctx, sandbox.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != SandboxStatusSaveBlocked {
		t.Fatalf("not blocked: %+v", blocked)
	}
	if err := service.Save(ctx, *blocked); !errors.Is(err, ErrNotReady) {
		t.Fatalf("save before stop must wait: %v", err)
	}
	if err := store.confirmStopped(ctx, *blocked); err != nil {
		t.Fatal(err)
	}
	stopped, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil || stopped.StoppedAt == nil {
		t.Fatalf("not stopped: %+v %v", stopped, err)
	}
}

func TestConfirmRequiredSaveAcceptsLayerfsCheckpoint(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	in := AcquireInput{
		OwnerID: "owner", RequestKey: "save-confirm", ProfileID: "coding",
		WorkerID: "worker", RunID: "run", RunEpoch: 1, RequiredSave: true,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sandbox, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.recordPhysicalIdentity(ctx, sandbox.ID, "pod-1", "pvc-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateForRun(
		ctx,
		sandbox.ID,
		"pod-1",
		in.RunID,
		in.WorkerID,
		in.RunEpoch,
		in.HardExpiresAt,
	); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.release(ctx, sandbox.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.confirmStopped(ctx, *blocked); err != nil {
		t.Fatal(err)
	}
	stopped, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := fmt.Sprintf("sha256:%064x:%064x", 1, 2)
	if err := store.confirmRequiredSave(
		ctx,
		stopped.ID,
		stopped.Generation,
		stopped.Version,
		checkpoint,
	); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil || saved.Status != SandboxStatusReleasing || saved.SavedCheckpoint != checkpoint {
		t.Fatalf("checkpoint not confirmed: %+v %v", saved, err)
	}
}

func TestSaveWaitsForConcurrentSaver(t *testing.T) {
	store := testStore(t)
	now := time.Now()
	sandbox := Sandbox{
		ID: "concurrent-save", OwnerID: "owner", RequestKey: "concurrent-save",
		Status: SandboxStatusSaveBlocked, RequiredSave: true, StoppedAt: &now, Generation: 1,
	}
	if err := store.db.Create(&sandbox).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(store, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
			"sandbox-save:"+sandbox.ID).Error; err != nil {
			return err
		}
		if err := service.Save(ctx, sandbox); !errors.Is(err, ErrNotReady) {
			t.Fatalf("concurrent saver was not deferred: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

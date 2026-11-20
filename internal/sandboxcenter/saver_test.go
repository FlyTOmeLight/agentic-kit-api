package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

func TestCheckpointConfigRequiresExactlyOneStore(t *testing.T) {
	for _, cfg := range []CheckpointConfig{
		{Image: "saver:test", StoreHostPath: "/checkpoints"},
		{Image: "saver:test", StorePVC: "checkpoint-store"},
	} {
		if err := cfg.valid(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cfg := range []CheckpointConfig{
		{Image: "saver:test"},
		{Image: "saver:test", StoreHostPath: "/checkpoints", StorePVC: "checkpoint-store"},
		{StorePVC: "checkpoint-store"},
	} {
		if err := cfg.valid(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid checkpoint config accepted: %+v: %v", cfg, err)
		}
	}
}

func TestCheckpointManifestUsesConfiguredStore(t *testing.T) {
	sandbox := Sandbox{ID: "sandbox", Generation: 2}
	for _, test := range []struct {
		cfg  CheckpointConfig
		want map[string]any
	}{
		{
			cfg: CheckpointConfig{Image: "saver:test", StoreHostPath: "/checkpoints"},
			want: map[string]any{"name": "checkpoints", "hostPath": map[string]any{
				"path": "/checkpoints", "type": "DirectoryOrCreate",
			}},
		},
		{
			cfg: CheckpointConfig{Image: "saver:test", StorePVC: "checkpoint-store"},
			want: map[string]any{"name": "checkpoints", "persistentVolumeClaim": map[string]any{
				"claimName": "checkpoint-store",
			}},
		},
	} {
		spec := saverPodManifest(sandbox, test.cfg)["spec"].(map[string]any)
		volume := spec["volumes"].([]any)[1]
		if !reflect.DeepEqual(volume, test.want) {
			t.Fatalf("checkpoint volume mismatch: got %+v, want %+v", volume, test.want)
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

func TestCheckpointRetryRollsAttempt(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now()
	sandbox := Sandbox{
		ID: "checkpoint-retry", OwnerID: "owner", RequestKey: "checkpoint-retry",
		Status: SandboxStatusSaveBlocked, RequiredSave: true, StoppedAt: &now,
		Generation: 1, CheckpointAttempt: "attempt-1",
	}
	if err := store.db.Create(&sandbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.rotateCheckpointAttempt(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil || current.CheckpointAttempt == sandbox.CheckpointAttempt ||
		current.CheckpointRetryCount != 1 {
		t.Fatalf("attempt was not rolled: %+v %v", current, err)
	}
	if err := store.rotateCheckpointAttempt(ctx, sandbox); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale attempt rotation: %v", err)
	}
	if err := store.rotateCheckpointAttempt(ctx, *current); err != nil {
		t.Fatal(err)
	}
	current, err = store.GetSandbox(ctx, sandbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.rotateCheckpointAttempt(ctx, *current); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry limit was not enforced: %v", err)
	}
	if current.Status != SandboxStatusSaveBlocked || current.SavedCheckpoint != "" {
		t.Fatalf("retry published an unverified attempt: %+v", current)
	}
}

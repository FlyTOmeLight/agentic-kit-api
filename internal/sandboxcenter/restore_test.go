package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func TestAcquireRestoreSource(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	checkpoint := fmt.Sprintf("sha256:%064x:%064x", 1, 2)
	stopped := time.Now()
	for _, source := range []Sandbox{
		{ID: "saved", OwnerID: "owner", TenantID: "tenant", RequestKey: "saved", Status: SandboxStatusDestroyed, SavedCheckpoint: checkpoint},
		{ID: "same-content", OwnerID: "owner", TenantID: "tenant", RequestKey: "same-content", Status: SandboxStatusDestroyed, SavedCheckpoint: checkpoint},
		{ID: "other-owner", OwnerID: "someone", TenantID: "tenant", RequestKey: "other-owner", Status: SandboxStatusDestroyed, SavedCheckpoint: checkpoint},
		{ID: "other-tenant", OwnerID: "owner", TenantID: "other", RequestKey: "other-tenant", Status: SandboxStatusDestroyed, SavedCheckpoint: checkpoint},
		{ID: "unsaved", OwnerID: "owner", TenantID: "tenant", RequestKey: "unsaved", Status: SandboxStatusSaveBlocked},
		{ID: "invalid", OwnerID: "owner", TenantID: "tenant", RequestKey: "invalid", Status: SandboxStatusDestroyed, SavedCheckpoint: "invalid"},
	} {
		source.RequiredSave = true
		source.StoppedAt = &stopped
		source.PodUID, source.WorkspaceUID = "pod", "pvc"
		source.ProfileID = "coding"
		if err := store.db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
	}
	input := AcquireInput{
		TenantID: "tenant", OwnerID: "owner", WorkerID: "worker", RunID: "run", RunEpoch: 1,
		RequestKey: "restore", ProfileID: "coding", RequiredSave: true,
		HardExpiresAt: time.Now().Add(time.Hour), RestoreFromSandboxID: "saved",
	}
	createRunClaim(t, store, input)
	for _, test := range []struct {
		source string
		save   bool
		want   error
	}{
		{source: "saved", save: true, want: nil},
		{source: "missing", save: true, want: ErrNotFound},
		{source: "other-owner", save: true, want: ErrNotFound},
		{source: "other-tenant", save: true, want: ErrNotFound},
		{source: "unsaved", save: true, want: ErrConflict},
		{source: "invalid", save: true, want: ErrConflict},
		{source: "saved", save: false, want: ErrInvalidInput},
	} {
		t.Run(fmt.Sprintf("%s/save=%t", test.source, test.save), func(t *testing.T) {
			request := input
			request.RequestKey = fmt.Sprintf("case-%s-%t", test.source, test.save)
			request.RestoreFromSandboxID = test.source
			request.RequiredSave = test.save
			freezeRestoreBinding(t, store, request, &execution.FilesystemBinding{
				Provider:             "Sandbox",
				Workspace:            "/workspace",
				RestoreFromSandboxID: request.RestoreFromSandboxID,
			})
			sandbox, _, err := store.Acquire(ctx, request)
			if !errors.Is(err, test.want) {
				t.Fatalf("acquire: %v; want %v", err, test.want)
			}
			if err == nil && sandbox.ResumeCheckpoint != checkpoint {
				t.Fatalf("checkpoint not frozen: %+v", sandbox)
			}
		})
	}
	freezeRestoreBinding(t, store, input, &execution.FilesystemBinding{
		Provider: "Sandbox", Workspace: "/workspace", RestoreFromSandboxID: input.RestoreFromSandboxID,
	})
	first, _, err := store.Acquire(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, _, err := store.Acquire(ctx, input)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("restore replay: %+v %v", replay, err)
	}
	input.RestoreFromSandboxID = "same-content"
	freezeRestoreBinding(t, store, input, &execution.FilesystemBinding{
		Provider: "Sandbox", Workspace: "/workspace", RestoreFromSandboxID: input.RestoreFromSandboxID,
	})
	if _, _, err := store.Acquire(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changing source with identical bytes should conflict: %v", err)
	}
}

func TestPendingReconciliationIncludesStoppedSave(t *testing.T) {
	store := testStore(t)
	now := time.Now()
	sandbox := Sandbox{
		ID: "stopped-save", OwnerID: "owner", RequestKey: "save",
		Status: SandboxStatusSaveBlocked, RequiredSave: true, StoppedAt: &now,
	}
	if err := store.db.Create(&sandbox).Error; err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingReconciliation(context.Background(), time.Minute, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != sandbox.ID {
		t.Fatalf("stopped workspace must remain scheduled for save: %+v %v", pending, err)
	}
}

func TestSaveRejectsIncompleteRestore(t *testing.T) {
	now := time.Now()
	service := NewService(nil, nil)
	err := service.Save(context.Background(), Sandbox{
		Status: SandboxStatusSaveBlocked, RequiredSave: true, StoppedAt: &now,
		ResumeCheckpoint: fmt.Sprintf("sha256:%064x:%064x", 1, 2),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("incomplete restore cannot publish a checkpoint: %v", err)
	}
}

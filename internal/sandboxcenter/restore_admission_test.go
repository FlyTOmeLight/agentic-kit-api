package sandboxcenter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func restoreSource(t *testing.T, store *Store) Sandbox {
	t.Helper()
	stopped := time.Now().UTC()
	source := Sandbox{
		ID: "saved-source", TenantID: "tenant", OwnerID: "owner", RequestKey: "source",
		ProfileID: "coding", ProfileRevision: 1, RequiredSave: true,
		Status: SandboxStatusDestroyed, StoppedAt: &stopped, DestroyedAt: &stopped,
		PodUID: "old-pod", WorkspaceUID: "old-pvc",
		SavedCheckpoint: "sha256:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64),
	}
	if err := store.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	return source
}

func freezeRestoreBinding(t *testing.T, store *Store, in AcquireInput, binding *execution.FilesystemBinding) {
	t.Helper()
	spec, err := json.Marshal(map[string]any{"filesystem": binding})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Exec(
		"UPDATE durable_run SET spec = ?::jsonb WHERE id = ?",
		string(spec),
		in.RunID,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func restoreInput(source Sandbox) AcquireInput {
	return AcquireInput{
		TenantID: source.TenantID, OwnerID: source.OwnerID, RequestKey: "restore",
		ProfileID: source.ProfileID, ProfileRevision: source.ProfileRevision,
		WorkerID: "worker", RunID: "next-run", RunEpoch: 1,
		HardExpiresAt: time.Now().Add(time.Hour), RequiredSave: true, RestoreFromSandboxID: source.ID,
	}
}

func TestAcquireRestoreSourceValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Sandbox)
		want   error
	}{
		{name: "valid", change: func(*Sandbox) {}, want: nil},
		{name: "tenant", change: func(source *Sandbox) { source.TenantID = "other" }, want: ErrNotFound},
		{name: "owner", change: func(source *Sandbox) { source.OwnerID = "other" }, want: ErrNotFound},
		{name: "profile", change: func(source *Sandbox) { source.ProfileID = "other" }, want: ErrConflict},
		{name: "revision", change: func(source *Sandbox) { source.ProfileRevision++ }, want: ErrConflict},
		{
			name:   "releasing",
			change: func(source *Sandbox) { source.Status = SandboxStatusReleasing },
			want:   ErrConflict,
		},
		{
			name:   "active",
			change: func(source *Sandbox) { source.Status = SandboxStatusActive },
			want:   ErrConflict,
		},
		{name: "lost", change: func(source *Sandbox) { source.Status = SandboxStatusLost }, want: ErrConflict},
		{name: "not-stopped", change: func(source *Sandbox) { source.StoppedAt = nil }, want: ErrConflict},
		{name: "no-save", change: func(source *Sandbox) { source.RequiredSave = false }, want: ErrConflict},
		{
			name:   "no-checkpoint",
			change: func(source *Sandbox) { source.SavedCheckpoint = "" },
			want:   ErrConflict,
		},
		{
			name:   "bad-checkpoint",
			change: func(source *Sandbox) { source.SavedCheckpoint = "../checkpoint" },
			want:   ErrConflict,
		},
		{name: "no-pod-identity", change: func(source *Sandbox) { source.PodUID = "" }, want: ErrConflict},
		{
			name:   "no-volume-identity",
			change: func(source *Sandbox) { source.WorkspaceUID = "" },
			want:   ErrConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			source := restoreSource(t, store)
			input := restoreInput(source)
			createRunClaim(t, store, input)
			freezeRestoreBinding(t, store, input, &execution.FilesystemBinding{
				Provider:             "Sandbox",
				Workspace:            "/workspace",
				ProfileID:            "coding",
				RestoreFromSandboxID: source.ID,
			})
			test.change(&source)
			if err := store.db.Save(&source).Error; err != nil {
				t.Fatal(err)
			}
			sandbox, _, err := store.Acquire(context.Background(), input)
			if !errors.Is(err, test.want) {
				t.Fatalf("acquire = %v, want %v", err, test.want)
			}
			if err == nil &&
				(sandbox.ResumeCheckpoint != source.SavedCheckpoint || sandbox.RestoreFromSandboxID != source.ID) {
				t.Fatalf("restore identity not frozen: %+v", sandbox)
			}
		})
	}
}

func TestAcquireRestoreAuthorizationAndReplay(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	source := restoreSource(t, store)
	input := restoreInput(source)
	createRunClaim(t, store, input)
	binding := execution.FilesystemBinding{
		Provider: "Sandbox", Workspace: "/workspace", ProfileID: "coding", RestoreFromSandboxID: source.ID,
	}
	for _, change := range []func(*execution.FilesystemBinding){
		func(binding *execution.FilesystemBinding) { binding.RestoreFromSandboxID = "" },
		func(binding *execution.FilesystemBinding) { binding.RestoreFromSandboxID = "other" },
		func(binding *execution.FilesystemBinding) { binding.Workspace = "/workspace/../../etc" },
		func(binding *execution.FilesystemBinding) { binding.Workspace = "/workspace-other" },
		func(binding *execution.FilesystemBinding) { binding.Provider = "AIO" },
		func(binding *execution.FilesystemBinding) { binding.ProfileID = "other" },
	} {
		invalid := binding
		change(&invalid)
		freezeRestoreBinding(t, store, input, &invalid)
		if _, _, err := store.Acquire(ctx, input); !errors.Is(err, ErrConflict) {
			t.Fatalf("invalid frozen binding accepted: %+v, %v", invalid, err)
		}
	}
	freezeRestoreBinding(t, store, input, nil)
	if _, _, err := store.Acquire(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing frozen binding accepted: %v", err)
	}
	freezeRestoreBinding(t, store, input, &binding)
	omitted := input
	omitted.RestoreFromSandboxID = ""
	if _, _, err := store.Acquire(ctx, omitted); !errors.Is(err, ErrConflict) {
		t.Fatalf("frozen restore source omitted: %v", err)
	}
	binding.Workspace = "/workspace/./project"
	freezeRestoreBinding(t, store, input, &binding)
	sandbox, _, err := store.Acquire(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, _, err := store.Acquire(ctx, input)
	if err != nil || replay.ID != sandbox.ID || replay.ResumeCheckpoint != sandbox.ResumeCheckpoint {
		t.Fatalf("replay failed: %+v %v", replay, err)
	}
	changed := input
	changed.RequiredSave = false
	if _, _, err := store.Acquire(ctx, changed); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("restore without required save accepted: %v", err)
	}
	changed = input
	changed.RestoreFromSandboxID = "another-source"
	binding.RestoreFromSandboxID = changed.RestoreFromSandboxID
	freezeRestoreBinding(t, store, input, &binding)
	if _, _, err := store.Acquire(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay changed source accepted: %v", err)
	}
	if _, _, err := store.Acquire(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay bypassed frozen filesystem: %v", err)
	}
	if err := store.db.Exec(
		"UPDATE durable_run SET epoch = epoch + 1 WHERE id = ?",
		input.RunID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Acquire(ctx, input); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("stale claim probed source: %v", err)
	}
}

func TestRestoreEnvironmentReconciliation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	source := restoreSource(t, store)
	for _, status := range []string{envStatusPreparing, envStatusActive, envStatusReleased} {
		env := table.RunEnvironment{
			ID: status, RunID: "run", RequestKey: status, Status: status,
			SandboxID: source.ID, ProfileID: "coding", ProfileRevision: 1, RunEpoch: 1,
		}
		if err := store.db.Create(&env).Error; err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.PendingReconciliation(ctx, time.Minute, 100)
	if err != nil || len(pending) != 1 {
		t.Fatalf("destroyed sandbox with unfinished environments not retried: %+v %v", pending, err)
	}
	reconciler := NewReconciler(store, nil, ReconcilerConfig{}, nil)
	reconciler.Sweep(ctx)
	reconciler.Sweep(ctx)
	var environments []table.RunEnvironment
	if err := store.db.Find(&environments).Error; err != nil {
		t.Fatal(err)
	}
	for _, env := range environments {
		if env.Status != envStatusReleased || env.SavedCheckpoint != source.SavedCheckpoint ||
			env.ReleasedAt == nil {
			t.Fatalf("trusted checkpoint not backfilled: %+v", env)
		}
	}
	pending, err = store.PendingReconciliation(ctx, time.Minute, 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("closed sandbox remains pending: %+v %v", pending, err)
	}
	for _, status := range []string{SandboxStatusSaveBlocked, SandboxStatusReleasing} {
		if err := store.db.Model(&Sandbox{}).
			Where("id = ?", source.ID).
			Update("status", status).
			Error; err != nil {
			t.Fatal(err)
		}
		if err := store.CloseEnvironmentsForSandbox(ctx, source.ID, true); !errors.Is(err, ErrConflict) {
			t.Fatalf("closed non-destroyed sandbox: %s %v", status, err)
		}
	}
}

func TestRestorePodInitialization(t *testing.T) {
	provider, err := NewProvider(K8sConfig{
		Context: "isolated", Namespace: "sandbox", GuardImage: "guard:test",
		Checkpoints: CheckpointConfig{Image: "saver:test", StoreHostPath: "/checkpoints"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sandbox := Sandbox{
		ID: "restore", RequiredSave: true, RestoreFromSandboxID: "source",
		ResumeCheckpoint: "sha256:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64),
	}
	spec := provider.podManifest(sandbox, admissionRequest{})["spec"].(map[string]any)
	initializers := spec["initContainers"].([]any)
	if len(initializers) != 2 || initializers[0].(map[string]any)["name"] != "restore" {
		t.Fatalf("restore not first initializer: %+v", initializers)
	}
	restore := initializers[0].(map[string]any)
	if restore["image"] != "saver:test" || restore["command"].([]string)[4] != sandbox.ResumeCheckpoint {
		t.Fatalf("restore does not use frozen checkpoint: %+v", restore)
	}
	guard := spec["containers"].([]any)[0].(map[string]any)
	mount := guard["volumeMounts"].([]any)[1].(map[string]any)
	if mount["mountPath"] != "/workspace" || mount["subPath"] != "workspace" {
		t.Fatalf("guard mount exposes PVC root: %+v", mount)
	}
	saver := saverPodManifest(sandbox, provider.cfg.Checkpoints)["spec"].(map[string]any)
	mount = saver["containers"].([]any)[0].(map[string]any)["volumeMounts"].([]any)[0].(map[string]any)
	if mount["subPath"] != "workspace" {
		t.Fatalf("saver did not save restored subtree: %+v", mount)
	}
	plain := provider.podManifest(Sandbox{ID: "plain"}, admissionRequest{})["spec"].(map[string]any)
	if len(plain["initContainers"].([]any)) != 1 {
		t.Fatal("normal acquire unexpectedly restores a checkpoint")
	}
	sandbox.ResumeCheckpoint = "../bad"
	if _, err := provider.Provision(
		context.Background(),
		sandbox,
		admissionRequest{},
		provider.cfg.Checkpoints,
	); !errors.Is(
		err,
		ErrInvalidInput,
	) {
		t.Fatalf("malformed frozen checkpoint reached provider: %v", err)
	}
}

package sandboxcenter

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func TestRequiredSaveLayerfsE2E(t *testing.T) {
	cluster := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if cluster == "" {
		t.Skip("SANDBOX_E2E_K8S_CONTEXT required")
	}
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:saveblocked"
	}
	checkpoints := CheckpointConfig{
		Image: os.Getenv(
			"SANDBOX_E2E_SAVER_IMAGE",
		),
		StoreHostPath: os.Getenv("SANDBOX_E2E_CHECKPOINT_HOST_PATH"),
	}
	if checkpoints.Image == "" {
		checkpoints.Image = "agentic-sandbox-saver:v1"
	}
	if checkpoints.StoreHostPath == "" {
		checkpoints.StoreHostPath = "/private/tmp/sandbox-center-checkpoints"
	}
	store := testStore(t)
	provider, err := NewProvider(K8sConfig{
		Context: cluster, Namespace: "sandbox-center", GuardImage: image, MaxLifetime: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, provider).WithCheckpoints(checkpoints)
	reconciler := NewReconciler(store, provider, ReconcilerConfig{}, nil).WithCheckpoints(service)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	input := AcquireInput{
		OwnerID: "save-owner", RequestKey: "save-layerfs-e2e", ProfileID: "coding",
		WorkerID: "worker", RunID: "save-run", RunEpoch: 1, RequiredSave: true,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	acquire := func(input AcquireInput) *Sandbox {
		t.Helper()
		sandbox, _, err := store.Acquire(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			for _, pod := range []string{saverPodPrefix + sandbox.ID, "digest-" + sandbox.ID, "sandbox-" + sandbox.ID} {
				if _, err := provider.kubectl(cleanup, nil, "delete", "pod", pod,
					"--ignore-not-found", "--wait", "--timeout=30s"); err != nil {
					t.Error(err)
				}
			}
			if _, err := provider.kubectl(cleanup, nil, "delete", "pvc", "workspace-"+sandbox.ID,
				"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
				t.Error(err)
			}
		})
		if err := service.provision(ctx, sandbox, acquireRequest{
			TTLSeconds: 600, WorkerID: input.WorkerID, RunID: input.RunID,
			RunEpoch: input.RunEpoch, RequestKey: input.RequestKey,
			RestoreFromSandboxID: input.RestoreFromSandboxID,
		}); err != nil {
			t.Fatal(err)
		}
		return sandbox
	}
	createRunClaim(t, store, input)
	source := acquire(input)
	script := `set -eu
mkdir -p /workspace/project/src /workspace/project/docs /workspace/project/empty
echo 'package main' > /workspace/project/src/main.go
printf 'int main(void){return 0;}' > /workspace/project/src/main.c
echo '# Design' > /workspace/project/docs/design.md
chmod 0755 /workspace/project/src/main.go
chmod 0700 /workspace/project/empty
ln -s src/main.go /workspace/project/entry.go
ln /workspace/project/src/main.go /workspace/project/hardlink
`
	if operation, err := service.ExecCommand(ctx, source.ID, "seed", script); err != nil ||
		operation == nil || operation.Status != OpStatusSucceeded {
		t.Fatalf("seed workspace: %+v %v", operation, err)
	}
	before := workspaceTreeDigest(t, ctx, provider, *source, checkpoints)
	if _, err := service.Release(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	var saved *Sandbox
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		reconciler.Sweep(ctx)
		current, err := store.GetSandbox(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == SandboxStatusDestroyed {
			saved = current
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	if saved == nil {
		t.Fatal("save loop did not converge")
	}
	out, err := provider.kubectl(ctx, nil, "get", "pvc", "workspace-"+source.ID, "--ignore-not-found", "-o", "name")
	if err != nil || len(out) != 0 {
		t.Fatalf("saved workspace not reclaimed: %s %v", out, err)
	}
	input.RunID, input.RequestKey = "restored-run", "restore-layerfs-e2e"
	input.RestoreFromSandboxID = source.ID
	createRunClaim(t, store, input)
	freezeRestoreBinding(t, store, input, &execution.FilesystemBinding{
		Provider:             "Sandbox",
		Workspace:            "/workspace",
		ProfileID:            input.ProfileID,
		RestoreFromSandboxID: source.ID,
	})
	restored := acquire(input)
	current, err := store.GetSandbox(ctx, restored.ID)
	if err != nil || current.Status != SandboxStatusActive || current.ResumeCheckpoint != saved.SavedCheckpoint ||
		current.RestoreFromSandboxID != source.ID {
		t.Fatalf("restored sandbox not active with frozen source: %+v %v", current, err)
	}
	after := workspaceTreeDigest(t, ctx, provider, *current, checkpoints)
	if before != after {
		t.Fatalf("restored tree digest differs: before=%s after=%s", before, after)
	}
	t.Logf("cross-run Acquire restored tree digest %s", after)
}

func workspaceTreeDigest(
	t *testing.T,
	ctx context.Context,
	provider *Provider,
	sandbox Sandbox,
	cfg CheckpointConfig,
) string {
	t.Helper()
	name := "digest-" + sandbox.ID
	pod := saverPodManifest(sandbox, cfg)
	pod["metadata"].(map[string]any)["name"] = name
	spec := pod["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)
	container["command"] = []string{
		"python3",
		"-B",
		"-c",
		"import sys; sys.path.insert(0, '/usr/local/bin'); import layerfs_checkpoint as checkpoint; print(checkpoint.tree_digest('/workspace'))",
	}
	container["volumeMounts"] = container["volumeMounts"].([]any)[:1]
	spec["volumes"] = spec["volumes"].([]any)[:1]
	manifest, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.kubectl(ctx, manifest, "apply", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.kubectl(
		ctx,
		nil,
		"wait",
		"--for=jsonpath={.status.phase}=Succeeded",
		"pod/"+name,
		"--timeout=60s",
	); err != nil {
		logs, _ := provider.kubectl(ctx, nil, "logs", name)
		t.Fatalf("tree digest failed: %s %v", logs, err)
	}
	out, err := provider.kubectl(ctx, nil, "logs", name)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.TrimSpace(string(out))
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("invalid tree digest: %q", digest)
	}
	uid, err := provider.observeIdentity(ctx, sandbox.ID, "pods", name, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.destroyObject(ctx, "pods", name, uid); err != nil {
		t.Fatal(err)
	}
	return digest
}

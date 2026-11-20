package sandboxcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// TestReconcileFailurePathsE2E drives the negative reconciliation branches on
// a real cluster: a failed saver pod must be torn down so the next sweep
// re-saves, and a restore stuck in provisioning past the grace window must
// lose its pod and PVC without ever serving the half-restored workspace.
func TestReconcileFailurePathsE2E(t *testing.T) {
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
		StorePVC:      os.Getenv("SANDBOX_E2E_CHECKPOINT_PVC"),
	}
	if checkpoints.Image == "" {
		checkpoints.Image = "agentic-sandbox-saver:v1"
	}
	if checkpoints.StoreHostPath == "" && checkpoints.StorePVC == "" {
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
	reconciler := NewReconciler(store, provider, ReconcilerConfig{ProvisioningGrace: time.Millisecond}, nil).
		WithCheckpoints(service)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	cleanupSandbox := func(id string) {
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		for _, pod := range []string{saverPodPrefix + id, "verify-" + id, "sandbox-" + id} {
			if _, err := provider.kubectl(cleanup, nil, "delete", "pod", pod,
				"--ignore-not-found", "--wait", "--timeout=30s"); err != nil {
				t.Error(err)
			}
		}
		if _, err := provider.kubectl(cleanup, nil, "delete", "pvc", "workspace-"+id,
			"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
			t.Error(err)
		}
	}

	t.Run("failed saver is destroyed and the save is retried", func(t *testing.T) {
		input := AcquireInput{
			OwnerID: "fail-owner", RequestKey: "failed-saver-e2e", ProfileID: "coding",
			WorkerID: "worker", RunID: "failed-saver-run", RunEpoch: 1, RequiredSave: true,
			HardExpiresAt: time.Now().Add(time.Hour),
		}
		createRunClaim(t, store, input)
		sandbox, _, err := store.Acquire(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupSandbox(sandbox.ID) })
		if err := service.provision(ctx, sandbox, acquireRequest{
			TTLSeconds: 600, WorkerID: input.WorkerID, RunID: input.RunID,
			RunEpoch: input.RunEpoch, RequestKey: input.RequestKey,
		}); err != nil {
			t.Fatal(err)
		}
		if operation, err := service.ExecCommand(ctx, sandbox.ID, "seed-failed-saver",
			"echo still-here > /workspace/marker.txt"); err != nil ||
			operation == nil || operation.Status != OpStatusSucceeded {
			t.Fatalf("seed workspace: %+v %v", operation, err)
		}
		released, err := service.Release(ctx, sandbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		if released.Status != SandboxStatusSaveBlocked || released.StoppedAt == nil {
			t.Fatalf("release must stop the save-blocked sandbox: %+v", released)
		}

		// A workspace permission fault cannot stop the real saver: it runs as
		// root with DAC_OVERRIDE so it can read a mode-000 directory. Inject a
		// failing saver pod with the exact identity Save validates instead.
		if checkpoints.StorePVC != "" {
			if err := service.Save(ctx, *released); err != ErrNotReady {
				t.Fatalf("register save attempt: %v", err)
			}
			released, err = store.GetSandbox(ctx, sandbox.ID)
			if err != nil || released.CheckpointAttempt == "" {
				t.Fatalf("save attempt not recorded: %+v %v", released, err)
			}
		}
		failedSaver := saverPodManifest(*released, checkpoints)
		container := failedSaver["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		container["command"] = []string{"python3", "-c", "import sys; sys.exit(42)"}
		if checkpoints.StorePVC != "" {
			container["command"] = []string{"python3", "-c", `
import pathlib, sys
pathlib.Path("/checkpoints", sys.argv[1]).mkdir(parents=True)
sys.exit(42)
`}
		}
		failedSaverManifest, err := json.Marshal(failedSaver)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, failedSaverManifest, "apply", "-f", "-"); err != nil {
			t.Fatal(err)
		}

		waitForCondition(t, ctx, "failed saver pod", func() bool {
			out, err := provider.kubectl(ctx, nil, "get", "pod", saverPodPrefix+sandbox.ID,
				"-o", "jsonpath={.status.phase}", "--ignore-not-found")
			return err == nil && strings.TrimSpace(string(out)) == "Failed"
		})
		// One sweep must observe the failed saver and keep it for diagnosis:
		// no checkpoint may be published and the pod must not be replaced.
		reconciler.Sweep(ctx)
		current, err := store.GetSandbox(ctx, sandbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != SandboxStatusSaveBlocked || current.SavedCheckpoint != "" {
			t.Fatalf("failed save must keep the sandbox blocked without checkpoint: %+v", current)
		}
		failedPhase, err := provider.kubectl(ctx, nil, "get", "pod", saverPodPrefix+sandbox.ID,
			"-o", "jsonpath={.status.phase}", "--ignore-not-found")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(failedPhase)) != "Failed" {
			t.Fatalf("failed saver pod must be retained for diagnosis, got phase %q", failedPhase)
		}

		if checkpoints.StorePVC == "" {
			failedSaverUID, err := provider.observeIdentity(ctx, sandbox.ID, "pods",
				saverPodPrefix+sandbox.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.destroyObject(ctx, "pods", saverPodPrefix+sandbox.ID,
				failedSaverUID); err != nil {
				t.Fatal(err)
			}
		}

		waitForCondition(t, ctx, "save retry destroys the sandbox", func() bool {
			reconciler.Sweep(ctx)
			current, err := store.GetSandbox(ctx, sandbox.ID)
			return err == nil && current.Status == SandboxStatusDestroyed && current.SavedCheckpoint != ""
		})
		if checkpoints.StorePVC != "" {
			current, err := store.GetSandbox(ctx, sandbox.ID)
			if err != nil || current.CheckpointRetryCount != 1 ||
				current.CheckpointAttempt == released.CheckpointAttempt {
				t.Fatalf("incomplete save was not retried in a new directory: %+v %v", current, err)
			}
		}
		for _, resource := range []string{
			"pod/" + saverPodPrefix + sandbox.ID,
			"pod/verify-" + sandbox.ID,
			"pod/sandbox-" + sandbox.ID,
			"pvc/workspace-" + sandbox.ID,
		} {
			out, err := provider.kubectl(ctx, nil, "get", resource,
				"--ignore-not-found", "-o", "name")
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 0 {
				t.Errorf("saved sandbox resource %s was not reclaimed: %s", resource, out)
			}
		}
	})

	t.Run("restore stuck provisioning loses pod and pvc", func(t *testing.T) {
		sourceInput := AcquireInput{
			OwnerID: "fail-owner", RequestKey: "restore-source-e2e", ProfileID: "coding",
			WorkerID: "worker", RunID: "restore-source-run", RunEpoch: 1, RequiredSave: true,
			HardExpiresAt: time.Now().Add(time.Hour),
		}
		createRunClaim(t, store, sourceInput)
		source, _, err := store.Acquire(ctx, sourceInput)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupSandbox(source.ID) })
		if err := service.provision(ctx, source, acquireRequest{
			TTLSeconds: 600, WorkerID: sourceInput.WorkerID, RunID: sourceInput.RunID,
			RunEpoch: sourceInput.RunEpoch, RequestKey: sourceInput.RequestKey,
		}); err != nil {
			t.Fatal(err)
		}
		if operation, err := service.ExecCommand(ctx, source.ID, "seed-restore-source",
			"echo source-data > /workspace/data.txt"); err != nil ||
			operation == nil || operation.Status != OpStatusSucceeded {
			t.Fatalf("seed source workspace: %+v %v", operation, err)
		}
		if _, err := service.Release(ctx, source.ID); err != nil {
			t.Fatal(err)
		}
		var saved *Sandbox
		waitForCondition(t, ctx, "source saved and destroyed", func() bool {
			reconciler.Sweep(ctx)
			current, err := store.GetSandbox(ctx, source.ID)
			if err == nil && current.Status == SandboxStatusDestroyed {
				saved = current
				return true
			}
			return false
		})

		restore := sourceInput
		restore.RunID = "restore-stuck-run"
		restore.RequestKey = "restore-stuck-e2e"
		restore.RestoreFromSandboxID = source.ID
		createRunClaim(t, store, restore)
		freezeRestoreBinding(t, store, restore, &execution.FilesystemBinding{
			Provider: "Sandbox", Workspace: "/workspace", ProfileID: restore.ProfileID,
			RestoreFromSandboxID: source.ID,
		})
		restored, _, err := store.Acquire(ctx, restore)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupSandbox(restored.ID) })
		if restored.ResumeCheckpoint != saved.SavedCheckpoint {
			t.Fatalf("restore did not freeze the source checkpoint: %+v", restored)
		}
		// A stuck restore never activates: no lease fact commits and the row
		// keeps no physical identity. Push it past the provisioning grace
		// window and materialize the pod and PVC it would have provisioned so
		// the reconciler observes, abandons and reclaims both.
		if err := store.db.Model(&Sandbox{}).Where("id = ?", restored.ID).
			Update("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
		workspacePVC := map[string]any{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata": map[string]any{
				"name": "workspace-" + restored.ID,
				"labels": map[string]string{
					"app.kubernetes.io/managed-by": "sandbox-center",
					"sandbox-center/id":            restored.ID,
				},
			},
			"spec": map[string]any{
				"accessModes": []string{"ReadWriteOnce"},
				"resources": map[string]any{
					"requests": map[string]string{"storage": "1Gi"},
				},
			},
		}
		pvcManifest, err := json.Marshal(workspacePVC)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, pvcManifest, "apply", "-f", "-"); err != nil {
			t.Fatal(err)
		}
		pod := map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name": "sandbox-" + restored.ID,
				"labels": map[string]string{
					"app.kubernetes.io/managed-by": "sandbox-center",
					"sandbox-center/id":            restored.ID,
				},
			},
			"spec": map[string]any{
				"restartPolicy": "Never",
				"containers": []any{map[string]any{
					"name":    "guard",
					"image":   image,
					"command": []string{"sleep", "600"},
				}},
				"volumes": []any{
					map[string]any{
						"name": "workspace",
						"persistentVolumeClaim": map[string]any{
							"claimName": "workspace-" + restored.ID,
						},
					},
				},
			},
		}
		podManifest, err := json.Marshal(pod)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, podManifest, "apply", "-f", "-"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			for _, resource := range []string{"pod/sandbox-" + restored.ID, "pvc/workspace-" + restored.ID} {
				if _, err := provider.kubectl(cleanup, nil, "delete", resource,
					"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
					t.Error(err)
				}
			}
		})

		waitForCondition(t, ctx, "stuck restore marked lost", func() bool {
			reconciler.Sweep(ctx)
			current, err := store.GetSandbox(ctx, restored.ID)
			return err == nil && current.Status == SandboxStatusLost
		})
		for _, resource := range []string{"pod/sandbox-" + restored.ID, "pvc/workspace-" + restored.ID} {
			out, err := provider.kubectl(ctx, nil, "get", resource, "--ignore-not-found", "-o", "name")
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 0 {
				t.Errorf("stuck restore resource %s was not reclaimed: %s", resource, out)
			}
		}
		current, err := store.GetSandbox(ctx, restored.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.SavedCheckpoint != "" {
			t.Fatalf("abandoned restore must never publish a checkpoint: %+v", current)
		}
		origin, err := store.GetSandbox(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if origin.SavedCheckpoint != saved.SavedCheckpoint {
			t.Fatalf("abandoned restore must not touch the source checkpoint: %+v", origin)
		}
		if fmt.Sprint(current.Generation) != "0" {
			t.Errorf("abandoned restore generation must stay 0, got %d", current.Generation)
		}
	})
}

func waitForCondition(t *testing.T, ctx context.Context, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

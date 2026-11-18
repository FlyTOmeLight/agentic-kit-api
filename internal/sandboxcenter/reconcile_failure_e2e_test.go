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
	reconciler := NewReconciler(store, provider, ReconcilerConfig{ProvisioningGrace: time.Millisecond}, nil).
		WithCheckpoints(service)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	cleanupSandbox := func(id string) {
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		for _, pod := range []string{saverPodPrefix + id, "sandbox-" + id} {
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
		if operation, err := service.ExecCommand(ctx, sandbox.ID, "seed",
			"echo still-here > /workspace/marker.txt"); err != nil ||
			operation == nil || operation.Status != OpStatusSucceeded {
			t.Fatalf("seed workspace: %+v %v", operation, err)
		}
		if _, err := service.Release(ctx, sandbox.ID); err != nil {
			t.Fatal(err)
		}
		waitForCondition(t, ctx, "save blocked and stopped", func() bool {
			reconciler.Sweep(ctx)
			current, err := store.GetSandbox(ctx, sandbox.ID)
			return err == nil && current.Status == SandboxStatusSaveBlocked && current.StoppedAt != nil
		})

		// Replace the PVC with a directory the saver cannot read. The next
		// sweep recreates the saver on it and the pod must fail.
		fake, err := json.Marshal(map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":   "fake-" + sandbox.ID,
				"labels": map[string]string{"app.kubernetes.io/managed-by": "sandbox-center-e2e"},
			},
			"spec": map[string]any{
				"restartPolicy": "Never",
				"containers": []any{
					map[string]any{
						"name":  "busybox",
						"image": image,
						"command": []string{
							"python3",
							"-c",
							"import os; os.chmod('/workspace', 0o000)",
						},
						"securityContext": map[string]any{
							"runAsUser":                0,
							"allowPrivilegeEscalation": false,
						},
						"volumeMounts": []any{
							map[string]any{"name": "workspace", "mountPath": "/workspace"},
						},
					},
				},
				"volumes": []any{map[string]any{
					"name":                  "workspace",
					"persistentVolumeClaim": map[string]any{"claimName": "workspace-" + sandbox.ID},
				}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, fake, "apply", "-f", "-"); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, nil, "wait", "--for=jsonpath={.status.phase}=Succeeded",
			"pod/fake-"+sandbox.ID, "--timeout=60s"); err != nil {
			logs, _ := provider.kubectl(ctx, nil, "logs", "fake-"+sandbox.ID)
			t.Fatalf("chmod workspace failed: %s %v", logs, err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if _, err := provider.kubectl(cleanup, nil, "delete", "pod", "fake-"+sandbox.ID,
				"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
				t.Error(err)
			}
		})

		waitForCondition(t, ctx, "failed saver pod", func() bool {
			reconciler.Sweep(ctx)
			out, err := provider.kubectl(ctx, nil, "get", "pod", saverPodPrefix+sandbox.ID,
				"-o", "jsonpath={.status.phase}", "--ignore-not-found")
			return err == nil && strings.TrimSpace(string(out)) == "Failed"
		})
		current, err := store.GetSandbox(ctx, sandbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != SandboxStatusSaveBlocked || current.SavedCheckpoint != "" {
			t.Fatalf("failed save must keep the sandbox blocked without checkpoint: %+v", current)
		}

		reconciler.Sweep(ctx)
		out, err := provider.kubectl(ctx, nil, "get", "pod", saverPodPrefix+sandbox.ID,
			"--ignore-not-found", "-o", "name")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatalf("failed saver pod was not destroyed on the next sweep: %s", out)
		}

		// Restore read access so the retried save succeeds end to end.
		fix, err := json.Marshal(map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":   "fix-" + sandbox.ID,
				"labels": map[string]string{"app.kubernetes.io/managed-by": "sandbox-center-e2e"},
			},
			"spec": map[string]any{
				"restartPolicy": "Never",
				"containers": []any{
					map[string]any{
						"name":  "busybox",
						"image": image,
						"command": []string{
							"python3",
							"-c",
							"import os; os.chmod('/workspace', 0o750); os.chown('/workspace', 65534, 65534)",
						},
						"securityContext": map[string]any{
							"runAsUser":                0,
							"allowPrivilegeEscalation": false,
						},
						"volumeMounts": []any{
							map[string]any{"name": "workspace", "mountPath": "/workspace"},
						},
					},
				},
				"volumes": []any{map[string]any{
					"name":                  "workspace",
					"persistentVolumeClaim": map[string]any{"claimName": "workspace-" + sandbox.ID},
				}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, fix, "apply", "-f", "-"); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.kubectl(ctx, nil, "wait", "--for=jsonpath={.status.phase}=Succeeded",
			"pod/fix-"+sandbox.ID, "--timeout=60s"); err != nil {
			logs, _ := provider.kubectl(ctx, nil, "logs", "fix-"+sandbox.ID)
			t.Fatalf("restore workspace access failed: %s %v", logs, err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if _, err := provider.kubectl(cleanup, nil, "delete", "pod", "fix-"+sandbox.ID,
				"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
				t.Error(err)
			}
		})

		waitForCondition(t, ctx, "save retry destroys the sandbox", func() bool {
			reconciler.Sweep(ctx)
			current, err := store.GetSandbox(ctx, sandbox.ID)
			return err == nil && current.Status == SandboxStatusDestroyed && current.SavedCheckpoint != ""
		})
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
		if operation, err := service.ExecCommand(ctx, source.ID, "seed",
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
		// Record identity without ever activating: the pod is mid-restore when
		// the worker disappears, so the lease fact never commits.
		if err := store.recordPhysicalIdentity(ctx, restored.ID, "pod-stuck", "pvc-stuck"); err != nil {
			t.Fatal(err)
		}
		if err := store.db.Model(&Sandbox{}).Where("id = ?", restored.ID).
			Update("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
		pod := map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":   "sandbox-" + restored.ID,
				"labels": map[string]string{"app.kubernetes.io/managed-by": "sandbox-center-e2e"},
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
			if _, err := provider.kubectl(cleanup, nil, "delete", "pod", "sandbox-"+restored.ID,
				"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
				t.Error(err)
			}
		})
		// recordPhysicalIdentity stored a placeholder; observe the real pod UID
		// so the reconciler deletes this exact pod.
		podUID, err := provider.observeIdentity(ctx, restored.ID, "pods", "sandbox-"+restored.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		pvcUID, err := provider.observeIdentity(ctx, restored.ID, "persistentvolumeclaims",
			"workspace-"+restored.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.recordPhysicalIdentity(ctx, restored.ID, podUID, pvcUID); err != nil {
			t.Fatal(err)
		}

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

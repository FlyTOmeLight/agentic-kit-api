package sandboxcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRequiredSaveRetentionE2E(t *testing.T) {
	cluster := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if cluster == "" {
		t.Skip("SANDBOX_E2E_K8S_CONTEXT required")
	}
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:saveblocked"
	}
	for _, expiry := range []bool{false, true} {
		t.Run(fmt.Sprintf("expiry=%t", expiry), func(t *testing.T) {
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
			service := NewService(store, provider)
			reconciler := NewReconciler(store, provider, ReconcilerConfig{}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			in := AcquireInput{
				OwnerID: "save-owner", RequestKey: "save-e2e", ProfileID: "coding",
				WorkerID: "worker", RunID: "save-run", RunEpoch: 1, RequiredSave: true,
				HardExpiresAt: time.Now().Add(time.Hour),
			}
			createRunClaim(t, store, in)
			sandbox, _, err := store.Acquire(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			readerName := "reader-" + sandbox.ID
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
				defer stop()
				if _, err := provider.kubectl(
					cleanup,
					nil,
					"delete",
					"pod",
					readerName,
					"sandbox-"+sandbox.ID,
					"--ignore-not-found",
					"--wait",
					"--timeout=60s",
				); err != nil {
					t.Error(err)
					return
				}
				if _, err := provider.kubectl(cleanup, nil, "delete", "pvc", "workspace-"+sandbox.ID,
					"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
					t.Error(err)
				}
			})
			if err := service.provision(ctx, sandbox, acquireRequest{
				TTLSeconds: 120,
				WorkerID:   in.WorkerID,
				RunID:      in.RunID,
				RunEpoch:   in.RunEpoch,
				RequestKey: in.RequestKey,
			}); err != nil {
				t.Fatal(err)
			}
			script := `echo retained > /workspace/result; nohup sh -c 'while true; do echo x >> /workspace/heartbeat; sleep 0.1; done' >/tmp/writer.log 2>&1 </dev/null & echo started`
			if op, err := service.ExecCommand(
				ctx,
				sandbox.ID,
				"write",
				script,
			); err != nil ||
				op.Status != OpStatusSucceeded {
				t.Fatalf("write: %+v %v", op, err)
			}
			active, err := store.GetSandbox(ctx, sandbox.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, status, err := provider.guardCall(
				ctx,
				*active,
				"/stop",
				map[string]string{"podUid": "wrong-instance"},
			); status != 409 ||
				err == nil {
				t.Fatalf("stop accepted wrong instance: %d %v", status, err)
			}
			if expiry {
				if err := store.db.Model(&Lease{}).Where("sandbox_id = ?", sandbox.ID).
					Update("hard_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
					t.Fatal(err)
				}
				reconciler.Sweep(ctx)
			} else if _, err := service.Release(ctx, sandbox.ID); err != nil {
				t.Fatal(err)
			}
			blocked, err := store.GetSandbox(ctx, sandbox.ID)
			if err != nil || blocked.Status != SandboxStatusSaveBlocked || blocked.StoppedAt == nil {
				t.Fatalf("sandbox not stopped and protected: %+v %v", blocked, err)
			}
			for repeat := 0; repeat < 2; repeat++ {
				reconciler.Sweep(ctx)
				if _, err := service.Release(ctx, sandbox.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.Destroy(ctx, *blocked); err != ErrSaveBlocked {
				t.Fatalf("blocked destruction: %v", err)
			}
			reader, err := json.Marshal(map[string]any{
				"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": readerName},
				"spec": map[string]any{
					"restartPolicy": "Never", "automountServiceAccountToken": false,
					"terminationGracePeriodSeconds": 1,
					"containers": []any{
						map[string]any{
							"name":    "reader",
							"image":   image,
							"command": []string{"sh", "-ec", "sleep 180"},
							"securityContext": map[string]any{
								"runAsUser":                65534,
								"readOnlyRootFilesystem":   true,
								"allowPrivilegeEscalation": false,
								"capabilities": map[string]any{
									"drop": []string{"ALL"},
								},
							},
							"volumeMounts": []any{
								map[string]any{
									"name":      "workspace",
									"mountPath": "/workspace",
									"readOnly":  true,
								},
							},
						},
					},
					"volumes": []any{
						map[string]any{
							"name": "workspace",
							"persistentVolumeClaim": map[string]any{
								"claimName": "workspace-" + sandbox.ID,
								"readOnly":  true,
							},
						},
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if out, err := provider.kubectl(ctx, reader, "apply", "-f", "-"); err != nil {
				t.Fatalf("reader: %s %v", out, err)
			}
			if out, err := provider.kubectl(
				ctx,
				nil,
				"wait",
				"--for=condition=Ready",
				"pod/"+readerName,
				"--timeout=60s",
			); err != nil {
				t.Fatalf("reader ready: %s %v", out, err)
			}
			read := func() string {
				t.Helper()
				out, err := provider.kubectl(
					ctx,
					nil,
					"exec",
					readerName,
					"--",
					"sh",
					"-ec",
					"cat /workspace/result; wc -c < /workspace/heartbeat",
				)
				if err != nil || !strings.HasPrefix(string(out), "retained\n") {
					t.Fatalf("workspace data missing: %q %v", out, err)
				}
				return string(out)
			}
			before := read()
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if after := read(); after != before {
				t.Fatalf("writer continued after stop: %q -> %q", before, after)
			}
			t.Logf(
				"stopped original instance; PVC readable from non-root reader; stable contents %q",
				before,
			)
			if out, err := provider.kubectl(
				ctx,
				nil,
				"delete",
				"pod",
				readerName,
				"--wait",
				"--timeout=30s",
			); err != nil {
				t.Fatalf("reader cleanup: %s %v", out, err)
			}
			if err := store.confirmRequiredSave(
				ctx,
				blocked.ID,
				blocked.Generation,
				blocked.Version,
				"test-checkpoint",
			); err != nil {
				t.Fatal(err)
			}
			reconciler.Sweep(ctx)
			destroyed, err := store.GetSandbox(ctx, sandbox.ID)
			if err != nil || destroyed.Status != SandboxStatusDestroyed {
				t.Fatalf("saved sandbox not destroyed: %+v %v", destroyed, err)
			}
			out, err := provider.kubectl(
				ctx,
				nil,
				"get",
				"pvc",
				"workspace-"+sandbox.ID,
				"--ignore-not-found",
				"-o",
				"name",
			)
			if err != nil || len(out) != 0 {
				t.Fatalf("saved workspace not reclaimed: %s %v", out, err)
			}
		})
	}
}

func TestRequiredSaveProvisioningRecoveryE2E(t *testing.T) {
	cluster := os.Getenv("SANDBOX_E2E_K8S_CONTEXT")
	if cluster == "" {
		t.Skip("SANDBOX_E2E_K8S_CONTEXT required")
	}
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:saveblocked"
	}
	store := testStore(t)
	provider, err := NewProvider(K8sConfig{
		Context: cluster, Namespace: "sandbox-center", GuardImage: image, MaxLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	in := AcquireInput{
		OwnerID: "owner", RequestKey: "provisioning-recovery", ProfileID: "coding",
		WorkerID: "worker", RunID: "run", RunEpoch: 1, RequiredSave: true,
		HardExpiresAt: time.Now().Add(time.Hour),
	}
	createRunClaim(t, store, in)
	sandbox, _, err := store.Acquire(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		if _, err := provider.kubectl(cleanup, nil, "delete", "pod", "sandbox-"+sandbox.ID,
			"--ignore-not-found", "--wait", "--timeout=60s"); err != nil {
			t.Error(err)
			return
		}
		if _, err := provider.kubectl(cleanup, nil, "delete", "pvc", "workspace-"+sandbox.ID,
			"--ignore-not-found", "--wait", "--timeout=20s"); err != nil {
			t.Error(err)
		}
	})
	workspaceUID, err := provider.ensureWorkspace(ctx, *sandbox)
	if err != nil {
		t.Fatal(err)
	}
	podUID, err := provider.Provision(ctx, *sandbox, admissionRequest{}, CheckpointConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.waitReady(ctx, *sandbox); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&Sandbox{}).Where("id = ?", sandbox.ID).
		Update("created_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(store, provider, ReconcilerConfig{}, nil)
	if _, err := NewService(store, provider).Release(ctx, sandbox.ID); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil || blocked.Status != SandboxStatusSaveBlocked || blocked.StoppedAt == nil ||
		blocked.PodUID != podUID || blocked.WorkspaceUID != workspaceUID {
		t.Fatalf("unregistered provisioning identity not recovered: %+v %v", blocked, err)
	}
	t.Logf("recovered pre-activation pod=%s pvc=%s; stopped and retained", podUID, workspaceUID)
	if err := store.confirmRequiredSave(
		ctx,
		blocked.ID,
		blocked.Generation,
		blocked.Version,
		"test-checkpoint",
	); err != nil {
		t.Fatal(err)
	}
	reconciler.Sweep(ctx)
	final, err := store.GetSandbox(ctx, sandbox.ID)
	if err != nil || final.Status != SandboxStatusDestroyed {
		t.Fatalf("recovered sandbox did not converge after save: %+v %v", final, err)
	}
}

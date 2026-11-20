package sandboxcenter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestCheckpointRestoreHTTPE2E(t *testing.T) {
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
	namespace := os.Getenv("SANDBOX_E2E_NAMESPACE")
	if namespace == "" {
		namespace = "sandbox-center"
	}
	sourceRuntime := os.Getenv("SANDBOX_E2E_SOURCE_RUNTIME_CLASS")
	restoreRuntime := os.Getenv("SANDBOX_E2E_RESTORE_RUNTIME_CLASS")
	workspaceClass := os.Getenv("SANDBOX_E2E_WORKSPACE_STORAGE_CLASS")
	workspaceAccessMode := os.Getenv("SANDBOX_E2E_WORKSPACE_ACCESS_MODE")
	crossNode := sourceRuntime != "" || restoreRuntime != ""
	if crossNode && (sourceRuntime == "" || restoreRuntime == "" ||
		sourceRuntime == restoreRuntime || checkpoints.StorePVC == "" ||
		workspaceClass == "" || workspaceAccessMode != "ReadWriteMany") {
		t.Fatal(
			"cross-node restore requires distinct RuntimeClasses, a checkpoint PVC and a workspace StorageClass with ReadWriteMany",
		)
	}
	store := testStore(t)
	providerConfig := K8sConfig{
		Context:               cluster,
		Namespace:             namespace,
		GuardImage:            image,
		MaxLifetime:           24 * time.Hour,
		RuntimeClassName:      sourceRuntime,
		WorkspaceStorageClass: workspaceClass,
		WorkspaceAccessMode:   workspaceAccessMode,
	}
	provider, err := NewProvider(providerConfig)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, provider).WithCheckpoints(checkpoints)
	reconciler := NewReconciler(store, provider, ReconcilerConfig{}, nil).WithCheckpoints(service)
	startClient := func(service *Service) *sdk.Client {
		t.Helper()
		router := gin.New()
		NewController(service).RegisterRoutes(router.Group("/v1"))
		server := httptest.NewServer(router)
		t.Cleanup(server.Close)
		client, err := sdk.NewClient(server.URL, "local-test", &http.Client{Timeout: 2 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	client := startClient(service)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	nodeForSandbox := func(id string) string {
		t.Helper()
		out, err := provider.kubectl(ctx, nil, "get", "pod", "sandbox-"+id, "-o", "jsonpath={.spec.nodeName}")
		if node := strings.TrimSpace(string(out)); err == nil && node != "" {
			return node
		}
		t.Fatalf("sandbox %s has no scheduled node: %s %v", id, out, err)
		return ""
	}
	claim := AcquireInput{OwnerID: "restore-owner", WorkerID: "worker", RunID: "restore-run", RunEpoch: 1}
	createRunClaim(t, store, claim)
	request := sdk.AcquireSandboxRequest{
		OwnerID: claim.OwnerID, WorkerID: claim.WorkerID, RunID: claim.RunID, RunEpoch: claim.RunEpoch,
		ProfileID: "coding", RequestKey: "source", TTLSeconds: 600, RequiredSave: true,
	}
	cleanupSandbox := func(id string) {
		t.Helper()
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
			defer stop()
			for _, name := range []string{"saver-" + id, "verify-" + id, "sandbox-" + id} {
				if _, err := provider.kubectl(
					cleanup,
					nil,
					"delete",
					"pod",
					name,
					"--ignore-not-found",
					"--wait",
					"--timeout=30s",
				); err != nil {
					t.Error(err)
				}
			}
			if _, err := provider.kubectl(
				cleanup,
				nil,
				"delete",
				"pvc",
				"workspace-"+id,
				"--ignore-not-found",
				"--wait",
				"--timeout=20s",
			); err != nil {
				t.Error(err)
			}
		})
	}
	execute := func(id, key, script string) {
		t.Helper()
		op, err := client.Sandboxes.Exec(ctx, &sdk.ExecSandboxRequest{ID: id, RequestKey: key, Script: script})
		if err != nil || op == nil || op.Status != sdk.SandboxOpSucceeded || op.ExitCode == nil ||
			*op.ExitCode != 0 {
			t.Fatalf("exec %s: %+v %v", key, op, err)
		}
	}
	save := func(id string) *Sandbox {
		t.Helper()
		started := time.Now()
		if _, err := client.Sandboxes.Release(ctx, id); err != nil {
			t.Fatal(err)
		}
		stale, err := store.GetSandbox(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for ctx.Err() == nil {
			reconciler.Sweep(ctx)
			current, err := store.GetSandbox(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == SandboxStatusDestroyed {
				if err := validateCheckpointID(current.SavedCheckpoint); err != nil {
					t.Fatal(err)
				}
				if err := service.Save(ctx, *stale); err != nil {
					t.Fatalf("late save replay: %v", err)
				}
				for _, resource := range []struct{ kind, name string }{
					{kind: "pod", name: "sandbox-" + id},
					{kind: "pod", name: "saver-" + id},
					{kind: "pod", name: "verify-" + id},
					{kind: "pvc", name: "workspace-" + id},
				} {
					out, err := provider.kubectl(
						ctx,
						nil,
						"get",
						resource.kind,
						resource.name,
						"--ignore-not-found",
						"-o",
						"name",
					)
					if err != nil || len(out) != 0 {
						t.Fatalf(
							"resource survived save: %s %s: %s %v",
							resource.kind,
							resource.name,
							out,
							err,
						)
					}
				}
				t.Logf("sandbox %s release to Destroyed: %s", id, time.Since(started))
				return current
			}
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
		t.Fatal(ctx.Err())
		return nil
	}
	source, err := client.Sandboxes.Acquire(ctx, &request)
	if source != nil && source.Sandbox != nil {
		cleanupSandbox(source.Sandbox.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	execute(source.Sandbox.ID, "seed", `set -eu
mkdir -p /workspace/project/src /workspace/project/empty
printf 'saved workspace\n' > /workspace/project/src/main.txt
chmod 0751 /workspace/project/src/main.txt
chmod 0700 /workspace/project/empty
ln -s src/main.txt /workspace/project/entry
ln /workspace/project/src/main.txt /workspace/project/hardlink
`)
	var sourceNode string
	if crossNode {
		sourceNode = nodeForSandbox(source.Sandbox.ID)
	}
	saved := save(source.Sandbox.ID)
	if crossNode {
		providerConfig.RuntimeClassName = restoreRuntime
		restoreProvider, err := NewProvider(providerConfig)
		if err != nil {
			t.Fatal(err)
		}
		service = NewService(store, restoreProvider).WithCheckpoints(checkpoints)
		reconciler = NewReconciler(store, restoreProvider, ReconcilerConfig{}, nil).WithCheckpoints(service)
		client = startClient(service)
	}
	claim.RunID = "restored-run"
	createRunClaim(t, store, claim)
	request.RunID = claim.RunID
	request.RequestKey = "destination"
	request.RestoreFromSandboxID = source.Sandbox.ID
	freezeRestoreBinding(t, store, claim, &execution.FilesystemBinding{
		Provider: "Sandbox", Workspace: "/workspace", ProfileID: request.ProfileID,
		RestoreFromSandboxID: source.Sandbox.ID,
	})
	started := time.Now()
	target, err := client.Sandboxes.Acquire(ctx, &request)
	if target != nil && target.Sandbox != nil {
		cleanupSandbox(target.Sandbox.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if target.Sandbox.ResumeCheckpoint != saved.SavedCheckpoint || target.Sandbox.Status != SandboxStatusActive ||
		target.Sandbox.RestoreFromSandboxID != source.Sandbox.ID {
		t.Fatalf("restore identity not preserved: %+v", target)
	}
	if crossNode {
		targetNode := nodeForSandbox(target.Sandbox.ID)
		if targetNode == sourceNode {
			t.Fatalf("source and restored sandbox both scheduled on %s", sourceNode)
		}
		t.Logf("cross-node restore: %s -> %s", sourceNode, targetNode)
	}
	t.Logf("restore acquire to Active: %s", time.Since(started))
	execute(target.Sandbox.ID, "verify", `set -eu
test "$(cat /workspace/project/src/main.txt)" = 'saved workspace'
test "$(readlink /workspace/project/entry)" = 'src/main.txt'
test "$(stat -c '%u:%g:%a' /workspace/project/src/main.txt)" = '65534:65534:751'
test "$(stat -c '%u:%g:%a' /workspace/project/empty)" = '65534:65534:700'
test /workspace/project/hardlink -ef /workspace/project/src/main.txt
printf 'new contents\n' > /workspace/project/src/main.txt
`)
	replayed, err := client.Sandboxes.Acquire(ctx, &request)
	if err != nil || replayed.Sandbox.ID != target.Sandbox.ID || replayed.Sandbox.PodUID != target.Sandbox.PodUID {
		t.Fatalf("restore acquire replay: %+v %v", replayed, err)
	}
	execute(target.Sandbox.ID, "verify-replay", `test "$(cat /workspace/project/src/main.txt)" = 'new contents'`)
	save(target.Sandbox.ID)
	if crossNode {
		return
	}

	t.Run("missing checkpoint never activates", func(t *testing.T) {
		stopped := time.Now()
		missing := Sandbox{
			ID:              "missing-checkpoint",
			OwnerID:         claim.OwnerID,
			RequestKey:      "missing",
			Status:          SandboxStatusDestroyed,
			SavedCheckpoint: fmt.Sprintf("sha256:%064x:%064x", 3, 4),
			RequiredSave:    true,
			ProfileID:       "coding",
			StoppedAt:       &stopped,
			PodUID:          "stopped-pod",
			WorkspaceUID:    "old-pvc",
		}
		if err := store.db.Create(&missing).Error; err != nil {
			t.Fatal(err)
		}
		input := claim
		input.RequestKey, input.ProfileID, input.RequiredSave = "failed-restore", "coding", true
		input.HardExpiresAt, input.RestoreFromSandboxID = time.Now().Add(time.Hour), missing.ID
		input.RunID = "missing-checkpoint-run"
		createRunClaim(t, store, input)
		freezeRestoreBinding(t, store, input, &execution.FilesystemBinding{
			Provider:             "Sandbox",
			Workspace:            "/workspace",
			ProfileID:            input.ProfileID,
			RestoreFromSandboxID: missing.ID,
		})
		target, _, err := store.Acquire(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		cleanupSandbox(target.ID)
		shortConfig := providerConfig
		shortConfig.ProvisionDeadline = 8 * time.Second
		shortProvider, err := NewProvider(shortConfig)
		if err != nil {
			t.Fatal(err)
		}
		shortService := NewService(store, shortProvider).WithCheckpoints(checkpoints)
		if err := shortService.provision(ctx, target, acquireRequest{
			WorkerID: input.WorkerID, RunID: input.RunID, RunEpoch: input.RunEpoch,
			RequestKey: input.RequestKey, TTLSeconds: 600,
		}); err == nil {
			t.Fatal("missing checkpoint activated")
		}
		logs, err := provider.kubectl(ctx, nil, "logs", "sandbox-"+target.ID, "-c", "restore")
		if err != nil || !strings.Contains(string(logs), "FileNotFoundError") {
			t.Fatalf("missing checkpoint failure not observed: %s %v", logs, err)
		}
		failed, err := store.GetSandbox(ctx, target.ID)
		if err != nil || failed.Status != SandboxStatusProvisioning || failed.LeaseID != "" {
			t.Fatalf("failed restore was admitted: %+v %v", failed, err)
		}
		reconciler.converge(ctx, *failed)
		abandoned, err := store.GetSandbox(ctx, target.ID)
		if err != nil || abandoned.Status != SandboxStatusLost || abandoned.SavedCheckpoint != "" {
			t.Fatalf("incomplete restore not discarded: %+v %v", abandoned, err)
		}
		for _, kind := range []string{"pod", "pvc"} {
			name := "sandbox-" + target.ID
			if kind == "pvc" {
				name = "workspace-" + target.ID
			}
			out, err := provider.kubectl(ctx, nil, "get", kind, name, "--ignore-not-found", "-o", "name")
			if err != nil || len(out) != 0 {
				t.Fatalf("partial restore leaked %s: %s %v", kind, out, err)
			}
		}
	})
}

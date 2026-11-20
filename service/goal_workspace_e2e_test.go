package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/sandboxcenter"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type workspaceWorkerProcess struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	err     error
	logPath string
}

func startWorkspaceWorker(t *testing.T, ctx context.Context, binary, config string) *workspaceWorkerProcess {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "worker.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "-config", config)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	process := &workspaceWorkerProcess{cmd: cmd, exited: make(chan struct{}), logPath: logPath}
	go func() {
		process.err = cmd.Wait()
		_ = logFile.Close()
		close(process.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-process.exited:
		default:
			_ = process.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-process.exited:
				if process.err != nil && ctx.Err() == nil {
					t.Errorf("worker exit: %v", process.err)
				}
			case <-time.After(10 * time.Second):
				_ = process.cmd.Process.Kill()
				<-process.exited
				t.Error("worker did not stop")
			}
		}
		if t.Failed() {
			output, _ := os.ReadFile(logPath)
			t.Logf("worker log %s:\n%s", logPath, output)
		}
	})
	return process
}

func killWorkspaceWorker(t *testing.T, process *workspaceWorkerProcess) {
	t.Helper()
	if err := process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.exited:
		if process.err == nil {
			t.Fatal("worker exited without being killed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not exit after kill")
	}
}

func waitWorkspaceWorker(
	t *testing.T,
	ctx context.Context,
	process *workspaceWorkerProcess,
	description string,
	ready func() bool,
) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", description, ctx.Err())
		case <-process.exited:
			t.Fatalf("worker exited while waiting for %s: %v", description, process.err)
		case <-ticker.C:
		}
	}
}

func TestGoalWorkspaceWorkerKubernetesE2E(t *testing.T) {
	cluster, binary := os.Getenv("SANDBOX_E2E_K8S_CONTEXT"), os.Getenv("SANDBOX_E2E_WORKER_BINARY")
	if cluster == "" || binary == "" {
		t.Skip("SANDBOX_E2E_K8S_CONTEXT and SANDBOX_E2E_WORKER_BINARY required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	store := integrationStore(t)
	image := os.Getenv("SANDBOX_E2E_GUARD_IMAGE")
	if image == "" {
		image = "agentic-guard-execd:saveblocked"
	}
	provider, err := sandboxcenter.NewProvider(sandboxcenter.K8sConfig{
		Context: cluster, Namespace: "sandbox-center", GuardImage: image, MaxLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := sandboxcenter.CheckpointConfig{
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
	centerStore := sandboxcenter.NewStore(store.DB())
	center := sandboxcenter.NewService(centerStore, provider).WithCheckpoints(checkpoints)
	server := httptest.NewServer(
		newRouter(t, store, nil, map[string]string{testToken: "owner"}, func(group *gin.RouterGroup) {
			sandboxcenter.NewController(center).RegisterRoutes(group.Group("/v1"))
		}),
	)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		var sandboxes []sandboxcenter.Sandbox
		if err := store.DB().WithContext(cleanup).Find(&sandboxes).Error; err != nil {
			t.Error(err)
			return
		}
		for _, sandbox := range sandboxes {
			for _, resource := range []struct{ kind, name string }{
				{kind: "pod", name: "saver-" + sandbox.ID},
				{kind: "pod", name: "verify-" + sandbox.ID},
				{kind: "pod", name: "sandbox-" + sandbox.ID},
				{kind: "pvc", name: "workspace-" + sandbox.ID},
			} {
				command := exec.CommandContext(
					cleanup,
					"kubectl",
					"--context",
					cluster,
					"-n",
					"sandbox-center",
					"delete",
					resource.kind,
					resource.name,
					"--ignore-not-found",
					"--wait",
					"--timeout=10s",
				)
				if output, err := command.CombinedOutput(); err != nil {
					t.Errorf("cleanup %s: %s %v", resource.name, output, err)
				}
			}
		}
	})
	marker := durable.NewID()
	var modelTools atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil || len(input.Messages) == 0 {
			http.Error(writer, "invalid model request", http.StatusBadRequest)
			return
		}
		message := map[string]any{"role": "assistant", "content": "workspace updated"}
		finish := "stop"
		if input.Messages[len(input.Messages)-1].Role != "tool" {
			script := fmt.Sprintf(
				"set -eu; printf '%%s' '%s' > /workspace/inherited.txt; printf seeded",
				marker,
			)
			for _, previous := range input.Messages {
				if strings.Contains(string(previous.Content), "previous run failed acceptance") {
					script = fmt.Sprintf(
						"set -eu; test \"$(cat /workspace/inherited.txt)\" = '%s'; printf accepted > /workspace/repaired.txt; printf 'inherited:%%s' '%s'",
						marker,
						marker,
					)
					break
				}
			}
			arguments, _ := json.Marshal(map[string]string{"command": script})
			message["tool_calls"] = []any{map[string]any{
				"id": fmt.Sprintf("workspace-%d", modelTools.Add(1)), "type": "function",
				"function": map[string]any{"name": "execute", "arguments": string(arguments)},
			}}
			finish = "tool_calls"
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "workspace-completion", "object": "chat.completion", "model": "workspace-test",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
			"usage": map[string]any{
				"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2,
			},
		})
	}))
	t.Cleanup(model.Close)
	client, err := sdk.NewClient(server.URL, testToken, &http.Client{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	agent, err := client.Agents.Create(ctx, &sdk.CreateAgentRequest{
		Name: "workspace-goal",
		Config: sdk.AgentConfig{
			Model: sdk.ModelSpec{Name: "workspace-test", BaseURL: model.URL + "/v1"}, MaxTurns: 4,
			Toolsets: []string{"filesystem"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.Agents.Publish(ctx, &sdk.PublishAgentRequest{ID: agent.ID, Revision: agent.Revision})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Executions.Submit(ctx, &sdk.SubmitExecutionRequest{
		IdempotencyKey: "workspace-goal", TimeoutSeconds: 300,
		Spec: sdk.ExecutionSpec{
			AgentID:      agent.ID,
			AgentVersion: version.Number,
			Query:        "update the workspace",
			Scope: &sdk.Scope{
				TenantID: "tenant",
				ActorID:  "owner",
				TaskID:   "repair",
				RoundID:  "first",
			},
			Filesystem: &sdk.FilesystemBinding{
				Provider:  "Sandbox",
				URL:       server.URL,
				TokenEnv:  "SANDBOX_CENTER_TOKEN",
				Workspace: "/workspace",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, _, err := store.CreateGoal(ctx, durable.Goal{
		OwnerID:    "owner",
		SessionID:  first.SessionID,
		RequestKey: "workspace-goal",
		Objective:  "repair the inherited workspace",
		Evaluator: durable.EvaluatorSpec{
			Kind: durable.EvaluatorCommand,
			Config: fmt.Sprintf(
				"test \"$(cat /workspace/inherited.txt)\" = '%s' && test \"$(cat /workspace/repaired.txt)\" = accepted && printf accepted",
				marker,
			),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindGoalToRun(ctx, "owner", goal.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := store.DB().Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	const workerPoll = 100 * time.Millisecond
	workerConfig := filepath.Join(t.TempDir(), "worker.toml")
	if err := os.WriteFile(workerConfig, []byte(fmt.Sprintf(`AppName = "sandbox-goal-e2e"
AppCode = 101
RunMode = "release"
[HTTPServer]
Listen = "127.0.0.1:0"
[DB]
Driver = "PostgreSQL"
DSN = "${env:DATABASE_URL}"
AutoMigrate = false
[Worker]
Concurrency = 1
Lease = "30s"
Poll = %q
[Evaluator]
BaseURL = %q
TokenEnv = "SANDBOX_CENTER_TOKEN"
`, workerPoll.String(), server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", dsn.String())
	t.Setenv("SANDBOX_CENTER_TOKEN", testToken)
	worker := startWorkspaceWorker(t, ctx, binary, workerConfig)
	var source sandboxcenter.Sandbox
	waitWorkspaceWorker(t, ctx, worker, "failed acceptance and pending workspace save", func() bool {
		receipt, err := store.GetEvaluation(ctx, "owner", goal.ID, first.ID)
		if errors.Is(err, durable.ErrNotFound) {
			run, readErr := store.Get(ctx, "owner", first.ID)
			if readErr != nil || run.Status == durable.Failed || run.Status == durable.NeedsReview {
				events, _ := store.Events(ctx, "owner", first.ID, 0)
				t.Fatalf(
					"first run did not reach acceptance: %+v events=%+v err=%v",
					run,
					events,
					readErr,
				)
			}
			return false
		}
		if err != nil || receipt.Status != durable.EvaluationFailed {
			t.Fatalf("first acceptance: %+v %v", receipt, err)
		}
		var environments []table.RunEnvironment
		if err := store.DB().
			Where("run_id = ?", first.ID).
			Find(&environments).
			Error; err != nil ||
			len(environments) != 1 {
			t.Fatalf("first environment: %+v %v", environments, err)
		}
		current, err := centerStore.GetSandbox(ctx, environments[0].SandboxID)
		if err != nil {
			t.Fatal(err)
		}
		source = *current
		return source.Status == sandboxcenter.SandboxStatusSaveBlocked && source.StoppedAt != nil
	})
	blockedUntil := time.Now().Add(5 * workerPoll)
	waitWorkspaceWorker(t, ctx, worker, "pending save blocking iteration across worker polls", func() bool {
		if next, err := store.QueueGoalIteration(ctx); !errors.Is(err, durable.ErrNoWork) || next != nil {
			t.Fatalf("iteration bypassed pending save: %+v %v", next, err)
		}
		runs, err := store.SessionRuns(ctx, "owner", first.SessionID, "", 10)
		if err != nil || len(runs) != 1 || runs[0].ID != first.ID || modelTools.Load() != 1 {
			t.Fatalf("iteration started before save: %+v tools=%d err=%v", runs, modelTools.Load(), err)
		}
		current, err := store.GetGoal(ctx, "owner", goal.ID)
		if err != nil || current.Status != durable.GoalActive || current.ActiveRunID != first.ID {
			t.Fatalf("goal advanced before save: %+v %v", current, err)
		}
		return !time.Now().Before(blockedUntil)
	})
	killWorkspaceWorker(t, worker)
	firstRun, err := store.Get(ctx, "owner", first.ID)
	if err != nil || firstRun.EvaluationWorkerID == "" || firstRun.Status != durable.Completed {
		t.Fatalf("first run before restart: %+v %v", firstRun, err)
	}
	worker = startWorkspaceWorker(t, ctx, binary, workerConfig)
	reconciler := sandboxcenter.NewReconciler(centerStore, provider,
		sandboxcenter.ReconcilerConfig{Interval: 500 * time.Millisecond}, nil).WithCheckpoints(center)
	reconcileCtx, stopReconciler := context.WithCancel(ctx)
	reconciled := make(chan error, 1)
	go func() { reconciled <- reconciler.Run(reconcileCtx) }()
	t.Cleanup(func() {
		stopReconciler()
		if err := <-reconciled; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	started := time.Now()
	var currentGoal *durable.Goal
	waitWorkspaceWorker(t, ctx, worker, "automatic iteration accepted and both workspaces saved", func() bool {
		var err error
		currentGoal, err = store.GetGoal(ctx, "owner", goal.ID)
		if err != nil || currentGoal.Status == durable.GoalBlocked || currentGoal.Status == durable.GoalFailed {
			t.Fatalf("goal: %+v %v", currentGoal, err)
		}
		var unfinished int64
		if err := store.DB().
			Model(&table.RunEnvironment{}).
			Where("status <> ?", durable.EnvironmentReleased).
			Count(&unfinished).
			Error; err != nil {
			t.Fatal(err)
		}
		return currentGoal.Status == durable.GoalComplete && unfinished == 0
	})
	runs, err := store.SessionRuns(ctx, "owner", first.SessionID, "", 10)
	if err != nil || len(runs) != 2 || modelTools.Load() != 2 {
		t.Fatalf("iterations: %+v tools=%d err=%v", runs, modelTools.Load(), err)
	}
	next, err := store.Get(ctx, "owner", currentGoal.ActiveRunID)
	if err != nil || next.ID == first.ID || next.Status != durable.Completed ||
		next.EvaluationWorkerID == "" || next.EvaluationWorkerID == firstRun.EvaluationWorkerID ||
		next.Spec.Filesystem.RestoreFromSandboxID != source.ID {
		t.Fatalf("restored run: %+v %v", next, err)
	}
	var environments []table.RunEnvironment
	if err := store.DB().Order("created_at").Find(&environments).Error; err != nil || len(environments) != 2 {
		t.Fatalf("saved environments: %+v %v", environments, err)
	}
	for _, environment := range environments {
		sandbox, err := centerStore.GetSandbox(ctx, environment.SandboxID)
		if err != nil || sandbox.Status != sandboxcenter.SandboxStatusDestroyed ||
			sandbox.SavedCheckpoint == "" || sandbox.SavedCheckpoint != environment.SavedCheckpoint {
			t.Fatalf("workspace not saved: %+v %+v %v", environment, sandbox, err)
		}
		if environment.RunID == next.ID &&
			(sandbox.ID == source.ID || sandbox.ResumeCheckpoint != environments[0].SavedCheckpoint || sandbox.RestoreFromSandboxID != source.ID) {
			t.Fatalf("checkpoint lineage mismatch: %+v", sandbox)
		}
		for _, resource := range []struct{ kind, name string }{
			{kind: "pod", name: "sandbox-" + sandbox.ID},
			{kind: "pod", name: "saver-" + sandbox.ID},
			{kind: "pod", name: "verify-" + sandbox.ID},
			{kind: "pvc", name: "workspace-" + sandbox.ID},
		} {
			output, err := exec.CommandContext(
				ctx,
				"kubectl",
				"--context",
				cluster,
				"-n",
				"sandbox-center",
				"get",
				resource.kind,
				resource.name,
				"--ignore-not-found",
				"-o",
				"name",
			).CombinedOutput()
			if err != nil || len(output) != 0 {
				t.Fatalf("resource survived: %s %s %v", resource.name, output, err)
			}
		}
	}
	receipt, err := store.GetEvaluation(ctx, "owner", goal.ID, next.ID)
	if err != nil || receipt.Status != durable.EvaluationPassed || !strings.Contains(receipt.Output, "accepted") {
		t.Fatalf("restored acceptance: %+v %v", receipt, err)
	}
	t.Logf(
		"goal=%s first=%s next=%s source=%s workers=%s->%s save through accepted cleanup=%s",
		goal.ID,
		first.ID,
		next.ID,
		source.ID,
		firstRun.EvaluationWorkerID,
		next.EvaluationWorkerID,
		time.Since(started),
	)
}

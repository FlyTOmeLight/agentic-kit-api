package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestRunTracePublicReplayAndOwnerIsolation(t *testing.T) {
	store := integrationStore(t)
	server := httptest.NewServer(
		NewRouter(
			t,
			store,
			nil,
			[]string{"owner"},
			map[string]string{testToken: "owner", "other-token-at-least-24-characters": "other"},
		),
	)
	defer server.Close()
	client, err := sdk.NewClient(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	agent, err := client.Agents.Create(
		ctx,
		sdk.CreateAgentRequest{
			Name: "Trace",
			Config: sdk.AgentConfig{
				SystemPrompt: "private-system-prompt",
				Model: sdk.ModelSpec{
					Name:      "model",
					BaseURL:   "http://model.test/v1",
					APIKeyEnv: "PRIVATE_KEY",
				},
				MaxTurns: 3,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.Agents.Publish(ctx, agent.ID, sdk.PublishAgentRequest{Revision: agent.Revision})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := client.Executions.Submit(
		ctx,
		"round",
		sdk.SubmitExecutionRequest{
			Spec: sdk.ExecutionSpec{
				Scope: &sdk.Scope{
					TenantID:  "tenant",
					ProjectID: "project",
					TaskID:    "task",
					RoundID:   "round",
					ActorID:   "actor",
				},
				AgentID:      agent.ID,
				AgentVersion: version.Number,
				Query:        "private-query",
			},
			TimeoutSeconds: 60,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	lease := durable.Lease{RunID: run.ID, WorkerID: "worker", Epoch: claimed.Epoch}
	state := json.RawMessage(`{"reasoning_content":"private-reasoning","credentials":"private-secret"}`)
	call := sdk.ToolCall{ID: "tool-1", Name: "read_file", Arguments: `{"path":"src/calc.py"}`}
	if err = store.SaveDecision(
		ctx,
		lease,
		state,
		sdk.Message{Role: "assistant", Content: "读取当前代码", ToolCalls: []sdk.ToolCall{call}},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.BeginStep(
		ctx,
		lease,
		call.ID,
		call.Name+"\x00"+call.Arguments,
		durable.ReadOnly,
	); err != nil {
		t.Fatal(err)
	}
	first, err := client.Runs.Trace(ctx, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.LatestEvent.Kind != "ToolStarted" || first.LatestEvent.CreatedAt.IsZero() {
		t.Fatalf("missing live step: %+v", first)
	}
	var payload map[string]any
	_ = json.Unmarshal(first.LatestEvent.Payload, &payload)
	if payload["name"] != "read_file" || payload["arguments"] != call.Arguments {
		t.Fatal(payload)
	}
	if err = store.FinishStep(ctx, lease, call.ID, strings.Repeat("中", 12000), state); err != nil {
		t.Fatal(err)
	}
	// Fill two pages to prove reconnect is ordered and latest progress is independent of cursor.
	for i := 0; i < 55; i++ {
		if err = store.Save(ctx, lease, state, durable.Running, "", "", "ModelStarted"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := client.Runs.Trace(ctx, run.ID, 0)
	if err != nil || !page.HasMore || len(page.Events) != 50 || page.LatestEvent.Kind != "ModelStarted" {
		t.Fatalf("page: %+v %v", page, err)
	}
	tail, err := client.Runs.Trace(ctx, run.ID, page.NextSeq)
	if err != nil || tail.HasMore || tail.Events[0].Seq != page.NextSeq+1 || tail.NextSeq != tail.Run.EventSeq {
		t.Fatalf("tail: %+v %v", tail, err)
	}
	for _, event := range page.Events {
		if event.Kind == "ToolCompleted" {
			_ = json.Unmarshal(event.Payload, &payload)
			if payload["resultTruncated"] != true || len(payload["result"].(string)) > 32<<10 {
				t.Fatal("unmarked truncation")
			}
		}
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{"private-system-prompt", "private-query", "private-reasoning", "private-secret", "PRIVATE_KEY", "leaseUntil", "workerID"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private field exposed: %s", secret)
		}
	}
	other, _ := sdk.NewClient(server.URL, "other-token-at-least-24-characters", server.Client())
	_, err = other.Runs.Trace(ctx, run.ID, 0)
	var apiErr *serrors.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("owner isolation: %v", err)
	}
	_, err = client.Runs.Trace(ctx, run.ID, -1)
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("invalid cursor: %v", err)
	}
	fresh, err := client.Runs.Trace(ctx, run.ID, tail.NextSeq)
	if err != nil || len(fresh.Events) != 0 || fresh.LatestEvent.Seq != tail.NextSeq {
		t.Fatalf("reconnect: %+v %v", fresh, err)
	}
}

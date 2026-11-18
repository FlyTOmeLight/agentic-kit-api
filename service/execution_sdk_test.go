package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestSDKTrustedApplicationExecution(t *testing.T) {
	store := integrationStore(t)
	server := httptest.NewServer(
		NewRouter(
			t,
			store,
			nil,
			map[string]string{testToken: "owner", "ordinary-token-with-24-characters": "ordinary"},
		),
	)
	defer server.Close()
	server.Client().Transport = scopeTransport{base: server.Client().Transport, tenant: "tenant", project: "project"}
	client, e := sdk.NewClient(server.URL, testToken, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	legacy, _ := http.NewRequestWithContext(
		ctx,
		"POST",
		server.URL+"/internal/v1/executions",
		strings.NewReader(
			`{"spec":{"systemPrompt":"override","model":{"name":"injected"}},"timeoutSeconds":60}`,
		),
	)
	legacy.Header.Set("Authorization", "Bearer "+testToken)
	legacy.Header.Set("Content-Type", "application/json")
	legacyResponse, e := server.Client().Do(legacy)
	if e != nil {
		t.Fatal(e)
	}
	legacyResponse.Body.Close()
	if legacyResponse.StatusCode != 400 {
		t.Fatalf("inline Agent definition accepted: %d", legacyResponse.StatusCode)
	}
	agent, e := client.Agents.Create(
		ctx, &sdk.CreateAgentRequest{
			Name: "Coding",
			Config: sdk.AgentConfig{
				Model:    sdk.ModelSpec{Name: "model", BaseURL: "http://model.test/v1"},
				MaxTurns: 3,
			},
		},
	)
	if e != nil {
		t.Fatal(e)
	}
	submitStatus := func(c *sdk.Client, key string, version int64) int {
		_, err := c.Executions.Submit(ctx, &sdk.SubmitExecutionRequest{
			IdempotencyKey: key,
			Spec: sdk.ExecutionSpec{
				Scope: &sdk.Scope{
					TenantID:  "tenant",
					ProjectID: "project",
					ActorID:   "actor",
					TaskID:    "task",
					RoundID:   key,
				},
				Query:        "fix",
				AgentID:      agent.ID,
				AgentVersion: version,
			},
			TimeoutSeconds: 60,
		})
		var apiErr *serrors.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("%s: want API error, got %v", key, err)
		}
		return apiErr.Status
	}
	if status := submitStatus(client, "unpublished", 1); status != 404 {
		t.Fatalf("unpublished version: %d", status)
	}
	version, e := client.Agents.Publish(ctx, &sdk.PublishAgentRequest{ID: agent.ID, Revision: agent.Revision})
	if e != nil {
		t.Fatal(e)
	}
	ordinary, e := sdk.NewClient(server.URL, "ordinary-token-with-24-characters", server.Client())
	if e != nil {
		t.Fatal(e)
	}
	defer ordinary.Close()
	if status := submitStatus(ordinary, "foreign", version.Number); status != 404 {
		t.Fatalf("foreign agent: %d", status)
	}
	request := sdk.SubmitExecutionRequest{
		Spec: sdk.ExecutionSpec{
			Scope: &sdk.Scope{
				TenantID:  "tenant",
				ProjectID: "project",
				ActorID:   "actor",
				TaskID:    "task",
				RoundID:   "round",
			},
			Query:        "fix",
			AgentID:      agent.ID,
			AgentVersion: version.Number,
		},
		TimeoutSeconds: 60,
	}
	request.IdempotencyKey = "round"
	run, e := client.Executions.Submit(ctx, &request)
	created := run != nil && run.Created

	if e != nil || !created || run.Scope.TenantID != "tenant" {
		t.Fatal(e)
	}
	request.IdempotencyKey = "round"
	again, e := client.Executions.Submit(ctx, &request)
	created = again != nil && again.Created

	if e != nil || created || again.ID != run.ID {
		t.Fatal(e)
	}
	claimed, e := store.Claim(ctx, "worker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	lease, e := client.Executions.CheckLease(ctx, &sdk.CheckExecutionLeaseRequest{ID: run.ID, Epoch: claimed.Epoch})
	if e != nil || !lease.Allowed {
		t.Fatal(e)
	}
	if e = client.Runs.Cancel(ctx, &sdk.CancelRunRequest{ID: run.ID}); e != nil {
		t.Fatal(e)
	}
	lease, e = client.Executions.CheckLease(ctx, &sdk.CheckExecutionLeaseRequest{ID: run.ID, Epoch: claimed.Epoch})
	if e != nil || lease.Allowed {
		t.Fatal(e)
	}
}

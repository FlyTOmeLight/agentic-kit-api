package httpapi

import (
	"context"
	"errors"
	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSDKTrustedApplicationExecution(t *testing.T) {
	store := integrationStore(t)
	server := httptest.NewServer(NewRouter(store, nil, []string{"owner"}, map[string]string{testToken: "owner", "ordinary-token-with-24-characters": "ordinary"}))
	defer server.Close()
	client, e := sdk.NewClient(server.URL, testToken, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	legacy, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/internal/v1/executions", strings.NewReader(`{"spec":{"systemPrompt":"override","model":{"name":"injected"}},"timeoutSeconds":60}`))
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
	agent, e := client.Agents.Create(ctx, sdk.CreateAgentRequest{Name: "Coding", Config: sdk.AgentConfig{Model: sdk.ModelSpec{Name: "model", BaseURL: "http://model.test/v1"}, MaxTurns: 3}})
	if e != nil {
		t.Fatal(e)
	}
	version, e := client.Agents.Publish(ctx, agent.ID, sdk.PublishAgentRequest{Revision: agent.Revision})
	if e != nil {
		t.Fatal(e)
	}
	request := sdk.SubmitExecutionRequest{Spec: sdk.ExecutionSpec{Scope: &sdk.Scope{TenantID: "tenant", ProjectID: "project", ActorID: "actor", TaskID: "task", RoundID: "round"}, Query: "fix", AgentID: agent.ID, AgentVersion: version.Number}, TimeoutSeconds: 60}
	run, created, e := client.Executions.Submit(ctx, "round", request)
	if e != nil || !created || run.Scope.TenantID != "tenant" {
		t.Fatal(e)
	}
	again, created, e := client.Executions.Submit(ctx, "round", request)
	if e != nil || created || again.ID != run.ID {
		t.Fatal(e)
	}
	claimed, e := store.Claim(ctx, "worker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	lease, e := client.Executions.CheckLease(ctx, run.ID, claimed.Epoch)
	if e != nil || !lease.Allowed {
		t.Fatal(e)
	}
	ordinary, _ := sdk.NewClient(server.URL, "ordinary-token-with-24-characters", server.Client())
	_, _, e = ordinary.Executions.Submit(ctx, "other", request)
	var apiErr *sdk.APIError
	if !errors.As(e, &apiErr) || apiErr.StatusCode != 403 {
		t.Fatal(e)
	}
	if e = client.Runs.Cancel(ctx, run.ID); e != nil {
		t.Fatal(e)
	}
	lease, e = client.Executions.CheckLease(ctx, run.ID, claimed.Epoch)
	if e != nil || lease.Allowed {
		t.Fatal(e)
	}
}

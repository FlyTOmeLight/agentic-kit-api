package httpapi

import (
	"context"
	"errors"
	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"net/http/httptest"
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
	request := sdk.SubmitExecutionRequest{Spec: sdk.ExecutionSpec{Scope: &sdk.Scope{TenantID: "tenant", ProjectID: "project", ActorID: "actor", TaskID: "task", RoundID: "round"}, Query: "fix", Model: sdk.ModelSpec{Name: "model", BaseURL: "http://model.test/v1"}, MaxTurns: 3}, TimeoutSeconds: 60}
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

package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	framesdk "github.com/FlyTOmeLight/server-go/sdk"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestEnterpriseAssetsWithoutProjectAndDatasetSnapshots(t *testing.T) {
	store := integrationStore(t)
	ctx := t.Context()
	a, err := store.CreateAgent(
		ctx,
		"templates",
		"Writing",
		sdk.AgentConfig{Model: sdk.ModelSpec{Name: "test", BaseURL: "http://model/v1"}, MaxTurns: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PublishAgent(ctx, "templates", a.ID, 1); err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/internal/v1/session/check":
			if body["session"] != "member" {
				w.WriteHeader(401)
				return
			}
			if body["method"] != "GET" && body["csrf"] != "csrf" {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "member"})
		case "/internal/v1/workspace/check":
			if revoked.Load() {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).
				Encode(map[string]any{"allowed": true, "canEdit": true, "projects": []any{}})
		case "/internal/v1/access/check":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": false, "level": 0})
		default:
			w.WriteHeader(404)
		}
	}))
	defer tenant.Close()
	cfg := platform.Config{
		Tenant: framesdk.Config{
			Endpoint: tenant.URL,
			Credentials: &framesdk.Credentials{
				AuthType: framesdk.AuthTypeAPIKey,
				APIKey:   strings.Repeat("t", 32),
			},
		},
	}
	server := httptest.NewServer(
		NewPlatformRouter(t, store, nil, []string{"templates"}, map[string]string{testToken: "templates"}, cfg),
	)
	defer server.Close()
	c, err := sdk.NewWorkspaceClient(server.URL, "tenant", "member", "csrf", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	code := func(err error, want int) {
		t.Helper()
		var e *serrors.Error
		if !errors.As(err, &e) || e.Status != want {
			t.Fatalf("want %d got %v", want, err)
		}
	}
	owner := fmt.Sprintf("forge-tenant:%x", sha256.Sum256([]byte("tenant")))
	createdAgent, err := store.CreateAgent(ctx, owner, "Enterprise writer", a.Draft)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := c.Agent(ctx, &sdk.GetProjectAgentRequest{ID: createdAgent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Candidate(
		ctx,
		&sdk.PublishProjectAgentRequest{ID: agent.ID, Revision: agent.Revision},
	); err != nil {
		t.Fatal(err)
	}
	dataset, err := c.SaveDataset(
		ctx,
		&sdk.SaveDatasetRequest{
			ID:    "shared-cases",
			Name:  "Shared",
			Cases: []sdk.EvaluationCase{{ID: "one", Input: "answer JSON"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.PublishDataset(ctx, &sdk.PublishDatasetRequest{ID: dataset.ID, Revision: dataset.Revision})
	if err != nil || v.Number != 1 {
		t.Fatal(v, err)
	}
	suite := sdk.EvaluationSuite{
		SchemaVersion: 1,
		Name:          "Regression",
		Version:       "1",
		Target: sdk.EvaluationTarget{
			Kind:         "Agentic",
			AgentID:      agent.ID,
			AgentVersion: 1,
			Capabilities: []string{"output"},
		},
		Scenario: sdk.EvaluationScenario{Kind: "single_turn", TimeoutSeconds: 30},
		Dataset:  v.Snapshot,
		Evaluators: []sdk.EvaluatorSpec{
			{ID: "json", Version: "1", Kind: "json_valid", Scope: "output", Requires: []string{"output"}},
		},
		Metrics: []sdk.EvaluationMetric{
			{ID: "pass", Evaluator: "json", Aggregate: "pass_rate", MinimumScore: 1, MinimumCoverage: 1},
		},
	}
	suite.Dataset.Cases[0].Input = "tampered client text"
	plan, err := c.SaveEvaluationPlan(ctx, &sdk.SaveEvaluationPlanRequest{ID: "shared-plan", Suite: suite})
	if err != nil || plan.Suite.Dataset.Cases[0].Input != "answer JSON" {
		t.Fatal(plan, err)
	}
	e, err := c.Start(ctx, &sdk.StartProjectEvaluationRequest{IdempotencyKey: "first", Suite: suite})
	created := e != nil && e.Created
	if err != nil || !created || e.Scope.ProjectID != "" || e.Suite.Dataset.Cases[0].Input != "answer JSON" {
		t.Fatal(e, err)
	}
	again, err := c.Start(ctx, &sdk.StartProjectEvaluationRequest{IdempotencyKey: "first", Suite: suite})
	created = again != nil && again.Created
	if err != nil || created || again.ID != e.ID {
		t.Fatal(again, err)
	}
	_, err = c.SaveDataset(
		ctx, &sdk.SaveDatasetRequest{ID: dataset.ID, Revision: 0, Name: "Conflict", Cases: dataset.Cases},
	)
	code(err, 409)
	changed, err := c.SaveDataset(
		ctx, &sdk.SaveDatasetRequest{
			ID: dataset.ID, Revision: 1,
			Name:  "Shared",
			Cases: []sdk.EvaluationCase{{ID: "one", Input: "new input"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.PublishDataset(ctx, &sdk.PublishDatasetRequest{ID: dataset.ID, Revision: changed.Revision})
	if err != nil || next.Number != 2 {
		t.Fatal(next, err)
	}
	old, err := c.DatasetVersion(ctx, &sdk.GetDatasetVersionRequest{ID: dataset.ID, Number: 1})
	if err != nil || old.Snapshot.Cases[0].Input != "answer JSON" {
		t.Fatal(old, err)
	}
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if old, err = c.DatasetVersion(
		ctx,
		&sdk.GetDatasetVersionRequest{ID: dataset.ID, Number: 1},
	); err != nil ||
		old.Snapshot.Cases[0].Input != "answer JSON" {
		t.Fatal(old, err)
	}
	foreign, _ := sdk.NewWorkspaceClient(server.URL, "other", "member", "csrf", server.Client())
	_, err = foreign.Agent(ctx, &sdk.GetProjectAgentRequest{ID: agent.ID})
	code(err, 404)
	_, err = foreign.Dataset(ctx, &sdk.GetDatasetRequest{ID: dataset.ID})
	code(err, 404)
	legacy, _ := sdk.NewProjectClient(server.URL, "tenant", "private", "member", "csrf", server.Client())
	_, err = legacy.Agents(ctx, &sdk.ListProjectAgentsRequest{PageOptions: sdk.PageOptions{}})
	code(err, 403)
	wrong, _ := sdk.NewWorkspaceClient(server.URL, "tenant", "member", "wrong", server.Client())
	_, err = wrong.Candidate(ctx, &sdk.PublishProjectAgentRequest{ID: agent.ID, Revision: agent.Revision})
	code(err, 403)
	list, err := c.Evaluations(ctx, &sdk.ListProjectEvaluationsRequest{PageOptions: sdk.PageOptions{}})
	if err != nil || len(list.Items) != 1 {
		t.Fatal(list, err)
	}
	if err = c.Cancel(ctx, &sdk.CancelProjectEvaluationRequest{ID: e.ID}); err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	_, err = c.Agent(ctx, &sdk.GetProjectAgentRequest{ID: agent.ID})
	code(err, 403)
}

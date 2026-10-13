package service

import (
	"errors"
	"net/http/httptest"
	"testing"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestSDKEvaluationLifecycleAndAuthorization(t *testing.T) {
	store := integrationStore(t)
	server := httptest.NewServer(
		NewRouter(
			t,
			store,
			nil,
			[]string{"owner", "other"},
			map[string]string{
				testToken:                           "owner",
				"ordinary-token-with-24-characters": "ordinary",
				"other-token-with-24-characters":    "other",
			},
		),
	)
	defer server.Close()
	client, err := sdk.NewClient(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	agent, err := client.Agents.Create(
		ctx,
		sdk.CreateAgentRequest{
			Name: "Eval",
			Config: sdk.AgentConfig{
				Model:    sdk.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
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
	req := sdk.StartEvaluationRequest{
		Scope: sdk.EvaluationScope{TenantID: "t", ProjectID: "p", ActorID: "a"},
		Suite: sdk.EvaluationSuite{
			SchemaVersion: 1,
			Name:          "suite",
			Version:       "1",
			Target: sdk.EvaluationTarget{
				Kind:         "Agentic",
				AgentID:      agent.ID,
				AgentVersion: version.Number,
				Capabilities: []string{"output"},
			},
			Scenario: sdk.EvaluationScenario{Kind: "single_turn", TimeoutSeconds: 60},
			Dataset: sdk.EvaluationDataset{
				Name:    "cases",
				Version: "1",
				Cases:   []sdk.EvaluationCase{{ID: "a", Input: "test"}},
			},
			Evaluators: []sdk.EvaluatorSpec{
				{
					ID:       "json",
					Version:  "1",
					Kind:     "json_valid",
					Scope:    "output",
					Requires: []string{"output"},
				},
			},
			Metrics: []sdk.EvaluationMetric{
				{
					ID:              "format",
					Evaluator:       "json",
					Aggregate:       "pass_rate",
					MinimumScore:    1,
					MinimumCoverage: 1,
				},
			},
		},
	}
	out, created, err := client.Evaluations.Start(ctx, "stable", req)
	if err != nil || !created {
		t.Fatal(err)
	}
	replay, created, err := client.Evaluations.Start(ctx, "stable", req)
	if err != nil || created || replay.ID != out.ID {
		t.Fatal(err)
	}
	rows, err := client.Evaluations.List(ctx, sdk.ListEvaluationsOptions{TenantID: "t", ProjectID: "p"})
	if err != nil || len(rows.Items) != 1 {
		t.Fatal(err)
	}
	ordinary, _ := sdk.NewClient(server.URL, "ordinary-token-with-24-characters", server.Client())
	_, _, err = ordinary.Evaluations.Start(ctx, "other", req)
	var apiErr *serrors.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatal("ordinary submit", err)
	}
	other, _ := sdk.NewClient(server.URL, "other-token-with-24-characters", server.Client())
	_, err = other.Evaluations.Get(ctx, out.ID)
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatal("cross-owner read", err)
	}
	if err = client.Evaluations.Cancel(ctx, out.ID); err != nil {
		t.Fatal(err)
	}
	got, err := client.Evaluations.Get(ctx, out.ID)
	if err != nil || got.Status != "Canceled" || got.Report.Passed || got.Trials[0].Run.Status != sdk.Canceled {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	comparison, err := client.Evaluations.Compare(ctx, out.ID, out.ID)
	if err != nil || len(comparison.Cases) != 1 {
		t.Fatal(err)
	}
}

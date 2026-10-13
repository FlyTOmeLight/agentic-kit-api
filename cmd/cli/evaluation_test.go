package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestResumeAfterAcceptedSubmissionDoesNotCreateSecondTrial(t *testing.T) {
	dir := t.TempDir()
	suite := sdk.EvaluationSuite{
		SchemaVersion: 1,
		Name:          "writing",
		Version:       "1",
		Target: sdk.EvaluationTarget{
			Kind:         "Agentic",
			AgentID:      "agent",
			AgentVersion: 2,
			Capabilities: []string{"output"},
		},
		Scenario: sdk.EvaluationScenario{Kind: "single_turn", TimeoutSeconds: 30},
		Dataset: sdk.EvaluationDataset{
			Name:    "cases",
			Version: "1",
			Cases:   []sdk.EvaluationCase{{ID: "a", Input: "write"}},
		},
		Evaluators: []sdk.EvaluatorSpec{
			{ID: "json", Version: "1", Kind: "json_valid", Scope: "output", Requires: []string{"output"}},
		},
		Metrics: []sdk.EvaluationMetric{
			{ID: "valid", Evaluator: "json", Aggregate: "pass_rate", MinimumScore: 1, MinimumCoverage: 1},
		},
	}
	b, _ := json.Marshal(suite)
	suitePath := filepath.Join(dir, "suite.json")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(suitePath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	submissions := 0
	firstKey := ""
	runtime := sdk.Run{
		ID:            "run",
		AgentID:       "agent",
		ConfigVersion: "2",
		SessionID:     "session",
		Status:        sdk.Completed,
		Output:        `{"ok":true}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/v1/executions":
			submissions++
			var req sdk.SubmitExecutionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Spec.AgentVersion != 2 {
				t.Error("target version changed")
			}
			if submissions == 1 {
				firstKey = r.Header.Get("Idempotency-Key")
				http.Error(w, `{"code":"Temporary","message":"accepted response lost"}`, 502)
				return
			}
			if firstKey == "" || firstKey != r.Header.Get("Idempotency-Key") {
				t.Error("resubmission did not reuse trial key")
			}
			json.NewEncoder(w).Encode(runtime)
		case "/v1/runs/run":
			json.NewEncoder(w).Encode(runtime)
		case "/v1/sessions/session/messages":
			json.NewEncoder(w).Encode(sdk.Page[sdk.SessionMessage]{Items: []sdk.SessionMessage{}})
		case "/v1/runs/run/trace":
			json.NewEncoder(w).Encode(sdk.RunTrace{Run: runtime})
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	t.Setenv("AGENTIC_URL", server.URL)
	t.Setenv("AGENTIC_TOKEN", "test-token")
	if _, err := run(context.Background(), suitePath, statePath); err == nil {
		t.Fatal("lost response should leave incomplete checkpoint")
	}
	passed, err := run(context.Background(), suitePath, statePath)
	if err != nil || !passed {
		t.Fatalf("resume: passed=%t err=%v", passed, err)
	}
	passed, err = run(context.Background(), suitePath, statePath)
	if err != nil || !passed || submissions != 2 {
		t.Fatalf("completed checkpoint reran target: submissions=%d err=%v", submissions, err)
	}
	suite.Version = "2"
	b, _ = json.Marshal(suite)
	os.WriteFile(suitePath, b, 0o600)
	if _, err = run(context.Background(), suitePath, statePath); err == nil {
		t.Fatal("changed suite reused old state")
	}
}

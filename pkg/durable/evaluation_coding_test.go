package durable

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type codingTestEnvironment struct {
	ready, verify bool
	exit          int
	stopped       map[string]bool
}

func (e *codingTestEnvironment) Filesystem(id string) *sdk.FilesystemBinding {
	return &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       "http://coding/" + id,
		TokenEnv:  "CODING_TOOLS_TOKEN",
		Workspace: "/workspace/repo",
	}
}

func (e *codingTestEnvironment) Advance(
	_ context.Context,
	id string,
	_ sdk.EvaluationScope,
	s sdk.CodingEvaluationScenario,
	phase string,
) (*sdk.CodingEvaluationEvidence, error) {
	v := &sdk.CodingEvaluationEvidence{Status: "Preparing", BaseHead: s.BaseHead}
	switch phase {
	case "prepare":
		if e.ready {
			v.Status = "Ready"
		}
	case "verify":
		v.Status = "Verifying"
		if e.verify {
			v.Status = "Completed"
			v.Stopped = true
			v.Patch = "real patch"
			v.Checks = []sdk.CodingEvaluationCheck{{Command: s.Checks[0], ExitCode: e.exit}}
		}
	case "stop":
		v.Status = "Stopped"
		v.Stopped = true
		e.stopped[id] = true
	}
	return v, nil
}

func codingRequest(t *testing.T, s *Store) sdk.StartEvaluationRequest {
	req := evaluationRequest(t, s)
	a, err := s.Agent(t.Context(), "owner", req.Suite.Target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	a.Draft.Toolsets = []string{"filesystem"}
	a, err = s.UpdateAgent(t.Context(), "owner", a.ID, a.Name, a.Revision, a.Draft)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.PublishAgent(t.Context(), "owner", a.ID, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	req.Suite.Target.AgentVersion = v.Number
	req.Suite.Target.Capabilities = []string{"output", "filesystem", "checks"}
	req.Suite.Scenario.Kind = "coding"
	req.Suite.Scenario.Coding = &sdk.CodingEvaluationScenario{
		BaseHead:     strings.Repeat("a", 40),
		AllowedPaths: []string{"calc.py"},
		Checks:       []string{"python3 -m unittest"},
	}
	req.Suite.Evaluators = []sdk.EvaluatorSpec{
		{ID: "tests", Version: "1", Kind: "checks_passed", Scope: "checks", Requires: []string{"checks"}},
	}
	req.Suite.Metrics[0].Evaluator = "tests"
	return req
}

func TestCodingEvaluationIndependentEvidenceAndRecovery(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	req := codingRequest(t, s)
	out, _, err := s.StartEvaluation(ctx, "owner", "coding", req)
	if err != nil {
		t.Fatal(err)
	}
	env := &codingTestEnvironment{stopped: map[string]bool{}}
	advance := func() {
		t.Helper()
		if err := s.ReconcileCodingEvaluations(ctx, env); err != nil {
			t.Fatal(err)
		}
		if err := s.ReconcileEvaluation(ctx, out.ID); err != nil {
			t.Fatal(err)
		}
	}
	advance()
	for _, trial := range out.Trials {
		if trial.RunID != "" {
			t.Fatal("Run scheduled before independent environment ready")
		}
	}
	// Repeat migration with both existing Writing data and pending Coding trials.
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	env.ready = true
	advance()
	out, err = s.Evaluation(ctx, "owner", out.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := []string{out.Trials[0].RunID, out.Trials[1].RunID}
	if firstIDs[0] == firstIDs[1] || firstIDs[0] == "" {
		t.Fatal("case isolation missing")
	}
	advance()
	for i := 0; i < 2; i++ {
		run := claim(t, s, "worker")
		trialID := ""
		for _, tr := range out.Trials {
			if tr.RunID == run.ID {
				trialID = tr.ID
			}
		}
		allowed, err := s.CheckCodingEvaluationLease(ctx, trialID, run.ID, run.Epoch)
		if err != nil || !allowed {
			t.Fatal("lease", allowed, err)
		}
		if allowed, _ = s.CheckCodingEvaluationLease(ctx, trialID, "foreign", run.Epoch); allowed {
			t.Fatal("foreign run accepted")
		}
		if err = s.Save(
			ctx,
			run.Lease(),
			json.RawMessage(`{}`),
			Completed,
			"all tests passed",
			"",
			"RunCompleted",
		); err != nil {
			t.Fatal(err)
		}
	}
	advance()
	out, _ = s.Evaluation(ctx, "owner", out.ID)
	if out.Status == "Completed" || out.Report.Passed {
		t.Fatal("Agent self report completed evaluation without verifier")
	}
	env.verify = true
	env.exit = 1
	advance()
	out, _ = s.Evaluation(ctx, "owner", out.ID)
	if out.Status != "Completed" || out.Report.Passed || out.Report.Metrics[0].Coverage != 1 ||
		*out.Report.Metrics[0].Value != 0 {
		t.Fatalf("independent exit code ignored: %+v", out.Report)
	}
	for i, tr := range out.Trials {
		if tr.RunID != firstIDs[i] || !tr.Coding.Stopped {
			t.Fatal("retry or cleanup changed")
		}
	}
}

func TestCodingEvaluationCancelBeforeAndAfterPreparation(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "preparing", true: "running"}[prepare], func(t *testing.T) {
			s := testStore(t)
			ctx := t.Context()
			req := codingRequest(t, s)
			out, _, err := s.StartEvaluation(ctx, "owner", "cancel", req)
			if err != nil {
				t.Fatal(err)
			}
			env := &codingTestEnvironment{ready: prepare, stopped: map[string]bool{}}
			if err = s.ReconcileCodingEvaluations(ctx, env); err != nil {
				t.Fatal(err)
			}
			if err = s.CancelEvaluation(ctx, "owner", out.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.ReconcileCodingEvaluations(ctx, env); err != nil {
				t.Fatal(err)
			}
			got, err := s.Evaluation(ctx, "owner", out.ID)
			if err != nil || got.Status != "Canceled" || got.Report.Passed || len(env.stopped) != 2 {
				t.Fatal("cancel", got, err)
			}
			for _, tr := range got.Trials {
				if !tr.Coding.Stopped || (tr.Run != nil && tr.Run.Status != Canceled) {
					t.Fatal("unconfirmed cleanup", tr)
				}
			}
		})
	}
}

package evaluation

import (
	"testing"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func suite() sdk.EvaluationSuite {
	return sdk.EvaluationSuite{
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
			Cases:   []sdk.EvaluationCase{{ID: "a", Input: "write"}, {ID: "b", Input: "rewrite"}},
		},
		Evaluators: []sdk.EvaluatorSpec{
			{
				ID:       "requirements",
				Version:  "1",
				Kind:     "contains_all",
				Scope:    "output",
				Requires: []string{"output"},
				Values:   []string{"42"},
			},
		},
		Metrics: []sdk.EvaluationMetric{
			{
				ID:              "success",
				Evaluator:       "requirements",
				Aggregate:       "pass_rate",
				MinimumScore:    0.5,
				MinimumCoverage: 1,
			},
		},
	}
}

func observation(id string, status sdk.Status, complete bool, output string) sdk.EvaluationObservation {
	return sdk.EvaluationObservation{
		TrialID:          "trial-" + id,
		CaseID:           id,
		Run:              sdk.Run{AgentID: "agent", ConfigVersion: "2", Status: status, Output: output},
		EvidenceComplete: complete,
	}
}

func TestScheduledCasesAndCoverage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		observations []sdk.EvaluationObservation
		scored       int
		value        float64
		passed       bool
	}{
		{name: "complete", observations: []sdk.EvaluationObservation{observation("a", sdk.Completed, true, "42"), observation("b", sdk.Completed, true, "wrong")}, scored: 2, value: 0.5, passed: true},
		{name: "missing", observations: []sdk.EvaluationObservation{observation("a", sdk.Completed, true, "42")}, scored: 1, value: 0.5, passed: false},
		{name: "truncated", observations: []sdk.EvaluationObservation{observation("a", sdk.Completed, true, "42"), observation("b", sdk.Completed, false, "42")}, scored: 1, value: 0.5, passed: false},
		{name: "execution_error", observations: []sdk.EvaluationObservation{observation("a", sdk.Completed, true, "42"), observation("b", sdk.Failed, true, "42")}, scored: 1, value: 0.5, passed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Evaluate(suite(), tc.observations)
			if err != nil {
				t.Fatal(err)
			}
			m := r.Metrics[0]
			if m.Scored != tc.scored || m.Value == nil || *m.Value != tc.value || r.Passed != tc.passed {
				t.Fatalf("unexpected result: %+v", r)
			}
		})
	}
}

func TestEmptyEvidenceCannotPassZeroThreshold(t *testing.T) {
	s := suite()
	s.Metrics[0].MinimumScore = 0
	r, err := Evaluate(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Passed || r.Metrics[0].Value != nil {
		t.Fatal("empty evidence passed")
	}
}

func TestPreflightRejectsUnsupportedOrInconsistentConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sdk.EvaluationSuite)
	}{
		{name: "unknown_kind", change: func(s *sdk.EvaluationSuite) { s.Evaluators[0].Kind = "made_up" }},
		{name: "handoff_evidence", change: func(s *sdk.EvaluationSuite) { s.Evaluators[0].Requires = []string{"handoff"} }},
		{name: "duplicate_case", change: func(s *sdk.EvaluationSuite) { s.Dataset.Cases[1].ID = "a" }},
		{name: "unknown_metric_source", change: func(s *sdk.EvaluationSuite) { s.Metrics[0].Evaluator = "missing" }},
		{name: "zero_coverage", change: func(s *sdk.EvaluationSuite) { s.Metrics[0].MinimumCoverage = 0 }},
		{name: "empty_contains", change: func(s *sdk.EvaluationSuite) { s.Evaluators[0].Values = []string{""} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := suite()
			tc.change(&s)
			if Validate(s) == nil {
				t.Fatal("invalid suite accepted")
			}
		})
	}
}

func TestVersionAndDuplicateEvidenceRejected(t *testing.T) {
	o := observation("a", sdk.Completed, true, "42")
	o.Run.ConfigVersion = "3"
	if _, err := Evaluate(suite(), []sdk.EvaluationObservation{o}); err == nil {
		t.Fatal("different target scored")
	}
	o = observation("a", sdk.Completed, true, "42")
	if _, err := Evaluate(suite(), []sdk.EvaluationObservation{o, o}); err == nil {
		t.Fatal("duplicate evidence scored")
	}
}

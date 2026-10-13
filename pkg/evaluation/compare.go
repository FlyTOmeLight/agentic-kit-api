package evaluation

import (
	"encoding/json"
	"errors"
	"reflect"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// Compare keeps the dataset, scenario, evaluator versions and gates identical.
// Target identity and suite display identity can differ across candidate versions.
func Compare(b, c sdk.EvaluationExperiment) (*sdk.EvaluationComparison, error) {
	if b.Scope.TenantID != c.Scope.TenantID || b.Scope.ProjectID != c.Scope.ProjectID {
		return nil, errors.New("experiments belong to different projects")
	}
	left, right := b.Suite, c.Suite
	left.Name, right.Name = "", ""
	left.Version, right.Version = "", ""
	left.Target.AgentID, right.Target.AgentID = "", ""
	left.Target.AgentVersion, right.Target.AgentVersion = 0, 0
	lb, le := json.Marshal(left)
	rb, re := json.Marshal(right)
	if le != nil || re != nil || !reflect.DeepEqual(lb, rb) {
		return nil, errors.New("dataset, scenario, evaluator versions and metrics must match")
	}
	if b.Report == nil || c.Report == nil {
		return nil, errors.New("evaluation report not available")
	}
	out := &sdk.EvaluationComparison{BaselineID: b.ID, CandidateID: c.ID}
	bm := map[string]sdk.EvaluationMetricResult{}
	bs := map[string]sdk.EvaluationScore{}
	for _, m := range b.Report.Metrics {
		bm[m.ID] = m
	}
	for _, s := range b.Report.Scores {
		bs[s.CaseID+"\x00"+s.Evaluator] = s
	}
	for _, m := range c.Report.Metrics {
		baseline, ok := bm[m.ID]
		if !ok {
			return nil, errors.New("baseline metric not available")
		}
		item := sdk.EvaluationMetricComparison{ID: m.ID, Baseline: baseline, Candidate: m}
		if baseline.Value != nil && m.Value != nil {
			delta := *m.Value - *baseline.Value
			item.Delta = &delta
		}
		out.Metrics = append(out.Metrics, item)
	}
	for _, s := range c.Report.Scores {
		baseline, ok := bs[s.CaseID+"\x00"+s.Evaluator]
		if !ok {
			return nil, errors.New("baseline case not available")
		}
		out.Cases = append(
			out.Cases,
			sdk.EvaluationCaseComparison{
				CaseID:    s.CaseID,
				Evaluator: s.Evaluator,
				Baseline:  baseline,
				Candidate: s,
			},
		)
	}
	return out, nil
}

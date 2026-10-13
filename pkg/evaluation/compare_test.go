package evaluation

import (
	"testing"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestPairedComparison(t *testing.T) {
	b := sdk.EvaluationExperiment{
		ID:    "baseline",
		Scope: sdk.EvaluationScope{TenantID: "t", ProjectID: "p"},
		Suite: suite(),
	}
	c := b
	c.ID = "candidate"
	var err error
	b.Report, err = Evaluate(
		b.Suite,
		[]sdk.EvaluationObservation{
			observation("a", sdk.Completed, true, "wrong"),
			observation("b", sdk.Completed, true, "wrong"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	c.Report, err = Evaluate(
		c.Suite,
		[]sdk.EvaluationObservation{
			observation("a", sdk.Completed, true, "42"),
			observation("b", sdk.Failed, false, ""),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	c.Suite.Target.AgentVersion = 3
	c.Suite.Name = "candidate"
	out, err := Compare(b, c)
	if err != nil || len(out.Cases) != 2 || out.Metrics[0].Delta == nil || *out.Metrics[0].Delta != 0.5 ||
		out.Cases[1].Candidate.Status != "ExecutionError" {
		t.Fatalf("comparison %+v %v", out, err)
	}
	c.Scope.ProjectID = "other"
	if _, err = Compare(b, c); err == nil {
		t.Fatal("cross-project accepted")
	}
	c.Scope = b.Scope
	c.Suite.Dataset.Version = "2"
	if _, err = Compare(b, c); err == nil {
		t.Fatal("different dataset accepted")
	}
}

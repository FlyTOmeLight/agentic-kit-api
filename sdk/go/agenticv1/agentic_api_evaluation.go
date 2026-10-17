package agenticv1

import "time"

// EvaluationSuite is the frozen input to an experiment. Its version changes
// when cases, target, evaluators, metrics, or gates change.
type EvaluationSuite struct {
	SchemaVersion int                `json:"schemaVersion"`
	Name          string             `json:"name"`
	Version       string             `json:"version"`
	Target        EvaluationTarget   `json:"target"`
	Scenario      EvaluationScenario `json:"scenario"`
	Dataset       EvaluationDataset  `json:"dataset"`
	Evaluators    []EvaluatorSpec    `json:"evaluators"`
	Metrics       []EvaluationMetric `json:"metrics"`
}

type EvaluationTarget struct {
	Kind         string   `json:"kind"`
	AgentID      string   `json:"agentID"`
	AgentVersion int64    `json:"agentVersion"`
	Capabilities []string `json:"capabilities"`
}

type EvaluationScenario struct {
	Kind           string                    `json:"kind"`
	TimeoutSeconds int                       `json:"timeoutSeconds"`
	Coding         *CodingEvaluationScenario `json:"coding,omitempty"`
	Filesystem     *FilesystemBinding        `json:"filesystem,omitempty"`
}

type EvaluationDataset struct {
	ID      string           `json:"id,omitempty"`
	Name    string           `json:"name"`
	Version string           `json:"version"`
	Cases   []EvaluationCase `json:"cases"`
}

type EvaluationCase struct {
	ID       string         `json:"id"`
	Input    string         `json:"input"`
	Expected string         `json:"expected,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// The first evaluator implementation supports explicit output checks.
// Unknown kinds and scopes are rejected, never silently approximated.
type EvaluatorSpec struct {
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	Kind     string   `json:"kind"`
	Scope    string   `json:"scope"`
	Requires []string `json:"requires"`
	Values   []string `json:"values,omitempty"`
}

type EvaluationMetric struct {
	ID              string  `json:"id"`
	Evaluator       string  `json:"evaluator"`
	Aggregate       string  `json:"aggregate"`
	MinimumScore    float64 `json:"minimumScore"`
	MinimumCoverage float64 `json:"minimumCoverage"`
}

type EvaluationObservation struct {
	TrialID          string                    `json:"trialID"`
	CaseID           string                    `json:"caseID"`
	Run              Run                       `json:"run"`
	Messages         []SessionMessage          `json:"messages,omitempty"`
	Events           []Event                   `json:"events,omitempty"`
	Coding           *CodingEvaluationEvidence `json:"coding,omitempty"`
	EvidenceComplete bool                      `json:"evidenceComplete"`
}

type EvaluationScore struct {
	TrialID   string   `json:"trialID"`
	CaseID    string   `json:"caseID"`
	Evaluator string   `json:"evaluator"`
	Status    string   `json:"status"`
	Value     *float64 `json:"value,omitempty"`
	Reason    string   `json:"reason"`
}

type EvaluationMetricResult struct {
	ID       string   `json:"id"`
	Planned  int      `json:"planned"`
	Scored   int      `json:"scored"`
	Coverage float64  `json:"coverage"`
	Value    *float64 `json:"value,omitempty"`
	Passed   bool     `json:"passed"`
}

type EvaluationReport struct {
	SuiteDigest string                   `json:"suiteDigest"`
	Scores      []EvaluationScore        `json:"scores"`
	Metrics     []EvaluationMetricResult `json:"metrics"`
	Passed      bool                     `json:"passed"`
}

// StartEvaluationRequest carries a complete frozen suite. Scope identity is
// supplied by a trusted application, never inferred from an Agent prompt.
type StartEvaluationRequest struct {
	IdempotencyKey string `json:"-" form:"-" uri:"-" header:"Idempotency-Key" binding:"omitempty,max=128"`

	Suite EvaluationSuite `json:"suite"`
	Scope EvaluationScope `json:"scope"`
}

type EvaluationScope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
}

type EvaluationTrial struct {
	ID     string                    `json:"id"`
	CaseID string                    `json:"caseID"`
	RunID  string                    `json:"runID"`
	Coding *CodingEvaluationEvidence `json:"coding,omitempty"`
	Run    *Run                      `json:"run,omitempty"`
}

type EvaluationExperiment struct {
	ID          string            `json:"id"`
	Scope       EvaluationScope   `json:"scope"`
	Status      string            `json:"status"`
	Suite       EvaluationSuite   `json:"suite"`
	SuiteDigest string            `json:"suiteDigest"`
	Trials      []EvaluationTrial `json:"trials"`
	Report      *EvaluationReport `json:"report,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

type ListEvaluationsOptions struct {
	// Non-nil restricts repository-bound experiments to these authorized projects.
	AllowedProjectIDs []string

	PageOptions
	TenantID  string `json:"-" form:"tenantID"`
	ProjectID string `json:"-" form:"projectID"`
}

type EvaluationMetricComparison struct {
	ID        string                 `json:"id"`
	Baseline  EvaluationMetricResult `json:"baseline"`
	Candidate EvaluationMetricResult `json:"candidate"`
	Delta     *float64               `json:"delta,omitempty"`
}

type EvaluationCaseComparison struct {
	CaseID    string          `json:"caseID"`
	Evaluator string          `json:"evaluator"`
	Baseline  EvaluationScore `json:"baseline"`
	Candidate EvaluationScore `json:"candidate"`
}

type EvaluationComparison struct {
	BaselineID  string                       `json:"baselineID"`
	CandidateID string                       `json:"candidateID"`
	Metrics     []EvaluationMetricComparison `json:"metrics"`
	Cases       []EvaluationCaseComparison   `json:"cases"`
}

// CodingEvaluationScenario fixes repository inputs before scheduling any trial.
// Repository identity comes from the authenticated project, never a clone URL.
type CodingEvaluationScenario struct {
	BaseHead     string   `json:"baseHead"`
	AllowedPaths []string `json:"allowedPaths"`
	Checks       []string `json:"checks"`
}

type CodingEvaluationCheck struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	Log      string `json:"log"`
}

type CodingEvaluationEvidence struct {
	Status   string                  `json:"status"`
	BaseHead string                  `json:"baseHead"`
	Patch    string                  `json:"patch,omitempty"`
	Changed  []string                `json:"changed,omitempty"`
	Checks   []CodingEvaluationCheck `json:"checks,omitempty"`
	Error    string                  `json:"error,omitempty"`
	Stopped  bool                    `json:"stopped"`
}

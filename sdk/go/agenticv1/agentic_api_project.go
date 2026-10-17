package agenticv1

import "time"

// Project endpoints authenticate a Forge session, not a runtime bearer token.
// Connection addresses and credential references never enter these DTOs.
type ProjectAgent struct {
	Kind             string   `json:"kind"`
	Tools            []string `json:"tools"`
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Revision         int64    `json:"revision"`
	CandidateVersion int64    `json:"candidateVersion"`
	Prompt           string   `json:"prompt"`
	MaxTurns         int      `json:"maxTurns"`
	Model            string   `json:"model"`
}

type ProjectAgentVersion struct {
	AgentID  string `json:"agentID"`
	Number   int64  `json:"number"`
	Prompt   string `json:"prompt"`
	MaxTurns int    `json:"maxTurns"`
	Model    string `json:"model"`
}

type ProjectAgentList struct {
	Items   []ProjectAgent `json:"items"`
	CanEdit bool           `json:"canEdit"`
}

type UpdateProjectAgentRequest struct {
	ID string `json:"-" uri:"agent" binding:"required,max=128"`

	Name     string `json:"name" binding:"required,max=200"`
	Revision int64  `json:"revision" binding:"min=1"`
	Prompt   string `json:"prompt"`
	MaxTurns int    `json:"maxTurns"`
}

type StartProjectEvaluationRequest struct {
	IdempotencyKey string `json:"-" form:"-" uri:"-" header:"Idempotency-Key" binding:"omitempty,max=128"`

	ProjectID string          `json:"projectID,omitempty"`
	Suite     EvaluationSuite `json:"suite"`
}

// EvaluationPlan is editable. Runs keep their own immutable suite snapshot.
type EvaluationPlan struct {
	ProjectID string          `json:"projectID,omitempty"`
	ID        string          `json:"id"`
	Revision  int64           `json:"revision"`
	Suite     EvaluationSuite `json:"suite"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type SaveEvaluationPlanRequest struct {
	ID string `json:"-" uri:"plan" binding:"required,max=128"`

	ProjectID string          `json:"projectID,omitempty"`
	Revision  int64           `json:"revision" binding:"min=0"`
	Suite     EvaluationSuite `json:"suite"`
}

type StartProjectTrialRequest struct {
	AgentID        string `json:"-" uri:"agent" binding:"required,max=128"`
	IdempotencyKey string `json:"-" form:"-" uri:"-" header:"Idempotency-Key" binding:"omitempty,max=100"`

	AgentVersion   int64  `json:"agentVersion" binding:"min=1"`
	Input          string `json:"input" binding:"required"`
	TimeoutSeconds int    `json:"timeoutSeconds" binding:"min=1,max=3600"`
}

type ProjectTrial struct {
	Run   Run    `json:"run"`
	Input string `json:"input"`
}

// CodingReleaseRequest is sent only by the trusted Coding service.
type CodingReleaseRequest struct {
	TenantID     string `json:"tenantID" binding:"required"`
	ProjectID    string `json:"projectID" binding:"required"`
	ActorID      string `json:"actorID" binding:"required"`
	ExperimentID string `json:"experimentID" binding:"required"`
	AgentID      string `json:"agentID" binding:"required"`
	AgentVersion int64  `json:"agentVersion" binding:"min=1"`
}

type CodingReleaseApproval struct {
	ExperimentID   string                   `json:"experimentID"`
	SuiteDigest    string                   `json:"suiteDigest"`
	AgentID        string                   `json:"agentID"`
	AgentVersion   int64                    `json:"agentVersion"`
	RuntimeAgentID string                   `json:"runtimeAgentID"`
	RuntimeVersion int64                    `json:"runtimeVersion"`
	Model          string                   `json:"model"`
	Scenario       CodingEvaluationScenario `json:"scenario"`
}

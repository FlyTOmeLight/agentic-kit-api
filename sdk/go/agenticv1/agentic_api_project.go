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

type ProjectTemplate struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Model    string   `json:"model"`
	Version  int64    `json:"version"`
	Prompt   string   `json:"prompt"`
	MaxTurns int      `json:"maxTurns"`
	Tools    []string `json:"tools"`
}

type ProjectAgentList struct {
	Items     []ProjectAgent    `json:"items"`
	Templates []ProjectTemplate `json:"templates"`
	CanEdit   bool              `json:"canEdit"`
}

type CreateProjectAgentRequest struct {
	TemplateID string `json:"templateID"`
	Name       string `json:"name"`
}

type UpdateProjectAgentRequest struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Prompt   string `json:"prompt"`
	MaxTurns int    `json:"maxTurns"`
}

type StartProjectEvaluationRequest struct {
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
	ProjectID string          `json:"projectID,omitempty"`
	Revision  int64           `json:"revision"`
	Suite     EvaluationSuite `json:"suite"`
}

type StartProjectTrialRequest struct {
	AgentVersion   int64  `json:"agentVersion"`
	Input          string `json:"input"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type ProjectTrial struct {
	Run   Run    `json:"run"`
	Input string `json:"input"`
}

// CodingReleaseRequest is sent only by the trusted Coding service.
type CodingReleaseRequest struct {
	TenantID     string `json:"tenantID"`
	ProjectID    string `json:"projectID"`
	ActorID      string `json:"actorID"`
	ExperimentID string `json:"experimentID"`
	AgentID      string `json:"agentID"`
	AgentVersion int64  `json:"agentVersion"`
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

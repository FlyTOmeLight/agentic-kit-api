package sdk

// Scope is asserted by a trusted application from its verified identity and task.
type Scope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
	TaskID    string `json:"taskID"`
	RoundID   string `json:"roundID"`
}

// ExecutionSpec references an immutable published Agent. Applications supply
// business input and scope; they cannot replace its model, prompt or tools.
type ExecutionSpec struct {
	Filesystem   *FilesystemBinding `json:"filesystem,omitempty"`
	Scope        *Scope             `json:"scope"`
	AgentID      string             `json:"agentID"`
	AgentVersion int64              `json:"agentVersion"`
	Query        string             `json:"query"`
}
type SubmitExecutionRequest struct {
	Spec           ExecutionSpec `json:"spec"`
	TimeoutSeconds int           `json:"timeoutSeconds"`
}
type LeaseCheck struct {
	Allowed bool `json:"allowed"`
}

// FilesystemBinding is accepted only through the trusted execution endpoint.
// Credentials are server-side references, never values in published Agent config.
type FilesystemBinding struct {
	Provider  string `json:"provider"`
	URL       string `json:"url"`
	TokenEnv  string `json:"tokenEnv"`
	Workspace string `json:"workspace"`
}

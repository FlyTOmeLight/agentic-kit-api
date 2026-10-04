package sdk

// Scope is asserted by a trusted application from its verified identity and task.
type Scope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
	TaskID    string `json:"taskID"`
	RoundID   string `json:"roundID"`
}
type ExecutionSpec struct {
	Scope        *Scope     `json:"scope"`
	Query        string     `json:"query"`
	SystemPrompt string     `json:"systemPrompt"`
	Model        ModelSpec  `json:"model"`
	Tools        []ToolSpec `json:"tools,omitempty"`
	MaxTurns     int        `json:"maxTurns"`
}
type SubmitExecutionRequest struct {
	Spec           ExecutionSpec `json:"spec"`
	TimeoutSeconds int           `json:"timeoutSeconds"`
}
type LeaseCheck struct {
	Allowed bool `json:"allowed"`
}

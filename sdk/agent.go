package sdk

import "time"

type AgentConfig struct {
	SystemPrompt string     `json:"systemPrompt"`
	Model        ModelSpec  `json:"model"`
	Tools        []ToolSpec `json:"tools,omitempty"`
	MaxTurns     int        `json:"maxTurns"`
}
type Agent struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Draft            AgentConfig `json:"draft"`
	Revision         int64       `json:"revision"`
	PublishedVersion int64       `json:"publishedVersion"`
	CreatedAt        time.Time   `json:"createdAt"`
	UpdatedAt        time.Time   `json:"updatedAt"`
}
type AgentVersion struct {
	AgentID   string      `json:"agentID"`
	Number    int64       `json:"number"`
	Config    AgentConfig `json:"config"`
	CreatedAt time.Time   `json:"createdAt"`
}

type CreateAgentRequest struct {
	Name   string      `json:"name"`
	Config AgentConfig `json:"config"`
}
type UpdateAgentRequest struct {
	Name     string      `json:"name"`
	Revision int64       `json:"revision"`
	Config   AgentConfig `json:"config"`
}
type PublishAgentRequest struct {
	Revision int64 `json:"revision"`
}

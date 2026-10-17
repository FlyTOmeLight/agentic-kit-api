package agenticv1

import "time"

type AgentConfig struct {
	Toolsets     []string   `json:"toolsets,omitempty"`
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
	Name   string      `json:"name" binding:"required,max=200"`
	Config AgentConfig `json:"config"`
}

type UpdateAgentRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`

	Name     string      `json:"name" binding:"required,max=200"`
	Revision int64       `json:"revision" binding:"min=1"`
	Config   AgentConfig `json:"config"`
}

type PublishAgentRequest struct {
	ID string `json:"-" uri:"id" binding:"required,max=128"`

	Revision int64 `json:"revision" binding:"min=1"`
}

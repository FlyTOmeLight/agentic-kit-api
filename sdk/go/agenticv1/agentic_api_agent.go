package agenticv1

import "github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"

type AgentConfig = agent.AgentConfig

type Agent = agent.Agent

type AgentVersion = agent.AgentVersion

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

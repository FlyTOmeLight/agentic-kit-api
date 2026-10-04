package agent

import "github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"

type createAgentRequest struct {
	Name   string              `json:"name" binding:"required,max=200"`
	Config durable.AgentConfig `json:"config" binding:"required"`
}
type updateAgentRequest struct {
	Name     string              `json:"name" binding:"required,max=200"`
	Revision int64               `json:"revision" binding:"required,min=1"`
	Config   durable.AgentConfig `json:"config" binding:"required"`
}

type publishRequest struct {
	Revision int64 `json:"revision" binding:"required,min=1"`
}

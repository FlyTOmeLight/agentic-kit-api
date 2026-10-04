package agent

import (
	"context"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// Service is the transactional behavior required by this HTTP module.
type Service interface {
	CreateAgent(ctx context.Context, owner, name string, config durable.AgentConfig) (*durable.Agent, error)
	Agent(ctx context.Context, owner, id string) (*durable.Agent, error)
	Agents(ctx context.Context, owner, after string, limit int) ([]durable.Agent, error)
	UpdateAgent(ctx context.Context, owner, id, name string, revision int64, config durable.AgentConfig) (*durable.Agent, error)
	PublishAgent(ctx context.Context, owner, id string, revision int64) (*durable.AgentVersion, error)
	AgentVersion(ctx context.Context, owner, id string, number int64) (*durable.AgentVersion, error)
}

var _ Service = (*durable.Store)(nil)

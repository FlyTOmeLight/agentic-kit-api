package sdk

import "context"

// AgentAPI contains the agent operations exposed over HTTP.
type AgentAPI interface {
	Create(ctx context.Context, req CreateAgentRequest) (*Agent, error)
	Get(ctx context.Context, id string) (*Agent, error)
	List(ctx context.Context, opts PageOptions) (*Page[Agent], error)
	Update(ctx context.Context, id string, req UpdateAgentRequest) (*Agent, error)
	Publish(ctx context.Context, id string, req PublishAgentRequest) (*AgentVersion, error)
	GetVersion(ctx context.Context, id string, number int64) (*AgentVersion, error)
}

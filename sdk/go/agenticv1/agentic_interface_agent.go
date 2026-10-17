package agenticv1

import "context"

// AgentAPI contains the agent operations exposed over HTTP.
type AgentAPI interface {
	Create(ctx context.Context, req *CreateAgentRequest) (*Agent, error)
	Get(ctx context.Context, req *GetAgentRequest) (*Agent, error)
	List(ctx context.Context, req *ListAgentsRequest) (*Page[Agent], error)
	Update(ctx context.Context, req *UpdateAgentRequest) (*Agent, error)
	Publish(ctx context.Context, req *PublishAgentRequest) (*AgentVersion, error)
	GetVersion(ctx context.Context, req *GetAgentVersionRequest) (*AgentVersion, error)
}

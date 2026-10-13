package agenticv1

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type agentClient struct{ transport *transport }

var _ AgentAPI = (*agentClient)(nil)

func (c *agentClient) Create(ctx context.Context, req CreateAgentRequest) (*Agent, error) {
	return jsonResult[Agent](ctx, c.transport, http.MethodPost, "/v1/agents", req, http.StatusCreated)
}

func (c *agentClient) Get(ctx context.Context, id string) (*Agent, error) {
	return jsonResult[Agent](ctx, c.transport, http.MethodGet, "/v1/agents/"+url.PathEscape(id), nil, http.StatusOK)
}

func (c *agentClient) List(ctx context.Context, opts PageOptions) (*Page[Agent], error) {
	return jsonResult[Page[Agent]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/agents", opts.query()),
		nil,
		http.StatusOK,
	)
}

func (c *agentClient) Update(ctx context.Context, id string, req UpdateAgentRequest) (*Agent, error) {
	return jsonResult[Agent](ctx, c.transport, http.MethodPut, "/v1/agents/"+url.PathEscape(id), req, http.StatusOK)
}

func (c *agentClient) Publish(ctx context.Context, id string, req PublishAgentRequest) (*AgentVersion, error) {
	return jsonResult[AgentVersion](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/agents/"+url.PathEscape(id)+"/publish",
		req,
		http.StatusOK,
	)
}

func (c *agentClient) GetVersion(ctx context.Context, id string, number int64) (*AgentVersion, error) {
	return jsonResult[AgentVersion](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/agents/"+url.PathEscape(id)+"/versions/"+strconv.FormatInt(number, 10),
		nil,
		http.StatusOK,
	)
}

package agenticv1

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type agentClient struct{ transport *transport }

var _ AgentAPI = (*agentClient)(nil)

func (c *agentClient) Create(ctx context.Context, req *CreateAgentRequest) (*Agent, error) {
	return jsonResult[Agent](ctx, c.transport, http.MethodPost, "/v1/agents", req, http.StatusCreated)
}

func (c *agentClient) Get(ctx context.Context, req *GetAgentRequest) (*Agent, error) {
	return jsonResult[Agent](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/agents/"+url.PathEscape(req.ID),
		nil,
		http.StatusOK,
	)
}

func (c *agentClient) List(ctx context.Context, req *ListAgentsRequest) (*Page[Agent], error) {
	return jsonResult[Page[Agent]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/agents", req.PageOptions.query()),
		nil,
		http.StatusOK,
	)
}

func (c *agentClient) Update(ctx context.Context, req *UpdateAgentRequest) (*Agent, error) {
	return jsonResult[Agent](
		ctx,
		c.transport,
		http.MethodPut,
		"/v1/agents/"+url.PathEscape(req.ID),
		req,
		http.StatusOK,
	)
}

func (c *agentClient) Publish(ctx context.Context, req *PublishAgentRequest) (*AgentVersion, error) {
	return jsonResult[AgentVersion](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/agents/"+url.PathEscape(req.ID)+"/publish",
		req,
		http.StatusOK,
	)
}

func (c *agentClient) GetVersion(ctx context.Context, req *GetAgentVersionRequest) (*AgentVersion, error) {
	return jsonResult[AgentVersion](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/agents/"+url.PathEscape(req.ID)+"/versions/"+strconv.FormatInt(req.Number, 10),
		nil,
		http.StatusOK,
	)
}

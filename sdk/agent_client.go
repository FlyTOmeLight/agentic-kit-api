package sdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

func (c *Client) CreateAgent(ctx context.Context, req CreateAgentRequest) (*Agent, error) {
	return jsonResult[Agent](ctx, c, http.MethodPost, "/v1/agents", req, http.StatusCreated)
}
func (c *Client) GetAgent(ctx context.Context, id string) (*Agent, error) {
	return jsonResult[Agent](ctx, c, http.MethodGet, "/v1/agents/"+url.PathEscape(id), nil, http.StatusOK)
}
func (c *Client) ListAgents(ctx context.Context, opts PageOptions) (*Page[Agent], error) {
	return jsonResult[Page[Agent]](ctx, c, http.MethodGet, withQuery("/v1/agents", opts.query()), nil, http.StatusOK)
}
func (c *Client) UpdateAgent(ctx context.Context, id string, req UpdateAgentRequest) (*Agent, error) {
	return jsonResult[Agent](ctx, c, http.MethodPut, "/v1/agents/"+url.PathEscape(id), req, http.StatusOK)
}
func (c *Client) PublishAgent(ctx context.Context, id string, req PublishAgentRequest) (*AgentVersion, error) {
	return jsonResult[AgentVersion](ctx, c, http.MethodPost, "/v1/agents/"+url.PathEscape(id)+"/publish", req, http.StatusOK)
}
func (c *Client) GetAgentVersion(ctx context.Context, id string, number int64) (*AgentVersion, error) {
	return jsonResult[AgentVersion](ctx, c, http.MethodGet, "/v1/agents/"+url.PathEscape(id)+"/versions/"+strconv.FormatInt(number, 10), nil, http.StatusOK)
}

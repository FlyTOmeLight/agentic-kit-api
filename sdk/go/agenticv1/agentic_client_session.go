package agenticv1

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type sessionClient struct{ transport *transport }

var _ SessionAPI = (*sessionClient)(nil)

func (c *sessionClient) Create(ctx context.Context, req *CreateSessionRequest) (*Session, error) {
	return jsonResult[Session](ctx, c.transport, http.MethodPost, "/v1/sessions", req, http.StatusCreated)
}

func (c *sessionClient) Get(ctx context.Context, req *GetSessionRequest) (*Session, error) {
	return jsonResult[Session](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/sessions/"+url.PathEscape(req.ID),
		nil,
		http.StatusOK,
	)
}

func (c *sessionClient) List(ctx context.Context, req *ListSessionsRequest) (*Page[Session], error) {
	q := req.PageOptions.query()
	if req.AgentID != "" {
		q.Set("agentID", req.AgentID)
	}
	return jsonResult[Page[Session]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/sessions", q),
		nil,
		http.StatusOK,
	)
}

// SendMessage sets Created=false when the server replays an accepted request.
// Reuse the same key to retry the same input; use a new key for the next message.
func (c *sessionClient) SendMessage(ctx context.Context, req *SendMessageRequest) (*SendMessageResponse, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("idempotency key is required")
	}
	run, status, err := doJSON[Run](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/sessions/"+url.PathEscape(req.SessionID)+"/messages",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusAccepted,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return &SendMessageResponse{Run: *run, Created: status == http.StatusAccepted}, nil
}

func (c *sessionClient) ListMessages(ctx context.Context, req *ListMessagesRequest) (*Page[SessionMessage], error) {
	q := url.Values{}
	if req.After != 0 {
		q.Set("after", strconv.FormatInt(req.After, 10))
	}
	if req.Limit != 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	return jsonResult[Page[SessionMessage]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/sessions/"+url.PathEscape(req.SessionID)+"/messages", q),
		nil,
		http.StatusOK,
	)
}

func (c *sessionClient) ListRuns(ctx context.Context, req *ListSessionRunsRequest) (*Page[Run], error) {
	return jsonResult[Page[Run]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/sessions/"+url.PathEscape(req.SessionID)+"/runs", req.PageOptions.query()),
		nil,
		http.StatusOK,
	)
}

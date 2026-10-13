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

func (c *sessionClient) Create(ctx context.Context, req CreateSessionRequest) (*Session, error) {
	return jsonResult[Session](ctx, c.transport, http.MethodPost, "/v1/sessions", req, http.StatusCreated)
}

func (c *sessionClient) Get(ctx context.Context, id string) (*Session, error) {
	return jsonResult[Session](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/sessions/"+url.PathEscape(id),
		nil,
		http.StatusOK,
	)
}

func (c *sessionClient) List(ctx context.Context, opts ListSessionsOptions) (*Page[Session], error) {
	q := opts.PageOptions.query()
	if opts.AgentID != "" {
		q.Set("agentID", opts.AgentID)
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

// SendMessage returns created=false when the server replays an accepted request.
// Reuse the same key to retry the same input; use a new key for the next message.
func (c *sessionClient) SendMessage(
	ctx context.Context,
	sessionID, key string,
	req SendMessageRequest,
) (*Run, bool, error) {
	if strings.TrimSpace(key) == "" {
		return nil, false, errors.New("idempotency key is required")
	}
	run, status, err := doJSON[Run](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/sessions/"+url.PathEscape(sessionID)+"/messages",
		req,
		http.Header{"Idempotency-Key": {key}},
		http.StatusAccepted,
		http.StatusOK,
	)
	return run, status == http.StatusAccepted, err
}

func (c *sessionClient) ListMessages(
	ctx context.Context,
	sessionID string,
	opts ListMessagesOptions,
) (*Page[SessionMessage], error) {
	q := url.Values{}
	if opts.After != 0 {
		q.Set("after", strconv.FormatInt(opts.After, 10))
	}
	if opts.Limit != 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	return jsonResult[Page[SessionMessage]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/sessions/"+url.PathEscape(sessionID)+"/messages", q),
		nil,
		http.StatusOK,
	)
}

func (c *sessionClient) ListRuns(ctx context.Context, sessionID string, opts PageOptions) (*Page[Run], error) {
	return jsonResult[Page[Run]](
		ctx,
		c.transport,
		http.MethodGet,
		withQuery("/v1/sessions/"+url.PathEscape(sessionID)+"/runs", opts.query()),
		nil,
		http.StatusOK,
	)
}

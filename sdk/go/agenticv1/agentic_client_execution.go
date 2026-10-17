package agenticv1

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type executionClient struct{ transport *transport }

var _ ExecutionAPI = (*executionClient)(nil)

func (c *executionClient) Submit(ctx context.Context, req *SubmitExecutionRequest) (*SubmitExecutionResponse, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("idempotency key is required")
	}
	run, status, e := doJSON[Run](
		ctx,
		c.transport,
		http.MethodPost,
		"/internal/v1/executions",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusAccepted,
		http.StatusOK,
	)
	if e != nil {
		return nil, e
	}
	return &SubmitExecutionResponse{Run: *run, Created: status == http.StatusAccepted}, nil
}

func (c *executionClient) CheckLease(ctx context.Context, req *CheckExecutionLeaseRequest) (*LeaseCheck, error) {
	return jsonResult[LeaseCheck](
		ctx,
		c.transport,
		http.MethodGet,
		"/internal/v1/executions/"+url.PathEscape(req.ID)+"/lease?epoch="+strconv.FormatInt(req.Epoch, 10),
		nil,
		http.StatusOK,
	)
}

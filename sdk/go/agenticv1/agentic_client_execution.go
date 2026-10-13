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

func (c *executionClient) Submit(ctx context.Context, key string, request SubmitExecutionRequest) (*Run, bool, error) {
	if strings.TrimSpace(key) == "" {
		return nil, false, errors.New("idempotency key is required")
	}
	run, status, e := doJSON[Run](
		ctx,
		c.transport,
		http.MethodPost,
		"/internal/v1/executions",
		request,
		http.Header{"Idempotency-Key": {key}},
		http.StatusAccepted,
		http.StatusOK,
	)
	return run, status == http.StatusAccepted, e
}

func (c *executionClient) CheckLease(ctx context.Context, id string, epoch int64) (*LeaseCheck, error) {
	return jsonResult[LeaseCheck](
		ctx,
		c.transport,
		http.MethodGet,
		"/internal/v1/executions/"+url.PathEscape(id)+"/lease?epoch="+strconv.FormatInt(epoch, 10),
		nil,
		http.StatusOK,
	)
}

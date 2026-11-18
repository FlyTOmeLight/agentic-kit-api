package agenticv1

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	framesdk "github.com/FlyTOmeLight/server-go/sdk"
)

type sandboxClient struct{ transport *transport }

var _ SandboxAPI = (*sandboxClient)(nil)

func (c *sandboxClient) Acquire(ctx context.Context, req *AcquireSandboxRequest) (*SandboxView, error) {
	return jsonResult[SandboxView](ctx, c.transport, http.MethodPost, "/v1/sandboxes", req, nil, http.StatusOK)
}

func (c *sandboxClient) Get(ctx context.Context, id string) (*SandboxView, error) {
	return jsonResult[SandboxView](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/sandboxes/"+url.PathEscape(id),
		nil,
		nil,
		http.StatusOK,
	)
}

func (c *sandboxClient) Renew(ctx context.Context, req *RenewSandboxRequest) (*SandboxLease, error) {
	return jsonResult[SandboxLease](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/sandboxes/"+url.PathEscape(req.ID)+"/renew",
		req,
		nil,
		http.StatusOK,
	)
}

func (c *sandboxClient) Exec(ctx context.Context, req *ExecSandboxRequest) (*SandboxOperation, error) {
	ctx, cancel := context.WithTimeout(ctx, c.transport.timeout)
	defer cancel()
	var operation SandboxOperation
	response, requestErr := c.transport.request(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		SetResult(&operation).
		SetResponseForceContentType("application/json").
		SetResponseBodyUnlimitedReads(true).
		Post("/v1/sandboxes/" + url.PathEscape(req.ID) + "/operations")
	if err := framesdk.Complete(response, requestErr); err != nil {
		if response != nil {
			if id := response.Header().Get("X-Sandbox-Operation-ID"); id != "" {
				return &SandboxOperation{
					ID:         id,
					SandboxID:  req.ID,
					RequestKey: req.RequestKey,
					Status:     SandboxOpUnknown,
				}, err
			}
		}
		return nil, err
	}
	if response.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode())
	}
	return &operation, nil
}

func (c *sandboxClient) GetOperation(ctx context.Context, req *GetSandboxOperationRequest) (*SandboxOperation, error) {
	return jsonResult[SandboxOperation](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/sandboxes/"+url.PathEscape(req.ID)+"/operations/"+url.PathEscape(req.OperationID),
		nil,
		nil,
		http.StatusOK,
	)
}

func (c *sandboxClient) Release(ctx context.Context, id string) (*Sandbox, error) {
	return jsonResult[Sandbox](
		ctx,
		c.transport,
		http.MethodDelete,
		"/v1/sandboxes/"+url.PathEscape(id),
		nil,
		nil,
		http.StatusOK,
	)
}

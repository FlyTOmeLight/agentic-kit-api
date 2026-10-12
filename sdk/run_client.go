package sdk

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
)

type runClient struct{ transport *transport }

var _ RunAPI = (*runClient)(nil)

func (c *runClient) Get(ctx context.Context, id string) (*Run, error) {
	return jsonResult[Run](ctx, c.transport, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, http.StatusOK)
}

func (c *runClient) Trace(ctx context.Context, id string, after int64) (*RunTrace, error) {
	return jsonResult[RunTrace](ctx, c.transport, http.MethodGet, fmt.Sprintf("/v1/runs/%s/trace?after=%d", url.PathEscape(id), after), nil, http.StatusOK)
}

func (c *runClient) Cancel(ctx context.Context, id string) error {
	_, err := jsonResult[struct{}](ctx, c.transport, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/cancel", nil, http.StatusNoContent)
	return err
}

func (c *runClient) Answer(ctx context.Context, id string, req SupplyResultRequest) error {
	_, err := jsonResult[struct{}](ctx, c.transport, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/answer", req, http.StatusNoContent)
	return err
}

func (c *runClient) Resolve(ctx context.Context, id string, req SupplyResultRequest) error {
	_, err := jsonResult[struct{}](ctx, c.transport, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/resolve", req, http.StatusNoContent)
	return err
}

// OpenEvents returns the raw SSE body. The caller must close it, even when
// stopping early. Parse it with an SSE consumer; Event is the JSON data schema.
// Supply the last runID:seq as after to reconnect. No automatic retry is performed.
func (c *runClient) OpenEvents(ctx context.Context, id, after string) (io.ReadCloser, error) {
	resp, err := c.transport.request(ctx).
		SetHeader("Accept", "text/event-stream").
		SetHeader("Last-Event-ID", after).
		SetDoNotParseResponse(true).
		Get("/v1/runs/" + url.PathEscape(id) + "/events")
	if err != nil {
		return nil, err
	}
	body := resp.RawBody()
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		defer body.Close()
		return nil, responseError(resp.StatusCode(), body)
	}
	contentType, _, err := mime.ParseMediaType(resp.Header().Get("Content-Type"))
	if resp.StatusCode() != http.StatusOK || err != nil || contentType != "text/event-stream" {
		body.Close()
		return nil, fmt.Errorf("expected an SSE response")
	}
	return body, nil
}

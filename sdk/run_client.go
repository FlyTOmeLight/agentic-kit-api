package sdk

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
)

func (c *Client) GetRun(ctx context.Context, id string) (*Run, error) {
	return jsonResult[Run](ctx, c, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, http.StatusOK)
}
func (c *Client) CancelRun(ctx context.Context, id string) error {
	_, err := jsonResult[struct{}](ctx, c, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/cancel", nil, http.StatusNoContent)
	return err
}
func (c *Client) AnswerRun(ctx context.Context, id string, req SupplyResultRequest) error {
	_, err := jsonResult[struct{}](ctx, c, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/answer", req, http.StatusNoContent)
	return err
}
func (c *Client) ResolveRun(ctx context.Context, id string, req SupplyResultRequest) error {
	_, err := jsonResult[struct{}](ctx, c, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/resolve", req, http.StatusNoContent)
	return err
}

// OpenRunEvents returns the raw SSE body. The caller must close it, even when
// stopping early. Parse it with an SSE consumer; Event is the JSON data schema.
// Supply the last runID:seq as after to reconnect. No automatic retry is performed.
func (c *Client) OpenRunEvents(ctx context.Context, id, after string) (io.ReadCloser, error) {
	headers := http.Header{"Accept": {"text/event-stream"}}
	if after != "" {
		headers.Set("Last-Event-ID", after)
	}
	resp, err := c.request(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/events", nil, headers)
	if err != nil {
		return nil, err
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || err != nil || contentType != "text/event-stream" {
		resp.Body.Close()
		return nil, fmt.Errorf("expected an SSE response")
	}
	return resp.Body, nil
}

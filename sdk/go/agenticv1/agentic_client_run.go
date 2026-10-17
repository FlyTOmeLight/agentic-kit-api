package agenticv1

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"

	framesdk "github.com/FlyTOmeLight/server-go/sdk"
)

type runClient struct{ transport *transport }

var _ RunAPI = (*runClient)(nil)

func (c *runClient) Get(ctx context.Context, req *GetRunRequest) (*Run, error) {
	return jsonResult[Run](ctx, c.transport, http.MethodGet, "/v1/runs/"+url.PathEscape(req.ID), nil, http.StatusOK)
}

func (c *runClient) Trace(ctx context.Context, req *GetRunTraceRequest) (*RunTrace, error) {
	return jsonResult[RunTrace](
		ctx,
		c.transport,
		http.MethodGet,
		fmt.Sprintf("/v1/runs/%s/trace?after=%d", url.PathEscape(req.ID), req.After),
		nil,
		http.StatusOK,
	)
}

func (c *runClient) Cancel(ctx context.Context, req *CancelRunRequest) error {
	_, err := jsonResult[struct{}](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/runs/"+url.PathEscape(req.ID)+"/cancel",
		nil,
		http.StatusNoContent,
	)
	return err
}

func (c *runClient) Answer(ctx context.Context, req *AnswerRunRequest) error {
	_, err := jsonResult[struct{}](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/runs/"+url.PathEscape(req.ID)+"/answer",
		req,
		http.StatusNoContent,
	)
	return err
}

func (c *runClient) Resolve(ctx context.Context, req *ResolveRunRequest) error {
	_, err := jsonResult[struct{}](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/runs/"+url.PathEscape(req.ID)+"/resolve",
		req,
		http.StatusNoContent,
	)
	return err
}

// OpenEvents returns the raw SSE body. The caller must close it, even when
// stopping early. Parse it with an SSE consumer; Event is the JSON data schema.
// Supply the last runID:seq in LastEventID to reconnect. No automatic retry is performed.
func (c *runClient) OpenEvents(ctx context.Context, req *OpenRunEventsRequest) (io.ReadCloser, error) {
	path := "/v1/runs/" + url.PathEscape(req.ID) + "/events"
	if req.After != "" {
		path = withQuery(path, url.Values{"after": {req.After}})
	}
	resp, err := c.transport.request(ctx).
		SetHeader("Accept", "text/event-stream").
		SetHeader("Last-Event-ID", req.LastEventID).
		SetResponseDoNotParse(true).
		SetTimeout(0).
		Get(path)
	if err != nil {
		return nil, err
	}
	body := resp.Body
	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		defer body.Close()
		return nil, framesdk.DecodeError(resp.StatusCode(), body)
	}
	contentType, _, err := mime.ParseMediaType(resp.Header().Get("Content-Type"))
	if resp.StatusCode() != http.StatusOK || err != nil || contentType != "text/event-stream" {
		body.Close()
		return nil, errors.New("expected an SSE response")
	}
	return body, nil
}

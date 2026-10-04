package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
)

// transport holds immutable client configuration, shared by the three services.
// Each operation creates a fresh Resty request with its own context and result.
type transport struct{ client *resty.Client }

func (t *transport) request(ctx context.Context) *resty.Request {
	return t.client.R().SetContext(ctx)
}

func doJSON[T any](ctx context.Context, t *transport, method, path string, body any, headers http.Header, statuses ...int) (*T, int, error) {
	var result T
	req := t.request(ctx).SetResult(&result).ForceContentType("application/json")
	if body != nil {
		req.SetHeader("Content-Type", "application/json").SetBody(body)
	}
	for name, values := range headers {
		req.Header[name] = append([]string(nil), values...)
	}
	resp, err := req.Execute(method, path)
	if err != nil {
		return nil, 0, err
	}
	status := resp.StatusCode()
	if status < 200 || status >= 300 {
		return nil, status, responseError(status, bytes.NewReader(resp.Body()))
	}
	for _, allowed := range statuses {
		if status == allowed {
			return &result, status, nil
		}
	}
	return nil, status, fmt.Errorf("unexpected HTTP status %d", status)
}

func jsonResult[T any](ctx context.Context, t *transport, method, path string, body any, status int) (*T, error) {
	result, _, err := doJSON[T](ctx, t, method, path, body, nil, status)
	return result, err
}

// Both JSON calls and raw SSE responses use the same public error contract.
func responseError(status int, body io.Reader) error {
	var detail ErrorResponse
	if json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&detail) != nil || detail.Error.Code == "" {
		detail.Error = ErrorDetail{Code: "HTTPError", Message: http.StatusText(status)}
	}
	return &APIError{StatusCode: status, Code: detail.Error.Code, Message: detail.Error.Message}
}

func (p PageOptions) query() url.Values {
	q := url.Values{}
	if p.After != "" {
		q.Set("after", p.After)
	}
	if p.Limit != 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	return q
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

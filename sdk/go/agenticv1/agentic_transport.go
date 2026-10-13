package agenticv1

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	framesdk "github.com/FlyTOmeLight/server-go/sdk"
	"resty.dev/v3"
)

// transport holds immutable client configuration, shared by the three services.
// Each operation creates a fresh Resty request with its own context and result.
type transport struct {
	client  *framesdk.Client
	timeout time.Duration
}

func newTransport(endpoint, token string, hc *http.Client) (*transport, error) {
	cfg := &framesdk.Config{Endpoint: endpoint, Timeout: 20}
	if token != "" {
		cfg.Credentials = &framesdk.Credentials{AuthType: framesdk.AuthTypeAPIKey, APIKey: token}
	}
	client, err := framesdk.NewClientWithConfig(cfg)
	if err != nil {
		return nil, err
	}
	timeout := 20 * time.Second
	if hc != nil {
		if hc.Transport != nil {
			transport := hc.Transport
			if pool, ok := transport.(*http.Transport); ok {
				transport = pool.Clone()
			}
			client.SetTransport(transport)
		}
		client.Client.Client().Jar = hc.Jar
		if hc.Timeout > 0 {
			timeout = hc.Timeout
			client.SetTimeout(timeout)
		}
	}
	client.SetRedirectPolicy(resty.NoRedirectPolicy()).SetHeader("Accept", "application/json").SetRetryCount(0)
	return &transport{client: client, timeout: timeout}, nil
}

func (t *transport) close() error {
	t.client.Client.Client().CloseIdleConnections()
	return t.client.Close()
}

func (t *transport) request(ctx context.Context) *resty.Request {
	return t.client.R().SetContext(ctx)
}

func doJSON[T any](
	ctx context.Context,
	t *transport,
	method, path string,
	body any,
	headers http.Header,
	statuses ...int,
) (*T, int, error) {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	var result T
	req := t.request(ctx).
		SetResult(&result).
		SetForceResponseContentType("application/json").
		SetResponseBodyUnlimitedReads(true)
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
		return nil, status, framesdk.DecodeError(status, bytes.NewReader(resp.Bytes()))
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

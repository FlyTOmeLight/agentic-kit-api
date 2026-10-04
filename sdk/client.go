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
	"strings"
)

// Client can be shared by goroutines. Set deadlines on each request's context.
// The caller owns the transport; Client does not close shared HTTP connections.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient accepts an optional HTTP client for transport, TLS and timeout settings.
// Redirects are disabled to avoid forwarding authenticated operations elsewhere.
func NewClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid API base URL")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("bearer token is required")
	}
	hc := http.Client{}
	if httpClient != nil {
		hc = *httpClient
	}
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &hc}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, headers http.Header) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		req.Header[name] = append([]string(nil), values...)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var detail ErrorResponse
		// Error bodies can come from a proxy. Never put arbitrary HTML into an error.
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&detail) != nil || detail.Error.Code == "" {
			detail.Error = ErrorDetail{Code: "HTTPError", Message: http.StatusText(resp.StatusCode)}
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Code: detail.Error.Code, Message: detail.Error.Message}
	}
	return resp, nil
}
func doJSON[T any](ctx context.Context, c *Client, method, path string, body any, headers http.Header, statuses ...int) (*T, int, error) {
	resp, err := c.request(ctx, method, path, body, headers)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	allowed := false
	for _, status := range statuses {
		if resp.StatusCode == status {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, resp.StatusCode, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	var result T
	if resp.StatusCode != http.StatusNoContent {
		if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return &result, resp.StatusCode, nil
}
func jsonResult[T any](ctx context.Context, c *Client, method, path string, body any, status int) (*T, error) {
	result, _, err := doJSON[T](ctx, c, method, path, body, nil, status)
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

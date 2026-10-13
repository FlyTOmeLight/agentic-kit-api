package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Tenant owns the transport for Forge session and authorization checks.
type Tenant struct {
	endpoint, token string
	http            *http.Client
}

func NewTenant(endpoint, token string) *Tenant {
	return &Tenant{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http: &http.Client{
			Transport:     http.DefaultTransport.(*http.Transport).Clone(),
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (t *Tenant) Close() { t.http.CloseIdleConnections() }

func (t *Tenant) Check(ctx context.Context, path string, body, out any) int {
	raw, err := json.Marshal(body)
	if err != nil {
		return 503
	}
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		strings.TrimRight(t.endpoint, "/")+path,
		bytes.NewReader(raw),
	)
	if err != nil {
		return 503
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return 503
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
			return resp.StatusCode
		}
		return 503
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out) != nil {
		return 503
	}
	return 200
}

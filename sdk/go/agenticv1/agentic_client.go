package agenticv1

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Client groups operations by business responsibility. Its services can be
// shared by goroutines; do not replace the fields while requests are in flight.
type Client struct {
	transport   *transport
	Evaluations EvaluationAPI
	Executions  ExecutionAPI
	Agents      AgentAPI
	Sessions    SessionAPI
	Runs        RunAPI
}

// NewClient constructs framework-backed services with a 20-second JSON timeout.
// The optional HTTP client supplies transport, TLS, cookie jar and timeout settings.
// Close the client after all requests and streams have finished.
func NewClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" {
		return nil, errors.New("invalid API base URL")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("bearer token is required")
	}
	httpTransport, err := newTransport(strings.TrimRight(baseURL, "/"), token, httpClient)
	if err != nil {
		return nil, err
	}
	return &Client{
		transport:   httpTransport,
		Evaluations: &evaluationClient{transport: httpTransport},
		Executions:  &executionClient{transport: httpTransport},
		Agents:      &agentClient{transport: httpTransport},
		Sessions:    &sessionClient{transport: httpTransport},
		Runs:        &runClient{transport: httpTransport},
	}, nil
}

// Close releases the client after all requests and streams have finished.
func (c *Client) Close() error { return c.transport.close() }

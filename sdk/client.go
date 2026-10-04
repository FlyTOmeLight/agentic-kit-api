package sdk

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"
)

// Client groups operations by business responsibility. Its services can be
// shared by goroutines; do not replace the fields while requests are in flight.
type Client struct {
	Agents   AgentAPI
	Sessions SessionAPI
	Runs     RunAPI
}

// NewClient constructs the Resty-backed services. The optional HTTP client
// controls transport, TLS and timeout settings. Request contexts should have
// deadlines. The caller owns the transport and its connection pool.
func NewClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("invalid API base URL")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("bearer token is required")
	}
	hc := http.Client{}
	if httpClient != nil {
		hc = *httpClient
	}
	// Copy the client so redirect policy does not mutate the caller's instance.
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	rc := resty.NewWithClient(&hc).
		SetBaseURL(strings.TrimRight(baseURL, "/")).
		SetAuthToken(token).
		SetHeader("Accept", "application/json").
		SetRetryCount(0)
	httpTransport := &transport{client: rc}
	return &Client{
		Agents:   &agentClient{transport: httpTransport},
		Sessions: &sessionClient{transport: httpTransport},
		Runs:     &runClient{transport: httpTransport},
	}, nil
}

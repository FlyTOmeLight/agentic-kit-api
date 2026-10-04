package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
)

func newClient(t *testing.T, handler http.HandlerFunc) *sdk.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := sdk.NewClient(server.URL+"/api/", "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSendMessageProtocol(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v1/sessions/session/messages" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Idempotency-Key") != "question-1" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
				}
				var req sdk.SendMessageRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query != "你好" {
					t.Errorf("body=%+v err=%v", req, err)
				}
				w.WriteHeader(status)
				io.WriteString(w, `{"id":"run","status":"Queued"}`)
			})
			run, created, err := client.SendMessage(t.Context(), "session", "question-1", sdk.SendMessageRequest{Query: "你好"})
			if err != nil || run.ID != "run" || created != (status == 202) || calls != 1 {
				t.Fatalf("run=%+v created=%v calls=%d err=%v", run, created, calls, err)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"conflict", `{"error":{"code":"Conflict","message":"stale revision"}}`, "Conflict", 409},
		{"proxy", "<html>upstream failed</html>", "HTTPError", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) })
			_, err := client.GetRun(t.Context(), "run")
			var apiErr *sdk.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || apiErr.Code != tc.code || strings.Contains(apiErr.Message, "<html>") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":`) })
	if _, err := client.GetRun(t.Context(), "run"); err == nil {
		t.Fatal("accepted malformed JSON")
	}
}

func TestPaginationAndCancellation(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("after") != "a+b" || q.Get("agentID") != "agent & one" || q.Get("limit") != "3" {
			t.Errorf("query=%v", q)
		}
		io.WriteString(w, `{"items":[]}`)
	})
	if _, err := client.ListSessions(t.Context(), sdk.ListSessionsOptions{PageOptions: sdk.PageOptions{After: "a+b", Limit: 3}, AgentID: "agent & one"}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	client = newClient(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.GetRun(ctx, "run"); result <- err }()
	select {
	case <-entered:
		cancel()
	case <-ctx.Done():
		t.Fatal("request never reached server")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request ignored cancellation")
	}
}

func TestEventStream(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "run:2" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("headers=%v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		io.WriteString(w, "id: run:3\nevent: Completed\ndata: {}\n\n")
	})
	body, err := client.OpenRunEvents(t.Context(), "run", "run:2")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || !strings.Contains(string(data), "id: run:3") {
		t.Fatalf("data=%s err=%v", data, err)
	}
	client = newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	if body, err := client.OpenRunEvents(t.Context(), "run", ""); err == nil {
		body.Close()
		t.Fatal("accepted non-SSE response")
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runs/run" {
			t.Error("followed redirect")
			w.WriteHeader(500)
			return
		}
		http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
	})
	_, err := client.GetRun(t.Context(), "run")
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 307 {
		t.Fatalf("err=%v", err)
	}
}

func TestInvalidClientConfiguration(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{"file:///tmp/api", "token"}, {"https://user:pass@example.com", "token"}, {"https://example.com?key=x", "token"}, {"https://example.com", ""}, {"https://example.com", "token\r\n"},
	} {
		if _, err := sdk.NewClient(tc.url, tc.token, nil); err == nil {
			t.Errorf("accepted %q", tc.url)
		}
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestResponseBodyOwnership(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		contentType, body string
		stream, wantError bool
	}{
		{"JSON", 200, "application/json", `{"id":"run"}`, false, false},
		{"error", 409, "application/json", `{"error":{"code":"Conflict"}}`, false, true},
		{"malformed JSON", 200, "application/json", `{`, false, true},
		{"SSE", 200, "text/event-stream", "data: {}\n\n", true, false},
		{"invalid SSE", 200, "application/json", `{}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			hc := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {tc.contentType}}, Body: body, Request: r}, nil
			})}
			client, err := sdk.NewClient("https://example.com", "token", hc)
			if err != nil {
				t.Fatal(err)
			}
			if tc.stream {
				stream, e := client.OpenRunEvents(t.Context(), "run", "")
				err = e
				if stream != nil {
					if body.closed {
						t.Fatal("closed stream before caller read")
					}
					stream.Close()
				}
			} else {
				_, err = client.GetRun(t.Context(), "run")
			}
			if (err != nil) != tc.wantError || !body.closed {
				t.Fatalf("err=%v closed=%v", err, body.closed)
			}
		})
	}
}

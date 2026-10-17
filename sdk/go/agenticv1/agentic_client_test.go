package agenticv1_test

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

	serrors "github.com/FlyTOmeLight/server-go/errors"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func newClient(t *testing.T, handler http.HandlerFunc) *sdk.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := sdk.NewClient(server.URL+"/api/", "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSendMessageProtocol(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v1/sessions/session/messages" ||
					r.Header.Get("Authorization") != "Bearer test-token" ||
					r.Header.Get("Idempotency-Key") != "question-1" ||
					r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).
					Decode(&req); err != nil || req["query"] != "你好" ||
					len(req) != 1 {
					t.Errorf("body=%+v err=%v", req, err)
				}
				w.WriteHeader(status)
				io.WriteString(w, `{"id":"run","status":"Queued"}`)
			})
			run, err := client.Sessions.SendMessage(
				t.Context(),
				&sdk.SendMessageRequest{
					SessionID:      "session",
					IdempotencyKey: "question-1",
					Query:          "你好",
				},
			)
			created := run != nil && run.Created

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
		{name: "conflict", body: `{"code":"Conflict","message":"stale revision"}`, code: "Conflict", status: 409},
		{name: "proxy", body: "<html>upstream failed</html>", code: "HTTPError", status: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(
				t,
				func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) },
			)
			_, err := client.Runs.Get(t.Context(), &sdk.GetRunRequest{ID: "run"})
			var apiErr *serrors.Error
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Code != tc.code ||
				strings.Contains(apiErr.Message, "<html>") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":`) })
	if _, err := client.Runs.Get(t.Context(), &sdk.GetRunRequest{ID: "run"}); err == nil {
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
	if _, err := client.Sessions.List(
		t.Context(),
		&sdk.ListSessionsRequest{
			ListSessionsOptions: sdk.ListSessionsOptions{
				PageOptions: sdk.PageOptions{After: "a+b", Limit: 3},
				AgentID:     "agent & one",
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	client = newClient(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Runs.Get(ctx, &sdk.GetRunRequest{ID: "run"}); result <- err }()
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
	body, err := client.Runs.OpenEvents(t.Context(), &sdk.OpenRunEventsRequest{ID: "run", LastEventID: "run:2"})
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
	if body, err := client.Runs.OpenEvents(
		t.Context(),
		&sdk.OpenRunEventsRequest{ID: "run", LastEventID: ""},
	); err == nil {
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
	_, err := client.Runs.Get(t.Context(), &sdk.GetRunRequest{ID: "run"})
	var apiErr *serrors.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 307 {
		t.Fatalf("err=%v", err)
	}
}

func TestInvalidClientConfiguration(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{url: "file:///tmp/api", token: "token"}, {url: "https://user:pass@example.com", token: "token"}, {url: "https://example.com?key=x", token: "token"}, {url: "https://example.com", token: ""}, {url: "https://example.com", token: "token\r\n"},
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
		{name: "JSON", status: 200, contentType: "application/json", body: `{"id":"run"}`, stream: false, wantError: false},
		{name: "error", status: 409, contentType: "application/json", body: `{"error":{"code":"Conflict"}}`, stream: false, wantError: true},
		{name: "malformed JSON", status: 200, contentType: "application/json", body: `{`, stream: false, wantError: true},
		{name: "SSE", status: 200, contentType: "text/event-stream", body: "data: {}\n\n", stream: true, wantError: false},
		{name: "invalid SSE", status: 200, contentType: "application/json", body: `{}`, stream: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			hc := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Header:     http.Header{"Content-Type": {tc.contentType}},
					Body:       body,
					Request:    r,
				}, nil
			})}
			client, err := sdk.NewClient("https://example.com", "token", hc)
			if err != nil {
				t.Fatal(err)
			}
			if tc.stream {
				stream, e := client.Runs.OpenEvents(
					t.Context(),
					&sdk.OpenRunEventsRequest{ID: "run", LastEventID: ""},
				)
				err = e
				if stream != nil {
					if body.closed {
						t.Fatal("closed stream before caller read")
					}
					stream.Close()
				}
			} else {
				_, err = client.Runs.Get(t.Context(), &sdk.GetRunRequest{ID: "run"})
			}
			if (err != nil) != tc.wantError || !body.closed {
				t.Fatalf("err=%v closed=%v", err, body.closed)
			}
		})
	}
}

func TestEventStreamReturnsBeforeCompletion(t *testing.T) {
	stopped := make(chan struct{})
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "id: run:1\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body, err := client.Runs.OpenEvents(ctx, &sdk.OpenRunEventsRequest{ID: "run", LastEventID: ""})
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len("id: run:1\ndata: {}\n\n"))
	_, err = io.ReadFull(body, data)
	body.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("closing the stream did not cancel the server request")
	}
}

func TestRunCommands(t *testing.T) {
	for _, command := range []string{"answer", "resolve", "cancel"} {
		t.Run(command, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/runs/run/"+command {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				if command != "cancel" {
					var req sdk.SupplyResultRequest
					if err := json.NewDecoder(r.Body).
						Decode(&req); err != nil || req.StepID != "step" ||
						req.Result != "approved" {
						t.Errorf("request=%+v err=%v", req, err)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})
			var api sdk.RunAPI = client.Runs
			req := sdk.SupplyResultRequest{StepID: "step", Result: "approved"}
			var err error
			switch command {
			case "answer":
				err = api.Answer(
					t.Context(),
					&sdk.AnswerRunRequest{ID: "run", SupplyResultRequest: req},
				)
			case "resolve":
				err = api.Resolve(
					t.Context(),
					&sdk.ResolveRunRequest{ID: "run", SupplyResultRequest: req},
				)
			case "cancel":
				err = api.Cancel(t.Context(), &sdk.CancelRunRequest{ID: "run"})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConcurrentServices(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": parts[len(parts)-1]})
	})
	for _, id := range []string{"first", "second", "third", "fourth"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			agent, err := client.Agents.Get(t.Context(), &sdk.GetAgentRequest{ID: id})
			if err != nil || agent.ID != id {
				t.Fatalf("agent=%+v err=%v", agent, err)
			}
			session, err := client.Sessions.Get(t.Context(), &sdk.GetSessionRequest{ID: id})
			if err != nil || session.ID != id {
				t.Fatalf("session=%+v err=%v", session, err)
			}
		})
	}
}

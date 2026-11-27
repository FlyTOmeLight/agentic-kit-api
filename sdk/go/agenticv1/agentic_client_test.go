package agenticv1_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestAgentConfigDelegationJSON(t *testing.T) {
	config := sdk.AgentConfig{
		SubAgents: []sdk.SubAgentSpec{
			{Name: "research", Description: "Find references", AgentID: "research-id"},
			{Name: "review", Description: "Review references", AgentID: "review-id", Version: 2},
		},
		SelfDelegate: true,
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"research","description":"Find references","agentID":"research-id"},{"name":"review","description":"Review references","agentID":"review-id","version":2}]`
	if string(fields["subAgents"]) != want || string(fields["selfDelegate"]) != "true" {
		t.Fatalf("delegation JSON = %s", data)
	}
	var decoded sdk.AgentConfig
	if err = json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, config) {
		t.Fatalf("decoded = %+v, error = %v", decoded, err)
	}
	data, err = json.Marshal(sdk.AgentConfig{})
	if err != nil || strings.Contains(string(data), `"subAgents"`) ||
		strings.Contains(string(data), `"selfDelegate"`) {
		t.Fatalf("empty delegation config = %s, error = %v", data, err)
	}
}

func TestCreateAgentMCPServers(t *testing.T) {
	const serverJSON = `{"name":"catalog","url":"https://mcp.test/tools","tokenEnv":"CATALOG_TOKEN",` +
		`"defaultPolicy":"Unsafe","allowedTools":["lookup"],"toolPolicies":{"lookup":"ReadOnly"},"forwardScope":true}`
	config := sdk.AgentConfig{
		Model:    sdk.ModelSpec{Name: "test", BaseURL: "https://model.test/v1"},
		MaxTurns: 3,
		MCPServers: []sdk.MCPServerSpec{{
			Name:          "catalog",
			URL:           "https://mcp.test/tools",
			TokenEnv:      "CATALOG_TOKEN",
			DefaultPolicy: sdk.Unsafe,
			AllowedTools:  []string{"lookup"},
			ToolPolicies:  map[string]sdk.Policy{"lookup": sdk.ReadOnly},
			ForwardScope:  true,
		}},
	}
	var wantServer map[string]any
	if err := json.Unmarshal([]byte(serverJSON), &wantServer); err != nil {
		t.Fatal(err)
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/agents" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		var request struct {
			Name   string `json:"name"`
			Config struct {
				MCPServers []map[string]any `json:"mcpServers"`
			} `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Name != "search" ||
			!reflect.DeepEqual(request.Config.MCPServers, []map[string]any{wantServer}) {
			t.Errorf("request = %+v", request)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"agent","draft":{"mcpServers":[`+serverJSON+`]}}`)
	})
	created, err := client.Agents.Create(t.Context(), &sdk.CreateAgentRequest{Name: "search", Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "agent" || !reflect.DeepEqual(created.Draft.MCPServers, config.MCPServers) {
		t.Fatalf("created agent = %+v", created)
	}
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

func TestEventStreamLiveQuery(t *testing.T) {
	for _, test := range []struct {
		name string
		live *bool
		want string
	}{
		{name: "default", want: "after=run%3A2"},
		{name: "enabled", live: new(true), want: "after=run%3A2&live=true"},
		{name: "disabled", live: new(false), want: "after=run%3A2&live=false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != test.want || r.Header.Get("Last-Event-ID") != "run:3" {
					t.Errorf("query=%q headers=%v", r.URL.RawQuery, r.Header)
				}
				w.Header().Set("Content-Type", "text/event-stream")
			})
			body, err := client.Runs.OpenEvents(t.Context(), &sdk.OpenRunEventsRequest{
				ID: "run", After: "run:2", LastEventID: "run:3", Live: test.live,
			})
			if err != nil {
				t.Fatal(err)
			}
			body.Close()
		})
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

func TestSessionAttachmentWireContract(t *testing.T) {
	attachment := sdk.Attachment{
		Kind: sdk.AttachmentImage, URI: "https://media.test/source.jpg",
		ArtifactURI: "artifact://tenant/media/version/source.jpg",
		ID:          "file-1", Name: "source.jpg", MediaType: "image/jpeg", SizeBytes: 42,
	}
	artifact := sdk.FileArtifact{
		Path: "/workspace/report.txt", Name: "report.txt", MediaType: "text/plain",
		SizeBytes: 80, Action: "written", Provider: "Sandbox",
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Query       string           `json:"query"`
				Attachments []sdk.Attachment `json:"attachments"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Query != "inspect" ||
				!reflect.DeepEqual(body.Attachments, []sdk.Attachment{attachment}) {
				t.Errorf("request = %+v %v", body, err)
			}
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":"run","status":"Queued"}`)
		case http.MethodGet:
			io.WriteString(w, `{"items":[
				{"kind":"User","role":"user","content":"inspect","attachments":[{"id":"file-1","kind":"image","uri":"https://media.test/source.jpg","name":"source.jpg","mediaType":"image/jpeg","sizeBytes":42,"artifactURI":"artifact://tenant/media/version/source.jpg"}]},
				{"kind":"Tool","role":"tool","content":"done","artifacts":[{"path":"/workspace/report.txt","name":"report.txt","mediaType":"text/plain","sizeBytes":80,"action":"written","provider":"Sandbox"}]}
			]}`)
		default:
			t.Errorf("unexpected method: %s", r.Method)
		}
	})
	_, err := client.Sessions.SendMessage(t.Context(), &sdk.SendMessageRequest{
		SessionID:      "session",
		IdempotencyKey: "question-1",
		Query:          "inspect",
		Attachments:    []sdk.Attachment{attachment},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Sessions.ListMessages(t.Context(), &sdk.ListMessagesRequest{SessionID: "session"})
	if err != nil || len(page.Items) != 2 ||
		!reflect.DeepEqual(page.Items[0].Attachments, []sdk.Attachment{attachment}) ||
		!reflect.DeepEqual(page.Items[1].Artifacts, []sdk.FileArtifact{artifact}) {
		t.Fatalf("messages = %+v %v", page, err)
	}
}

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/constants"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type testRoutes struct{ register func(*gin.RouterGroup) }

func (s testRoutes) RegisterRoutes(g *gin.RouterGroup) error { s.register(g); return nil }

func (testRoutes) Start(context.Context) error { return nil }

func (testRoutes) Stop(context.Context) error { return nil }

func NewRouter(
	t *testing.T,
	store *durable.Store,
	redisClient redis.UniversalClient,
	configOwners []string,
	tokens map[string]string,
) http.Handler {
	return newRouter(t, store, redisClient, configOwners, tokens, nil, nil)
}

func NewPlatformRouter(
	t *testing.T,
	store *durable.Store,
	redisClient redis.UniversalClient,
	configOwners []string,
	tokens map[string]string,
	cfg platform.Config,
) http.Handler {
	return newRouter(t, store, redisClient, configOwners, tokens, &cfg, nil)
}

func newRouter(
	t *testing.T,
	store *durable.Store,
	redisClient redis.UniversalClient,
	_ []string,
	tokens map[string]string,
	cfg *platform.Config,
	extra func(*gin.RouterGroup),
) http.Handler {
	t.Helper()
	settings, err := conf.FromSettings(map[string]any{"AppName": "agentic", "AppCode": 100, "RunMode": "release"})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	log, err := logger.NewLogger(&logger.Config{Outputs: []logger.Output{{Type: "Console", Level: "Error"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	var tenant *client.Tenant
	if cfg != nil {
		tenant, err = client.NewTenant(cfg.Tenant)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(tenant.Close)
	}
	routes := newRoutes(store, redisClient, cfg, tenant)
	h, err := host.NewHTTPHost(manager, log, nil, testRoutes{register: func(g *gin.RouterGroup) {
		// Test double for the future Forge middleware: it only establishes the
		// identity context and does not authenticate or authorize requests.
		g.Use(func(c *gin.Context) {
			token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if owner := tokens[token]; owner != "" {
				c.Set(constants.KeyUserID, owner)
			}
			c.Next()
		})
		routes.register(g)
		if extra != nil {
			extra(g)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h.Handler()
}

const testToken = "test-bearer-token-at-least-24-chars"

func request(router http.Handler, method, path, body, token, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response serrors.Error
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != code {
		t.Fatalf("response=%+v", response)
	}
}

func TestGinValidation(t *testing.T) {
	router := NewRouter(t, nil, nil, nil, map[string]string{testToken: "owner"})
	tests := []struct {
		name, method, path, body, token, key string
		status                               int
		code                                 string
	}{
		{
			name:   "required fields",
			method: "POST",
			path:   "/v1/sessions/s/messages",
			body:   `{}`,
			token:  testToken,
			key:    "key",
			status: 400,
			code:   "InvalidParameter",
		},
		{
			name:   "unknown field",
			method: "POST",
			path:   "/v1/sessions/s/messages",
			body:   `{"query":"q","ownerID":"other"}`,
			token:  testToken,
			key:    "key",
			status: 400,
			code:   "InvalidParameter",
		},
		{
			name:   "trailing JSON",
			method: "POST",
			path:   "/v1/sessions/s/messages",
			body:   `{"query":"q"} {}`,
			token:  testToken,
			key:    "key",
			status: 400,
			code:   "InvalidParameter",
		},
		{
			name:   "missing key",
			method: "POST",
			path:   "/v1/sessions/s/messages",
			body:   `{"query":"q"}`,
			token:  testToken,
			key:    "",
			status: 400,
			code:   "InvalidParameter",
		},
		{
			name:   "empty query",
			method: "POST",
			path:   "/v1/sessions/s/messages",
			body:   `{"query":"  "}`,
			token:  testToken,
			key:    "key",
			status: 400,
			code:   "InvalidParameter",
		},
		{
			name:   "oversized body",
			method: "POST",
			path:   "/v1/sessions/s/messages",
			body:   `{"query":"` + strings.Repeat("a", 1<<20) + `"}`,
			token:  testToken,
			key:    "key",
			status: 413,
			code:   "PayloadTooLarge",
		},
		{
			name:   "unknown route",
			method: "GET",
			path:   "/missing",
			body:   ``,
			token:  testToken,
			key:    "",
			status: 404,
			code:   "APINotFound",
		},
		{
			name:   "wrong method",
			method: "PUT",
			path:   "/v1/runs/run",
			body:   ``,
			token:  testToken,
			key:    "",
			status: 405,
			code:   "MethodNotAllowed",
		},
		{
			name:   "invalid cursor",
			method: "GET",
			path:   "/v1/runs/run/events?after=other:1",
			body:   ``,
			token:  testToken,
			key:    "",
			status: 400,
			code:   "InvalidCursor",
		},
		{
			name:   "missing answer",
			method: "POST",
			path:   "/v1/runs/run/answer",
			body:   `{"stepID":"s"}`,
			token:  testToken,
			key:    "",
			status: 400,
			code:   "InvalidParameter",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertError(
				t,
				request(router, tc.method, tc.path, tc.body, tc.token, tc.key),
				tc.status,
				tc.code,
			)
		})
	}
}

func TestGinRecoveryDoesNotAppendJSONToStream(t *testing.T) {
	router := newRouter(t, nil, nil, nil, map[string]string{testToken: "owner"}, nil, func(g *gin.RouterGroup) {
		g.GET("/panic", func(c *gin.Context) { panic("internal detail") })
		g.GET("/stream-panic", func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString("data: started\n\n")
			c.Writer.Flush()
			panic("internal detail")
		})
	})
	assertError(t, request(router, "GET", "/panic", "", testToken, ""), 500, "InternalError")
	w := request(router, "GET", "/stream-panic", "", testToken, "")
	if w.Body.String() != "data: started\n\n" {
		t.Fatalf("corrupted SSE: %s", w.Body.String())
	}
}

func integrationStore(t *testing.T) *durable.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	name := "test_" + durable.NewID()
	if err = admin.WithContext(ctx).Exec("CREATE SCHEMA " + name).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	store, err := durable.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		if err := admin.WithContext(ctx).Exec("DROP SCHEMA " + name + " CASCADE").Error; err != nil {
			t.Error(err)
		}
		pool, _ := admin.DB()
		_ = pool.Close()
	})
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestGinRunLifecycleAndSSE(t *testing.T) {
	store := integrationStore(t)
	router := NewRouter(
		t,
		store,
		nil,
		[]string{"owner"},
		map[string]string{testToken: "owner", "other-token-with-24-characters": "other"},
	)
	session := createHTTPConversation(t, router)
	path := "/v1/sessions/" + session.ID + "/messages"
	body := `{"query":"question"}`
	w := request(router, "POST", path, body, testToken, "key")
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var run durable.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if w = request(router, "POST", path, body, testToken, "key"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertError(
		t,
		request(router, "GET", "/v1/runs/"+run.ID, "", "other-token-with-24-characters", ""),
		404,
		"NotFound",
	)
	ctx := context.Background()
	active, err := store.Claim(ctx, "test-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.BeginStep(ctx, active.Lease(), "clarify", "question", durable.ReadOnly); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(
		ctx,
		active.Lease(),
		json.RawMessage(`{"waitingStep":"clarify"}`),
		durable.WaitingClarification,
		"",
		"question",
		"ClarificationRequested",
	); err != nil {
		t.Fatal(err)
	}
	w = request(
		router,
		"POST",
		"/v1/runs/"+run.ID+"/answer",
		`{"stepID":"clarify","result":"answer"}`,
		testToken,
		"",
	)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("answer %d %s", w.Code, w.Body.String())
	}
	active, err = store.Claim(ctx, "test-worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(
		ctx,
		active.Lease(),
		json.RawMessage(`{}`),
		durable.Completed,
		"final\nanswer",
		"",
		"RunCompleted",
	); err != nil {
		t.Fatal(err)
	}
	eventRequest := httptest.NewRequest("GET", "/v1/runs/"+run.ID+"/events", nil)
	eventRequest.Header.Set("Authorization", "Bearer "+testToken)
	w = requestHTTP(t, router, eventRequest)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(w.Body.String(), "event:RunCompleted\n") ||
		!strings.Contains(w.Body.String(), `final\nanswer`) {
		t.Fatalf("SSE %d %s", w.Code, w.Body.String())
	}
	done, err := store.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/runs/"+run.ID+"/events", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Last-Event-ID", run.ID+":"+fmt.Sprint(done.EventSeq))
	w = requestHTTP(t, router, r)
	if w.Code != http.StatusOK {
		t.Fatalf("SSE resume: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "RunCompleted") {
		t.Fatal("replayed event before cursor")
	}
	w = request(router, "POST", path, `{"query":"next"}`, testToken, "next")
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	w = request(router, "POST", "/v1/runs/"+run.ID+"/cancel", "", testToken, "")
	if w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	canceled, err := store.Get(ctx, "owner", run.ID)
	if err != nil || canceled.Status != durable.Canceled {
		t.Fatalf("%+v %v", canceled, err)
	}
}

func createHTTPConversation(t *testing.T, router http.Handler) durable.Session {
	t.Helper()
	w := request(
		router,
		"POST",
		"/v1/agents",
		`{"name":"Assistant","config":{"model":{"name":"test","baseURL":"http://localhost:11434/v1"},"maxTurns":5}}`,
		testToken,
		"",
	)
	if w.Code != 201 {
		t.Fatalf("create agent: %d %s", w.Code, w.Body.String())
	}
	var agent durable.Agent
	if err := json.Unmarshal(w.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	w = request(router, "POST", "/v1/agents/"+agent.ID+"/publish", `{"revision":1}`, testToken, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(router, "POST", "/v1/sessions", `{"agentID":"`+agent.ID+`","title":"test"}`, testToken, "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var session durable.Session
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}

// SSE requires a real connection for per-write deadlines.
func requestHTTP(t *testing.T, handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	request.RequestURI = ""
	request.URL.Scheme = "http"
	request.URL.Host = strings.TrimPrefix(server.URL, "http://")
	client := server.Client()
	client.Timeout = 5 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	recorded := httptest.NewRecorder()
	for key, values := range response.Header {
		recorded.Header()[key] = values
	}
	recorded.WriteHeader(response.StatusCode)
	if _, err := io.Copy(recorded, response.Body); err != nil {
		t.Fatal(err)
	}
	return recorded
}

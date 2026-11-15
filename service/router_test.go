package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	frameworkdb "github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
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
	tokens map[string]string,
) http.Handler {
	return newRouter(t, store, redisClient, tokens, nil)
}

func newRouter(
	t *testing.T,
	store *durable.Store,
	redisClient redis.UniversalClient,
	tokens map[string]string,
	extra func(*gin.RouterGroup),
) http.Handler {
	return newRouterWithCallback(t, store, redisClient, tokens, config.CallbackConfig{}, extra)
}

func newRouterWithCallback(
	t *testing.T,
	store *durable.Store,
	redisClient redis.UniversalClient,
	tokens map[string]string,
	callbackCfg config.CallbackConfig,
	extra func(*gin.RouterGroup),
) http.Handler {
	t.Helper()
	if store == nil {
		// Validation-only tests never reach the database.
		store = &durable.Store{}
	}
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
	artifactServer, _ := newFakeArtifact(t)
	routes := newRoutes(store, redisClient, newArtifactClient(t, artifactServer), nil, nil, callbackCfg)
	h, err := host.NewHTTPHost(manager, log, nil, testRoutes{register: func(g *gin.RouterGroup) {
		// Test double for the future Forge middleware: it only establishes the
		// identity context and does not authenticate or authorize requests.
		g.Use(func(c *gin.Context) {
			token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if owner := tokens[token]; owner != "" {
				c.Set(constants.KeyUserID, owner)
				c.Set(constants.KeyTenantID, "test-tenant")
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

func TestScenarioBusinessRoutesAreAbsent(t *testing.T) {
	router := NewRouter(t, nil, nil, nil)
	for _, path := range []string{
		"/v1/evaluations",
		"/internal/v1/evaluation-trials/trial/lease?runID=run&epoch=1",
		"/internal/v1/coding-release-approval",
		"/api/v1/tenants/tenant/agent-workspace",
		"/api/v1/tenants/tenant/datasets",
		"/api/v1/tenants/tenant/evaluation-plans",
		"/api/v1/tenants/tenant/projects/project/agents",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
			t.Run(method+" "+path, func(t *testing.T) {
				assertError(
					t,
					request(router, method, path, `{}`, "", ""),
					http.StatusNotFound,
					"APINotFound",
				)
			})
		}
	}
}

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
	router := NewRouter(t, nil, nil, map[string]string{testToken: "owner"})
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
	router := newRouter(t, nil, nil, map[string]string{testToken: "owner"}, func(g *gin.RouterGroup) {
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

func TestCallbackSecurity(t *testing.T) {
	secret := "callback-test-secret"
	router := newRouterWithCallback(
		t,
		nil,
		nil,
		map[string]string{testToken: "owner"},
		config.CallbackConfig{
			SigningSecret:  secret,
			AllowedSources: []string{"approval-system"},
		},
		nil,
	)
	path := "/internal/v1/executions/run-1/callback"
	// Invalid JSON body is intentional for the valid-signature case so we can
	// verify that signature & source pass before binder validation runs without a DB.
	body := `{}`
	validSig := callbackSignature(secret, path, "approval-system", body)

	send := func(source, sig string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+testToken)
		if source != "" {
			r.Header.Set("X-Agentic-Source", source)
		}
		if sig != "" {
			r.Header.Set("X-Agentic-Signature", sig)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	assertError(t, send("untrusted", validSig), http.StatusForbidden, "Forbidden")
	assertError(t, send("", validSig), http.StatusForbidden, "Forbidden")
	assertError(t, send("approval-system", ""), http.StatusUnauthorized, "Unauthorized")
	assertError(t, send("approval-system", "sha256=deadbeef"), http.StatusUnauthorized, "Unauthorized")
	assertError(
		t,
		send("approval-system", strings.TrimPrefix(validSig, "sha256=")),
		http.StatusUnauthorized,
		"Unauthorized",
	)
	assertError(
		t,
		send("approval-system", callbackSignature(secret, "/other-run", "approval-system", body)),
		http.StatusUnauthorized,
		"Unauthorized",
	)
	assertError(
		t,
		send("approval-system", callbackSignature(secret, path, "other-source", body)),
		http.StatusUnauthorized,
		"Unauthorized",
	)
	assertError(
		t,
		send("approval-system", callbackSignature(secret, path, "approval-system", `{"result":"tampered"}`)),
		http.StatusUnauthorized,
		"Unauthorized",
	)
	// Passing both source and signature reaches binder validation (400 InvalidParameter on `{}`).
	assertError(t, send("approval-system", validSig), http.StatusBadRequest, "InvalidParameter")
}

func callbackSignature(secret, path, source, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, http.MethodPost+"\n"+path+"\n"+source+"\n"+body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSignedCallbackResumesRunWithoutQueryOverride(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	secret := "callback-test-secret"
	source := "approval-system"
	router := newRouterWithCallback(t, store, nil, map[string]string{testToken: "owner"},
		config.CallbackConfig{SigningSecret: secret, AllowedSources: []string{source}}, nil)
	session := createHTTPConversation(t, router)
	response := request(router, http.MethodPost, "/v1/sessions/"+session.ID+"/messages",
		`{"query":"wait for approval"}`, testToken, "callback-run")
	if response.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", response.Code, response.Body)
	}
	claimed, err := store.Claim(ctx, "callback-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	intent := durable.StepIntent{
		ID:          "approval",
		Input:       "submit",
		Policy:      durable.Idempotent,
		ExternalRef: "approval-42",
	}
	if _, err = store.BeginStep(ctx, claimed.Lease(), intent); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitBoundary(ctx, claimed.Lease(), durable.Boundary{
		Kind: durable.BoundaryAwaitExternal, State: json.RawMessage(`{}`),
		WaitingStep: intent.ID, ExternalRef: intent.ExternalRef, Detail: "awaiting approval",
	}); err != nil {
		t.Fatal(err)
	}
	path := "/internal/v1/executions/" + claimed.ID + "/callback"
	body := `{"stepID":"approval","externalRef":"approval-42","result":"approved"}`
	for range 2 {
		httpRequest := httptest.NewRequest(
			http.MethodPost,
			path+"?StepID=other&ExternalRef=other&Result=tampered",
			strings.NewReader(body),
		)
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Authorization", "Bearer "+testToken)
		httpRequest.Header.Set("X-Agentic-Source", source)
		httpRequest.Header.Set("X-Agentic-Signature", callbackSignature(secret, path, source, body))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httpRequest)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("callback: %d %s", recorder.Code, recorder.Body)
		}
	}
	resumed, err := store.Claim(ctx, "resumed-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.BeginStep(ctx, resumed.Lease(), intent)
	if err != nil || decision.Outcome != durable.StepReplay || decision.Result != "approved" {
		t.Fatalf("signed result changed: %+v err=%v", decision, err)
	}
}

func TestCallbackBodyLimitsAndJSONBinding(t *testing.T) {
	path := "/internal/v1/executions/run/callback"
	for _, secret := range []string{"", "test-secret"} {
		router := newRouterWithCallback(t, nil, nil, nil, config.CallbackConfig{SigningSecret: secret}, nil)
		for _, test := range []struct {
			name, body, contentType, query string
			status                         int
		}{
			{name: "oversize", body: `{"result":"` + strings.Repeat("x", 1<<20) + `"}`, contentType: "application/json", status: http.StatusRequestEntityTooLarge},
			{name: "form", body: "StepID=a&ExternalRef=b&Result=c", contentType: "application/x-www-form-urlencoded", status: http.StatusBadRequest},
			{name: "query only", query: "?StepID=a&ExternalRef=b&Result=c", contentType: "application/json", status: http.StatusBadRequest},
		} {
			t.Run(fmt.Sprintf("signed=%t/%s", secret != "", test.name), func(t *testing.T) {
				httpRequest := httptest.NewRequest(
					http.MethodPost,
					path+test.query,
					strings.NewReader(test.body),
				)
				httpRequest.ContentLength = -1
				httpRequest.Header.Set("Content-Type", test.contentType)
				if secret != "" {
					httpRequest.Header.Set(
						"X-Agentic-Signature",
						callbackSignature(secret, path, "", test.body),
					)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httpRequest)
				if response.Code != test.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body)
				}
			})
		}
	}
}

func integrationStore(t *testing.T) *durable.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := frameworkdb.NewDB(ctx, &frameworkdb.Config{Driver: frameworkdb.DriverPostgreSQL, DSN: dsn})
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
	if err = store.InitSchema(ctx); err != nil {
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
	if _, err = store.BeginStep(ctx, active.Lease(), durable.StepIntent{
		ID:     "clarify",
		Input:  "question",
		Policy: durable.ReadOnly,
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitBoundary(ctx, active.Lease(), durable.Boundary{
		Kind:        durable.BoundaryAwaitClarification,
		State:       json.RawMessage(`{}`),
		Detail:      "question",
		WaitingStep: "clarify",
	}); err != nil {
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
	if err = store.CommitBoundary(
		ctx,
		active.Lease(),
		durable.Boundary{Kind: durable.BoundaryComplete, State: json.RawMessage(`{}`), Output: "final\nanswer"},
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

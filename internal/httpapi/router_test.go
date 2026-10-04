package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
	var response errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != code {
		t.Fatalf("response=%+v", response)
	}
}
func TestGinAuthenticationAndValidation(t *testing.T) {
	router := NewRouter(nil, nil, nil, map[string]string{testToken: "owner"})
	tests := []struct {
		name, method, path, body, token, key string
		status                               int
		code                                 string
	}{
		{"no auth", "POST", "/v1/sessions/s/messages", `{}`, "", "", 401, "Unauthorized"},
		{"wrong token", "GET", "/v1/runs/run", ``, "wrong", "", 401, "Unauthorized"},
		{"required fields", "POST", "/v1/sessions/s/messages", `{}`, testToken, "key", 400, "InvalidRequest"},
		{"unknown field", "POST", "/v1/sessions/s/messages", `{"query":"q","ownerID":"other"}`, testToken, "key", 400, "InvalidRequest"},
		{"trailing JSON", "POST", "/v1/sessions/s/messages", `{"query":"q"} {}`, testToken, "key", 400, "InvalidRequest"},
		{"missing key", "POST", "/v1/sessions/s/messages", `{"query":"q"}`, testToken, "", 400, "InvalidRequest"},
		{"empty query", "POST", "/v1/sessions/s/messages", `{"query":"  "}`, testToken, "key", 400, "InvalidRequest"},
		{"oversized body", "POST", "/v1/sessions/s/messages", `{"query":"` + strings.Repeat("a", maxRequestBytes) + `"}`, testToken, "key", 413, "PayloadTooLarge"},
		{"unknown route", "GET", "/missing", ``, testToken, "", 404, "NotFound"},
		{"wrong method", "PUT", "/v1/runs/run", ``, testToken, "", 405, "MethodNotAllowed"},
		{"invalid cursor", "GET", "/v1/runs/run/events?after=other:1", ``, testToken, "", 400, "InvalidCursor"},
		{"missing answer", "POST", "/v1/runs/run/answer", `{"stepID":"s"}`, testToken, "", 400, "InvalidRequest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertError(t, request(router, tc.method, tc.path, tc.body, tc.token, tc.key), tc.status, tc.code)
		})
	}
}
func TestGinRecoveryDoesNotAppendJSONToStream(t *testing.T) {
	router := NewRouter(nil, nil, nil, map[string]string{testToken: "owner"})
	router.GET("/panic", func(c *gin.Context) { panic("internal detail") })
	router.GET("/stream-panic", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("data: started\n\n")
		c.Writer.Flush()
		panic("internal detail")
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
	router := NewRouter(store, nil, []string{"owner"}, map[string]string{testToken: "owner", "other-token-with-24-characters": "other"})
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
	assertError(t, request(router, "GET", "/v1/runs/"+run.ID, "", "other-token-with-24-characters", ""), 404, "NotFound")
	ctx := context.Background()
	active, err := store.Claim(ctx, "test-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.BeginStep(ctx, active.Lease(), "clarify", "question", durable.ReadOnly); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, active.Lease(), json.RawMessage(`{"waitingStep":"clarify"}`), durable.WaitingClarification, "", "question", "ClarificationRequested"); err != nil {
		t.Fatal(err)
	}
	w = request(router, "POST", "/v1/runs/"+run.ID+"/answer", `{"stepID":"clarify","result":"answer"}`, testToken, "")
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("answer %d %s", w.Code, w.Body.String())
	}
	active, err = store.Claim(ctx, "test-worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ctx, active.Lease(), json.RawMessage(`{}`), durable.Completed, "final\nanswer", "", "RunCompleted"); err != nil {
		t.Fatal(err)
	}
	w = request(router, "GET", "/v1/runs/"+run.ID+"/events", "", testToken, "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(w.Body.String(), "event:RunCompleted\n") || !strings.Contains(w.Body.String(), `final\nanswer`) {
		t.Fatalf("SSE %d %s", w.Code, w.Body.String())
	}
	done, err := store.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/runs/"+run.ID+"/events", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Last-Event-ID", run.ID+":"+fmt.Sprint(done.EventSeq))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
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
	w := request(router, "POST", "/v1/agents", `{"name":"Assistant","config":{"model":{"name":"test","baseURL":"http://localhost:11434/v1"},"maxTurns":5}}`, testToken, "")
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

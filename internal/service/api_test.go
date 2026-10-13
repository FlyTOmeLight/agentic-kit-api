package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	server "github.com/FlyTOmeLight/server-go"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestAPIFrameworkLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := "test_" + durable.NewID()
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", u.String())
	t.Setenv("REDIS_URL", redisURL)
	t.Setenv("LISTEN_ADDR", "127.0.0.1:0")
	t.Setenv("GIN_MODE", "test")
	t.Setenv("SERVER_CONFIG_PATH", "../../conf/server/config.toml")
	t.Setenv("CONFIG_OWNERS", "owner")
	token := "framework-lifecycle-test-token-32chars"
	t.Setenv("API_TOKENS_JSON", `{"`+token+`":"owner"}`)
	for _, key := range []string{"PLATFORM_TENANT_URL", "PLATFORM_TENANT_TOKEN", "PLATFORM_TEMPLATE_OWNER", "PLATFORM_TEMPLATES_JSON", "PLATFORM_CODING_URL", "PLATFORM_CODING_TOKEN"} {
		t.Setenv(key, "")
	}
	cfg, manager, err := config.LoadManager("")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	log, err := logger.NewLogger(&logger.Config{Outputs: []logger.Output{{Type: "Console", Level: "Error"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	api, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := server.NewBootstrap(api)
	if err := bootstrap.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bootstrap.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err := api.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := host.NewHTTPHost(manager, log, nil, api)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatalf("ready before Start: %d", w.Code)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- h.Run(runCtx) }()
	// Join Run before releasing any resources, including when an assertion fails.
	defer func() {
		stop()
		select {
		case err := <-finished:
			select {
			case <-api.done:
			default:
				t.Error("background workers still running")
			}
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("HTTP host did not stop")
		}
	}()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: time.Second, Transport: transport}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	endpoint := ""
ready:
	for {
		if address := h.Address(); address != "" {
			endpoint = "http://" + address
			resp, err := client.Get(endpoint + "/healthz")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					break ready
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("host did not become ready")
		case <-ticker.C:
		}
	}
	for _, tc := range []struct {
		method, path, body, bearer string
		status                     int
	}{
		{method: "GET", path: "/ping", body: "", bearer: "", status: 200},
		{method: "GET", path: "/v1/agents", body: "", bearer: "", status: 401},
		{method: "GET", path: "/missing", body: "", bearer: token, status: 404},
		{method: "PUT", path: "/v1/runs/run", body: "", bearer: token, status: 405},
		{method: "POST", path: "/v1/sessions/s/messages", body: `{"query":"q","unexpected":true}`, bearer: token, status: 400},
	} {
		req, err := http.NewRequestWithContext(ctx, tc.method, endpoint+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if tc.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%s %s: got %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
	}
	sdkClient, err := sdk.NewClient(endpoint, token, client)
	if err != nil {
		t.Fatal(err)
	}
	defer sdkClient.Close()
	agent, err := sdkClient.Agents.Create(
		ctx,
		sdk.CreateAgentRequest{
			Name: "framework",
			Config: sdk.AgentConfig{
				Model:    sdk.ModelSpec{Name: "test", BaseURL: "http://localhost:11434/v1"},
				MaxTurns: 5,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdkClient.Agents.Publish(
		ctx,
		agent.ID,
		sdk.PublishAgentRequest{Revision: agent.Revision},
	); err != nil {
		t.Fatal(err)
	}
	session, err := sdkClient.Sessions.Create(ctx, sdk.CreateSessionRequest{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, created, err := sdkClient.Sessions.SendMessage(
		ctx,
		session.ID,
		"framework-request",
		sdk.SendMessageRequest{Query: "hello"},
	)
	if err != nil || !created {
		t.Fatalf("submit: created=%t err=%v", created, err)
	}
	if _, err := sdkClient.Runs.Get(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := sdkClient.Runs.Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	stream, err := sdkClient.Runs.OpenEvents(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	events, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), "RunQueued") || !strings.Contains(string(events), "RunCanceled") {
		t.Fatalf("missing committed SSE events: %s", events)
	}
}

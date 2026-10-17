package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/httpintegration"
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
	redisAddress := redisURL
	if parsed, parseErr := url.Parse(redisURL); parseErr == nil && parsed.Host != "" {
		redisAddress = parsed.Host
	}
	t.Setenv("REDIS_ADDR", redisAddress)
	t.Setenv("LISTEN_ADDR", "127.0.0.1:0")
	t.Setenv("GIN_MODE", "test")
	t.Setenv("SERVER_CONFIG_PATH", "../conf/server/config.toml")
	token := "framework-lifecycle-test-token-32chars"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	t.Setenv("LISTEN_ADDR", listener.Addr().String())
	_ = listener.Close()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	store, err := durable.Open(ctx, cfg.DB.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- app.Run(runCtx, app.Options{Setup: httpintegration.Install}, Build) }()
	defer func() {
		stop()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("application did not stop")
		}
	}()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: time.Second, Transport: transport}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
ready:
	for {
		resp, err := client.Get(endpoint + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break ready
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
		ctx, &sdk.CreateAgentRequest{
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
		ctx, &sdk.PublishAgentRequest{ID: agent.ID, Revision: agent.Revision},
	); err != nil {
		t.Fatal(err)
	}
	session, err := sdkClient.Sessions.Create(ctx, &sdk.CreateSessionRequest{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := sdkClient.Sessions.SendMessage(
		ctx,
		&sdk.SendMessageRequest{SessionID: session.ID, IdempotencyKey: "framework-request", Query: "hello"},
	)
	created := run != nil && run.Created

	if err != nil || !created {
		t.Fatalf("submit: created=%t err=%v", created, err)
	}
	if _, err := sdkClient.Runs.Get(ctx, &sdk.GetRunRequest{ID: run.ID}); err != nil {
		t.Fatal(err)
	}
	if err := sdkClient.Runs.Cancel(ctx, &sdk.CancelRunRequest{ID: run.ID}); err != nil {
		t.Fatal(err)
	}
	stream, err := sdkClient.Runs.OpenEvents(ctx, &sdk.OpenRunEventsRequest{ID: run.ID, LastEventID: ""})
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

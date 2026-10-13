package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/host"
)

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceFileAndEnv(t *testing.T) {
	clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_URL", "CONFIG_OWNERS", "API_TOKENS_JSON")
	path := writeConfig(t, `[database]
url = "postgres://file:password@localhost/db"
[HTTPServer]
Listen = "127.0.0.1:9000"
[redis]
url = "redis://localhost:6379/0"
[auth]
tokens_json = '{"123456789012345678901234":"owner"}'
config_owners = ['owner']
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPServer.Listen != "127.0.0.1:9000" || len(cfg.Auth.ConfigOwners) != 1 ||
		cfg.Auth.ConfigOwners[0] != "owner" {
		t.Fatalf("file settings not loaded")
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgres://env:password@localhost/db")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9100")
	t.Setenv("CONFIG_OWNERS", "owner,another")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Auth.ConfigOwners) != 2 || cfg.Auth.ConfigOwners[1] != "another" {
		t.Fatal("CONFIG_OWNERS not decoded")
	}
	if cfg.Database.URL != os.Getenv("DATABASE_URL") || cfg.HTTPServer.Listen != "127.0.0.1:9100" {
		t.Fatal("environment did not override file")
	}
}

func TestServiceConfigFailures(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{name: "unknown key", content: "[database]\nurl='postgres://localhost/db'\n[HTTPServer]\nListn='localhost:8000'"},
		{name: "malformed", content: "[database"},
		{name: "missing database", content: "[HTTPServer]\nListen='localhost:8000'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_URL", "CONFIG_OWNERS", "API_TOKENS_JSON")
			if _, err := Load(writeConfig(t, tc.content)); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestMigrationEnvironmentOnly(t *testing.T) {
	clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_URL", "CONFIG_OWNERS", "API_TOKENS_JSON")
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("SERVER_CONFIG_PATH", writeConfig(t, ""))
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPServer.Listen != "127.0.0.1:8080" {
		t.Fatal("missing default")
	}
	if cfg.ValidateAPI() == nil {
		t.Fatal("API settings should be required only for API")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("explicit missing file accepted")
	}
}

func TestProjectPlatformConfiguration(t *testing.T) {
	var cfg Service
	if got, err := cfg.ProjectPlatform(); err != nil || got != nil {
		t.Fatal("disabled platform", got, err)
	}
	cfg.Platform.TenantURL = "http://tenant"
	if _, err := cfg.ProjectPlatform(); err == nil {
		t.Fatal("partial platform config accepted")
	}
	cfg.Platform.TenantToken = "01234567890123456789012345678901"
	cfg.Platform.TemplateOwner = "templates"
	cfg.Platform.TemplatesJSON = `[{"id":"writing","name":"Writing","agentID":"template","version":1}]`
	got, err := cfg.ProjectPlatform()
	if err != nil || len(got.Templates) != 1 {
		t.Fatal(got, err)
	}
	cfg.Platform.TenantURL = "https://user:password@tenant"
	if _, err := cfg.ProjectPlatform(); err == nil {
		t.Fatal("URL credentials accepted")
	}
}

func TestFrameworkSnapshot(t *testing.T) {
	clearEnv(
		t,
		"SERVER_CONFIG_PATH",
		"GIN_MODE",
		"DATABASE_URL",
		"LISTEN_ADDR",
		"REDIS_URL",
		"CONFIG_OWNERS",
		"API_TOKENS_JSON",
	)
	t.Setenv("CONFIG_OWNERS", "owner,another")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9100")
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("API_TOKENS_JSON", `{"123456789012345678901234":"owner"}`)
	path := writeConfig(t, `[HTTPServer]
Listen = "127.0.0.1:9000"
[redis]
url = "${env:TEST_CONFIG_REDIS}"
`)
	t.Setenv("TEST_CONFIG_REDIS", "redis://localhost:6379/0")
	t.Setenv("SERVER_CONFIG_PATH", path)
	cfg, manager, err := LoadManager("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	snapshot := manager.Snapshot()
	var decoded Service
	if err := snapshot.Unmarshal(&decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, decoded) {
		t.Fatalf("application and framework configurations differ")
	}
	if snapshot.HTTPServer.Listen != "127.0.0.1:9100" || snapshot.AppName != "agentic-kit-api" ||
		cfg.Redis.URL != "redis://localhost:6379/0" {
		t.Fatal("effective configuration was not preserved")
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	server := host.NewHTTPServer(snapshot)
	if server.WriteTimeout >= 0 || server.ReadHeaderTimeout != 10*time.Second {
		t.Fatal("SSE or header timeout changed")
	}
	for _, change := range []func(*Service){
		func(c *Service) { c.HTTPServer.APIBasePath = "/prefix" },
		func(c *Service) { c.HTTPServer.WriteTimeout = 0 },
	} {
		bad := cfg
		change(&bad)
		if bad.ValidateAPI() == nil {
			t.Fatal("configuration breaking the HTTP contract accepted")
		}
	}
}

package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
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
	if err := os.WriteFile(
		path,
		[]byte("AppName=\"agentic-kit-api\"\nAppCode=100\nRunMode=\"release\"\n"+text),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceFileAndEnv(t *testing.T) {
	clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_ADDR")
	path := writeConfig(t, `[DB]
Driver = "PostgreSQL"
DSN = "${env:DATABASE_URL:postgres://file:password@localhost/db}"
[HTTPServer]
ReadTimeout = 10000
WriteTimeout = -1
Listen = "${env:LISTEN_ADDR:127.0.0.1:9000}"
[Redis]
Addrs = ["localhost:6379"]
DB = 0
[Artifact]
Endpoint = "http://127.0.0.1:18080"
[Tenant]
Endpoint = "https://tenant.example.test"
ServiceAPIKey = "test-service-key-at-least-32-characters"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPServer.Listen != "127.0.0.1:9000" {
		t.Fatalf("file settings not loaded")
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgres://env:password@localhost/db")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9100")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB.DSN != os.Getenv("DATABASE_URL") || cfg.HTTPServer.Listen != "127.0.0.1:9100" {
		t.Fatal("environment did not override file")
	}
}

func TestServiceConfigFailures(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{name: "malformed", content: "[database"},
		{name: "missing database", content: "[HTTPServer]\nListen='localhost:8000'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_ADDR")
			if _, err := Load(writeConfig(t, tc.content)); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestDatabaseConfigurationWithoutAPISettings(t *testing.T) {
	clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_ADDR")
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv(
		"SERVER_CONFIG_PATH",
		writeConfig(
			t,
			"[DB]\nDriver='PostgreSQL'\nDSN=\"${env:DATABASE_URL}\"\n[HTTPServer]\nListen=\"127.0.0.1:8080\"\nWriteTimeout=-1",
		),
	)
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

func TestFrameworkSnapshot(t *testing.T) {
	clearEnv(
		t,
		"SERVER_CONFIG_PATH",
		"GIN_MODE",
		"DATABASE_URL",
		"LISTEN_ADDR",
		"REDIS_ADDR",
	)
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9100")
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	path := writeConfig(t, `[HTTPServer]
ReadTimeout = 10000
WriteTimeout = -1
Listen = "${env:LISTEN_ADDR:127.0.0.1:9000}"
[DB]
Driver = "PostgreSQL"
DSN = "${env:DATABASE_URL}"
[Redis]
Addrs = ["${env:TEST_CONFIG_REDIS_ADDR}"]
DB = 0
[Artifact]
Endpoint = "${env:ARTIFACT_ENDPOINT:http://127.0.0.1:18080}"
Timeout = 5000
[Tenant]
Endpoint = "https://tenant.example.test"
ServiceAPIKey = "test-service-key-at-least-32-characters"
`)
	t.Setenv("TEST_CONFIG_REDIS_ADDR", "localhost:6379")
	t.Setenv("SERVER_CONFIG_PATH", path)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManager("")
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
		len(cfg.Redis.Addrs) != 1 || cfg.Redis.Addrs[0] != "localhost:6379" {
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
		func(c *Service) { c.Sandbox.CheckpointImage = "saver:v1" },
		func(c *Service) {
			c.Sandbox = SandboxConfig{K8sContext: "local", CheckpointStoreHostPath: "/checkpoints"}
		},
		func(c *Service) {
			c.Sandbox = SandboxConfig{K8sContext: "local", CheckpointImage: "saver:v1", CheckpointStoreHostPath: "relative"}
		},
		func(c *Service) { c.Callback.AllowedSources = []string{"approval"} },
		func(c *Service) { c.Callback = CallbackConfig{SigningSecret: "test", AllowedSources: []string{""}} },
		func(c *Service) {
			c.Callback = CallbackConfig{SigningSecret: "test", AllowedSources: []string{" approval "}}
		},
		func(c *Service) { c.Artifact.Endpoint = "127.0.0.1:18080" },
		func(c *Service) { c.Artifact.Endpoint = "ftp://127.0.0.1" },
		func(c *Service) { c.Artifact.Endpoint = "http://user:pass@127.0.0.1" },
		func(c *Service) { c.Artifact.Endpoint = "http://127.0.0.1/?q=1" },
		func(c *Service) { c.Artifact.Endpoint = "http://127.0.0.1/#fragment" },
		func(c *Service) { c.Artifact.Timeout = -1 },
		func(c *Service) { c.Tenant = TenantConfig{} },
		func(c *Service) { c.Tenant.Endpoint = "http://tenant.example.test" },
		func(c *Service) { c.Tenant.Endpoint = "https://user:pass@tenant.example.test" },
		func(c *Service) { c.Tenant.Endpoint = "https://tenant.example.test/?q=1" },
		func(c *Service) { c.Tenant.Endpoint = "https://tenant.example.test/#fragment" },
		func(c *Service) { c.Tenant.ServiceAPIKey = "short" },
		func(c *Service) { c.Tenant.ServiceAPIKey += "\n" },
		func(c *Service) { c.Tenant.Timeout = -1 },
		func(c *Service) { c.Tenant.AllowedPropagationServices = []string{" "} },
		func(c *Service) { c.Sandbox.AllowedServices = []string{""} },
		func(c *Service) { c.Callback.AllowedServices = []string{" service "} },
		func(c *Service) { c.Callback.Enabled = true },
		func(c *Service) {
			c.Callback = CallbackConfig{Enabled: true, SigningSecret: "test", AllowedSources: []string{"approval"}}
		},
		func(c *Service) {
			c.Callback = CallbackConfig{Enabled: true, SigningSecret: "test", AllowedServices: []string{"approval"}}
		},
		func(c *Service) {
			c.Callback = CallbackConfig{Enabled: true, AllowedSources: []string{"approval"}, AllowedServices: []string{"approval"}}
		},
	} {
		bad := cfg
		change(&bad)
		if bad.ValidateAPI() == nil {
			t.Fatal("configuration breaking the HTTP contract accepted")
		}
	}
	cfg.Sandbox = SandboxConfig{
		K8sContext:              "local",
		CheckpointImage:         "saver:v1",
		CheckpointStoreHostPath: "/checkpoints",
	}
	cfg.Callback = CallbackConfig{
		Enabled: true, SigningSecret: "test", AllowedSources: []string{"approval"}, AllowedServices: []string{"approval"},
	}
	cfg.Tenant.Endpoint = "http://127.0.0.1:8080"
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
}

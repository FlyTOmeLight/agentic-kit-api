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
	} {
		bad := cfg
		change(&bad)
		if bad.ValidateAPI() == nil {
			t.Fatal("configuration breaking the HTTP contract accepted")
		}
	}
}

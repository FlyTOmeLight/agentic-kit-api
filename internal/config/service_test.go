package config

import (
	"os"
	"path/filepath"
	"testing"
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
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceFileAndEnv(t *testing.T) {
	clearEnv(t, "DATABASE_URL", "LISTEN_ADDR", "REDIS_URL", "CONFIG_OWNERS", "API_TOKENS_JSON")
	path := writeConfig(t, `[database]
url = "postgres://file:password@localhost/db"
[http]
listen_addr = "127.0.0.1:9000"
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
	if cfg.HTTP.ListenAddr != "127.0.0.1:9000" || len(cfg.Auth.ConfigOwners) != 1 || cfg.Auth.ConfigOwners[0] != "owner" {
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
	if cfg.Database.URL != os.Getenv("DATABASE_URL") || cfg.HTTP.ListenAddr != "127.0.0.1:9100" {
		t.Fatal("environment did not override file")
	}
}
func TestServiceConfigFailures(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"unknown key", "[database]\nurl='postgres://localhost/db'\n[http]\nlistn_addr='localhost:8000'"},
		{"malformed", "[database"}, {"missing database", "[http]\nlisten_addr='localhost:8000'"},
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
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.ListenAddr != "127.0.0.1:8080" {
		t.Fatal("missing default")
	}
	if cfg.ValidateAPI() == nil {
		t.Fatal("API settings should be required only for API")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("explicit missing file accepted")
	}
}

package config

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	AppName    string
	AppCode    int
	RunMode    string
	HTTPServer conf.HTTPServerConfig
	DB         db.Config
	Redis      frameworkredis.Config
	Sandbox    SandboxConfig
	Callback   CallbackConfig
	Artifact   ArtifactConfig
}

// ArtifactConfig locates the artifact-go service that stores published Agent
// configuration. Timeout is in milliseconds; zero keeps the SDK default.
type ArtifactConfig struct {
	Endpoint string
	Timeout  int
}

// CallbackConfig wires the external-callback HMAC gate. An empty SigningSecret
// disables signature verification (local development only).
type CallbackConfig struct {
	SigningSecret  string
	AllowedSources []string
}

// SandboxConfig wires the phase-2 sandbox control plane. An empty
// K8sContext disables the runtime provider: metadata routes stay available
// and provisioning reports NotReady.
type SandboxConfig struct {
	K8sContext              string
	K8sNamespace            string
	GuardImage              string
	RuntimeClassName        string
	CheckpointImage         string
	CheckpointStoreHostPath string
}

// Load decodes the framework snapshot used by the server.
func Load(path string) (Service, error) {
	snapshot, err := conf.Load(path)
	if err != nil {
		return Service{}, err
	}
	var cfg Service
	if err := snapshot.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.ValidateDatabase()
}

func (cfg Service) ValidateDatabase() error {
	if cfg.DB.Driver != db.DriverPostgreSQL {
		return errors.New("DB.Driver must be PostgreSQL")
	}
	if cfg.DB.AutoMigrate {
		return errors.New(
			"DB.AutoMigrate must be false; schema initialization is managed by service.Build",
		)
	}
	if strings.TrimSpace(cfg.DB.DSN) == "" {
		return errors.New("DB.DSN is required")
	}
	if _, err := pgx.ParseConfig(cfg.DB.DSN); err != nil {
		return errors.New("invalid DB.DSN")
	}
	return nil
}

func validateArtifact(cfg ArtifactConfig) error {
	uri, err := url.Parse(cfg.Endpoint)
	if err != nil || uri.Host == "" || (uri.Scheme != "http" && uri.Scheme != "https") ||
		uri.User != nil || uri.RawQuery != "" || uri.ForceQuery || uri.Fragment != "" {
		return errors.New(
			"Artifact.Endpoint must be an absolute HTTP(S) URL without userinfo, query or fragment",
		)
	}
	if cfg.Timeout < 0 {
		return errors.New("Artifact.Timeout must not be negative")
	}
	return nil
}

// ValidateAPI checks the standalone HTTP and notification settings.
func (cfg Service) ValidateAPI() error {
	if err := cfg.ValidateDatabase(); err != nil {
		return err
	}
	if err := validateArtifact(cfg.Artifact); err != nil {
		return err
	}
	if cfg.HTTPServer.APIBasePath != "" || cfg.HTTPServer.WriteTimeout >= 0 {
		return errors.New(
			"HTTPServer requires an empty APIBasePath and negative WriteTimeout for the public API and SSE",
		)
	}
	_, port, err := net.SplitHostPort(cfg.HTTPServer.Listen)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 0 || number > 65535 {
		return errors.New("invalid HTTPServer.Listen")
	}
	if len(cfg.Redis.Addrs) == 0 {
		return errors.New("Redis.Addrs is required")
	}
	for _, addr := range cfg.Redis.Addrs {
		if strings.TrimSpace(addr) == "" {
			return errors.New("Redis.Addrs must not contain empty addresses")
		}
	}
	if len(cfg.Callback.AllowedSources) > 0 && cfg.Callback.SigningSecret == "" {
		return errors.New("Callback.SigningSecret is required with AllowedSources")
	}
	if cfg.Sandbox.CheckpointImage != "" || cfg.Sandbox.CheckpointStoreHostPath != "" {
		if cfg.Sandbox.K8sContext == "" || cfg.Sandbox.CheckpointImage == "" ||
			!filepath.IsAbs(cfg.Sandbox.CheckpointStoreHostPath) {
			return errors.New(
				"Sandbox checkpoints require K8sContext, CheckpointImage and an absolute CheckpointStoreHostPath",
			)
		}
	}
	for _, source := range cfg.Callback.AllowedSources {
		if source == "" || strings.TrimSpace(source) != source {
			return errors.New("Callback.AllowedSources must contain nonempty trimmed names")
		}
	}
	return nil
}

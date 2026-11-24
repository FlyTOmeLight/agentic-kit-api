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

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

type Service struct {
	AppName       string
	AppCode       int
	RunMode       string
	HTTPServer    conf.HTTPServerConfig
	DB            db.Config
	Redis         frameworkredis.Config
	Sandbox       SandboxConfig
	Callback      CallbackConfig
	Artifact      Artifact
	AgentArtifact ArtifactConfig
	Tenant        TenantConfig
	Pricing       execution.PricingCatalog
}

// Artifact configures the Memory archive adapter. An empty Endpoint disables
// remote archiving; memory writes then stay local without version references.
type Artifact struct {
	Endpoint        string
	StoreID         string
	TenantID        string
	BearerToken     string
	TimeoutSeconds  int
	MaxPayloadBytes int64
}

type TenantConfig struct {
	Endpoint                   string
	ServiceAPIKey              string
	Timeout                    int
	AllowedPropagationServices []string
}

// ArtifactConfig locates the artifact-go service that stores published Agent
// configuration. Timeout is in milliseconds; zero keeps the SDK default.
type ArtifactConfig struct {
	Endpoint string
	Timeout  int
}

// CallbackConfig wires service authentication and the external-callback HMAC gate.
type CallbackConfig struct {
	Enabled         bool
	SigningSecret   string
	AllowedSources  []string
	AllowedServices []string
}

// SandboxConfig wires the phase-2 sandbox control plane. An empty
// K8sContext disables the runtime provider: metadata routes stay available
// and provisioning reports NotReady.
type SandboxConfig struct {
	AllowedServices         []string
	K8sContext              string
	K8sNamespace            string
	GuardImage              string
	RuntimeClassName        string
	CheckpointImage         string
	CheckpointStoreHostPath string
	CheckpointStorePVC      string
	WorkspaceStorageClass   string
	WorkspaceAccessMode     string
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

func validateAgentArtifact(cfg ArtifactConfig) error {
	uri, err := url.Parse(cfg.Endpoint)
	if err != nil || uri.Host == "" || (uri.Scheme != "http" && uri.Scheme != "https") ||
		uri.User != nil || uri.RawQuery != "" || uri.ForceQuery || uri.Fragment != "" {
		return errors.New(
			"AgentArtifact.Endpoint must be an absolute HTTP(S) URL without userinfo, query or fragment",
		)
	}
	if cfg.Timeout < 0 {
		return errors.New("AgentArtifact.Timeout must not be negative")
	}
	return nil
}

// ValidateAPI checks the standalone HTTP and notification settings.
func (cfg Service) ValidateAPI() error {
	if err := cfg.Pricing.Validate(); err != nil {
		return err
	}
	if err := cfg.ValidateDatabase(); err != nil {
		return err
	}
	if err := validateAgentArtifact(cfg.AgentArtifact); err != nil {
		return err
	}
	if cfg.HTTPServer.APIBasePath != "" {
		return errors.New("HTTPServer.APIBasePath must be empty for the public API")
	}
	if cfg.HTTPServer.WriteTimeout < 0 {
		return errors.New("HTTPServer.WriteTimeout must be non-negative")
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
	if err := cfg.ValidateTenant(); err != nil {
		return err
	}
	if cfg.Callback.Enabled && (strings.TrimSpace(cfg.Callback.SigningSecret) == "" ||
		len(cfg.Callback.AllowedSources) == 0 || len(cfg.Callback.AllowedServices) == 0) {
		return errors.New("enabled Callback requires SigningSecret, AllowedSources and AllowedServices")
	}
	if len(cfg.Callback.AllowedSources) > 0 && strings.TrimSpace(cfg.Callback.SigningSecret) == "" {
		return errors.New("Callback.SigningSecret is required with AllowedSources")
	}
	if cfg.Sandbox.CheckpointImage != "" || cfg.Sandbox.CheckpointStoreHostPath != "" ||
		cfg.Sandbox.CheckpointStorePVC != "" {
		if cfg.Sandbox.K8sContext == "" || cfg.Sandbox.CheckpointImage == "" ||
			(cfg.Sandbox.CheckpointStoreHostPath == "" && cfg.Sandbox.CheckpointStorePVC == "") ||
			(cfg.Sandbox.CheckpointStoreHostPath != "" && cfg.Sandbox.CheckpointStorePVC != "") ||
			(cfg.Sandbox.CheckpointStoreHostPath != "" && !filepath.IsAbs(cfg.Sandbox.CheckpointStoreHostPath)) {
			return errors.New(
				"Sandbox checkpoints require K8sContext, CheckpointImage and one absolute CheckpointStoreHostPath or CheckpointStorePVC",
			)
		}
	}
	for _, list := range []struct {
		name   string
		values []string
	}{
		{name: "Callback.AllowedSources", values: cfg.Callback.AllowedSources},
		{name: "Callback.AllowedServices", values: cfg.Callback.AllowedServices},
		{name: "Sandbox.AllowedServices", values: cfg.Sandbox.AllowedServices},
		{name: "Tenant.AllowedPropagationServices", values: cfg.Tenant.AllowedPropagationServices},
	} {
		for _, value := range list.values {
			if value == "" || strings.TrimSpace(value) != value {
				return errors.New(list.name + " must contain nonempty trimmed names")
			}
		}
	}
	return cfg.Artifact.validate()
}

func (cfg Artifact) validate() error {
	if cfg.Endpoint == "" {
		return nil
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || !endpoint.IsAbs() ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return errors.New("Artifact.Endpoint must be an absolute http(s) URL")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return errors.New("Artifact.StoreID is required")
	}
	if strings.TrimSpace(cfg.TenantID) == "" {
		return errors.New("Artifact.TenantID is required")
	}
	if strings.TrimSpace(cfg.BearerToken) == "" {
		return errors.New("Artifact.BearerToken is required when Endpoint is set")
	}
	if cfg.TimeoutSeconds <= 0 || cfg.TimeoutSeconds > 60 {
		return errors.New("Artifact.TimeoutSeconds must be between 1 and 60")
	}
	if cfg.MaxPayloadBytes <= 0 || cfg.MaxPayloadBytes > 256*1024 {
		return errors.New("Artifact.MaxPayloadBytes must be between 1 and 262144")
	}
	return nil
}

func (cfg Service) ValidateTenant() error {
	uri, err := url.Parse(cfg.Tenant.Endpoint)
	if err != nil || uri.Host == "" || uri.User != nil || uri.RawQuery != "" ||
		uri.ForceQuery || uri.Fragment != "" ||
		(uri.Scheme != "https" && (uri.Scheme != "http" ||
			(uri.Hostname() != "localhost" && !net.ParseIP(uri.Hostname()).IsLoopback()))) {
		return errors.New(
			"Tenant.Endpoint must be HTTPS, or HTTP on loopback, without userinfo, query or fragment",
		)
	}
	if len(cfg.Tenant.ServiceAPIKey) < 32 || strings.ContainsAny(cfg.Tenant.ServiceAPIKey, "\r\n") {
		return errors.New("Tenant.ServiceAPIKey must contain at least 32 characters and no line breaks")
	}
	if cfg.Tenant.Timeout < 0 {
		return errors.New("Tenant.Timeout must not be negative")
	}
	return nil
}

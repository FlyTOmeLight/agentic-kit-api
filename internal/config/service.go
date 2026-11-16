package config

import (
	"errors"
	"net"
	"net/url"
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
	Artifact   Artifact
}

// Artifact configures the Memory archive adapter. An empty Endpoint disables
// remote archiving; memory writes then stay local without version references.
type Artifact struct {
	Endpoint        string
	StoreID         string
	TenantID        string
	TimeoutSeconds  int
	MaxPayloadBytes int64
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

// ValidateAPI checks the standalone API settings.
func (cfg Service) ValidateAPI() error {
	if err := cfg.ValidateDatabase(); err != nil {
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
	if cfg.Artifact.Endpoint == "" {
		return nil
	}
	endpoint, err := url.Parse(cfg.Artifact.Endpoint)
	if err != nil || !endpoint.IsAbs() ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
		return errors.New("Artifact.Endpoint must be an absolute http(s) URL")
	}
	if strings.TrimSpace(cfg.Artifact.StoreID) == "" {
		return errors.New("Artifact.StoreID is required")
	}
	if strings.TrimSpace(cfg.Artifact.TenantID) == "" {
		return errors.New("Artifact.TenantID is required")
	}
	if cfg.Artifact.TimeoutSeconds <= 0 || cfg.Artifact.TimeoutSeconds > 60 {
		return errors.New("Artifact.TimeoutSeconds must be between 1 and 60")
	}
	if cfg.Artifact.MaxPayloadBytes <= 0 || cfg.Artifact.MaxPayloadBytes > 256*1024 {
		return errors.New("Artifact.MaxPayloadBytes must be between 1 and 262144")
	}
	return nil
}

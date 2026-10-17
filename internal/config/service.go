package config

import (
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	framesdk "github.com/FlyTOmeLight/server-go/sdk"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
)

type Service struct {
	AppName    string
	AppCode    int
	RunMode    string
	HTTPServer conf.HTTPServerConfig
	Tenant     framesdk.Config
	Coding     framesdk.Config
	DB         db.Config
	Redis      frameworkredis.Config
}

// Load decodes the framework snapshot used by the server and migration command.
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
		return errors.New("DB.AutoMigrate must be false; use the migrate command")
	}
	if strings.TrimSpace(cfg.DB.DSN) == "" {
		return errors.New("DB.DSN is required")
	}
	if _, err := pgx.ParseConfig(cfg.DB.DSN); err != nil {
		return errors.New("invalid DB.DSN")
	}
	return nil
}

func (cfg Service) ProjectPlatform() (*platform.Config, error) {
	if cfg.Tenant.Endpoint == "" && cfg.Coding.Endpoint == "" {
		return nil, nil
	}
	if cfg.Tenant.Endpoint == "" {
		return nil, errors.New("Tenant.Endpoint is required when Coding is configured")
	}
	for _, clientConfig := range []framesdk.Config{cfg.Tenant, cfg.Coding} {
		if clientConfig.Endpoint == "" {
			continue
		}
		c, err := framesdk.NewClientWithConfig(&clientConfig)
		if err != nil {
			return nil, err
		}
		_ = c.Close()
	}
	return &platform.Config{Tenant: cfg.Tenant, CodingURL: cfg.Coding.Endpoint}, nil
}

// ValidateAPI checks settings that the migration command does not need.
func (cfg Service) ValidateAPI() error {
	if err := cfg.ValidateDatabase(); err != nil {
		return err
	}
	if cfg.HTTPServer.APIBasePath != "" || cfg.HTTPServer.WriteTimeout >= 0 {
		return errors.New(
			"HTTPServer requires an empty APIBasePath and negative WriteTimeout for the public API and SSE",
		)
	}
	if _, err := cfg.ProjectPlatform(); err != nil {
		return err
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
	return nil
}

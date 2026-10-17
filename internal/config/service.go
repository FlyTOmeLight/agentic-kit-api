package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
)

type Service struct {
	AppName    string
	AppCode    int
	RunMode    string
	HTTPServer conf.HTTPServerConfig
	Platform   struct {
		CodingURL     string `mapstructure:"coding_url"`
		CodingToken   string `mapstructure:"coding_token"`
		TenantURL     string `mapstructure:"tenant_url"`
		TenantToken   string `mapstructure:"tenant_token"`
		TemplateOwner string `mapstructure:"template_owner"`
		TemplatesJSON string `mapstructure:"templates_json"`
	} `mapstructure:"platform"`
	DB    db.Config
	Redis struct {
		URL string `mapstructure:"url"`
	} `mapstructure:"redis"`
	Auth struct {
		ConfigOwners []string `mapstructure:"config_owners"`
		TokensJSON   string   `mapstructure:"tokens_json"`
	} `mapstructure:"auth"`
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
	p := cfg.Platform
	if p.TenantURL == "" && p.TenantToken == "" && p.TemplateOwner == "" && p.TemplatesJSON == "" &&
		p.CodingURL == "" &&
		p.CodingToken == "" {
		return nil, nil
	}
	out := platform.Config{
		CodingURL:     p.CodingURL,
		CodingToken:   p.CodingToken,
		TenantURL:     p.TenantURL,
		TenantToken:   p.TenantToken,
		TemplateOwner: p.TemplateOwner,
	}
	if err := json.Unmarshal([]byte(p.TemplatesJSON), &out.Templates); err != nil {
		return nil, errors.New("invalid platform.templates_json")
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
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
	if cfg.Redis.URL == "" {
		return errors.New("redis.url is required")
	}
	if _, err := redis.ParseURL(cfg.Redis.URL); err != nil {
		return errors.New("invalid redis.url")
	}
	if _, err := auth.ParseBearerTokens(cfg.Auth.TokensJSON); err != nil {
		return fmt.Errorf("invalid auth.tokens_json: %w", err)
	}
	return nil
}

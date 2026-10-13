package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"

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
	Database struct {
		URL string `mapstructure:"url"`
	} `mapstructure:"database"`
	Redis struct {
		URL string `mapstructure:"url"`
	} `mapstructure:"redis"`
	Auth struct {
		ConfigOwners []string `mapstructure:"config_owners"`
		TokensJSON   string   `mapstructure:"tokens_json"`
	} `mapstructure:"auth"`
}

// Load reads a TOML file when specified. Bound environment variables override it.
// Configuration authors are explicitly listed by the deployment operator.
func Load(path string) (Service, error) {
	cfg, _, err := load(path)
	return cfg, err
}

// LoadManager supplies the same effective snapshot to the application and HTTP host.
func LoadManager(path string) (Service, *conf.Manager, error) {
	cfg, snapshot, err := load(path)
	if err != nil {
		return cfg, nil, err
	}
	manager, err := conf.NewManagerFromConfig(snapshot)
	return cfg, manager, err
}

func load(path string) (Service, *conf.Config, error) {
	var cfg Service
	v := viper.New()
	v.SetConfigType("toml")
	v.AllowEmptyEnv(true)
	v.SetDefault("auth.config_owners", []string{})
	v.SetDefault("AppName", "agentic-kit-api")
	v.SetDefault("AppCode", 100)
	v.SetDefault("RunMode", "release")
	v.SetDefault("HTTPServer.Listen", "127.0.0.1:8080")
	v.SetDefault("HTTPServer.ReadTimeout", 10000)
	v.SetDefault("HTTPServer.WriteTimeout", -1)
	v.SetDefault("HTTPServer.IdleTimeout", 60000)
	for key, env := range map[string]string{
		"platform.coding_url": "PLATFORM_CODING_URL", "platform.coding_token": "PLATFORM_CODING_TOKEN",
		"platform.tenant_url": "PLATFORM_TENANT_URL", "platform.tenant_token": "PLATFORM_TENANT_TOKEN", "platform.template_owner": "PLATFORM_TEMPLATE_OWNER", "platform.templates_json": "PLATFORM_TEMPLATES_JSON",
		"HTTPServer.Listen": "LISTEN_ADDR", "RunMode": "GIN_MODE", "database.url": "DATABASE_URL",
		"redis.url": "REDIS_URL", "auth.config_owners": "CONFIG_OWNERS", "auth.tokens_json": "API_TOKENS_JSON",
	} {
		if err := v.BindEnv(key, env); err != nil {
			return cfg, nil, fmt.Errorf("bind %s: %w", key, err)
		}
	}
	if path == "" {
		path = os.Getenv(conf.EnvServerConfigPath)
		if path == "" {
			path = conf.DefaultConfigPath
		}
	}
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return cfg, nil, fmt.Errorf("read service config: %w", err)
		}
	}
	if err := v.UnmarshalExact(&cfg, conf.DecodeHook()); err != nil {
		return cfg, nil, fmt.Errorf("decode service config: %w", err)
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return cfg, nil, errors.New("database.url is required")
	}
	if _, err := pgx.ParseConfig(cfg.Database.URL); err != nil {
		return cfg, nil, errors.New("invalid database.url")
	}
	snapshot, err := conf.FromSettings(v.AllSettings())
	if err != nil {
		return cfg, nil, err
	}
	if err := snapshot.Unmarshal(&cfg); err != nil {
		return cfg, nil, err
	}
	return cfg, snapshot, nil
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

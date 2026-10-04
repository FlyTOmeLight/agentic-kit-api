package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

type Service struct {
	HTTP struct {
		ListenAddr string `mapstructure:"listen_addr"`
	} `mapstructure:"http"`
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
	var cfg Service
	v := viper.New()
	v.SetConfigType("toml")
	v.AllowEmptyEnv(true)
	v.SetDefault("auth.config_owners", []string{})
	v.SetDefault("http.listen_addr", "127.0.0.1:8080")
	for key, env := range map[string]string{
		"http.listen_addr": "LISTEN_ADDR", "database.url": "DATABASE_URL",
		"redis.url": "REDIS_URL", "auth.config_owners": "CONFIG_OWNERS", "auth.tokens_json": "API_TOKENS_JSON",
	} {
		if err := v.BindEnv(key, env); err != nil {
			return cfg, fmt.Errorf("bind %s: %w", key, err)
		}
	}
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return cfg, fmt.Errorf("read service config: %w", err)
		}
	}
	if err := v.UnmarshalExact(&cfg); err != nil {
		return cfg, fmt.Errorf("decode service config: %w", err)
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return cfg, fmt.Errorf("database.url is required")
	}
	if _, err := pgx.ParseConfig(cfg.Database.URL); err != nil {
		return cfg, fmt.Errorf("invalid database.url")
	}
	return cfg, nil
}

// ValidateAPI checks settings that the migration command does not need.
func (cfg Service) ValidateAPI() error {
	_, port, err := net.SplitHostPort(cfg.HTTP.ListenAddr)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 0 || number > 65535 {
		return fmt.Errorf("invalid http.listen_addr")
	}
	if cfg.Redis.URL == "" {
		return fmt.Errorf("redis.url is required")
	}
	if _, err := redis.ParseURL(cfg.Redis.URL); err != nil {
		return fmt.Errorf("invalid redis.url")
	}
	if _, err := LoadTokens(cfg.Auth.TokensJSON); err != nil {
		return fmt.Errorf("invalid auth.tokens_json: %w", err)
	}
	return nil
}

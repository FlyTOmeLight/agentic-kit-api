package service

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/swagger"

	_ "github.com/FlyTOmeLight/agentic-kit-api/doc/openapi"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// Config describes the standalone application configuration.
type Config = config.Service

// Build owns standalone connections; combined applications inject their own
// shared connections through New instead.
func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
	if err := cfg.ValidateAPI(); err != nil {
		return err
	}
	runtime.Config.Subscribe(prepareHTTPConfig)
	database := &db.Module{Config: &cfg.DB}
	cache := &notificationCache{config: &cfg.Redis}
	if err := runtime.Initialize(ctx, database, cache); err != nil {
		return err
	}
	store, err := durable.New(database.DB)
	if err != nil {
		return err
	}
	api, err := New(*cfg, runtime.Log, store, cache.client)
	if err != nil {
		return err
	}
	swagger.DocPrefix = "Agentic"
	return runtime.AddService(api)
}

// Keep the public route contract when the framework rebuilds handlers on reload.
func prepareHTTPConfig(_, next *conf.Config, _ []string) (func(), error) {
	if next.HTTPServer.APIBasePath != "" {
		return nil, errors.New("HTTPServer.APIBasePath must remain empty for the public API")
	}
	return nil, nil
}

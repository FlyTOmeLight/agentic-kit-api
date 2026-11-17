package service

import (
	"context"
	"errors"
	"time"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/conf"
	frameworkdb "github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/swagger"

	_ "github.com/FlyTOmeLight/agentic-kit-api/doc/openapi"
	appmemory "github.com/FlyTOmeLight/agentic-kit-api/internal/app/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
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
	database := &frameworkdb.Module{Config: &cfg.DB}
	cache := &notificationCache{config: &cfg.Redis}
	if err := runtime.Initialize(ctx, database, cache); err != nil {
		return err
	}
	store, err := durable.New(database.DB)
	if err != nil {
		return err
	}
	if err = store.InitSchema(ctx); err != nil {
		return err
	}
	archive, err := buildArchiver(cfg)
	if err != nil {
		return err
	}
	var archiver appmemory.Archiver
	if archive != nil {
		archiver = archive
	}
	api, err := New(*cfg, runtime.Log, store, cache.client, archiver)
	if err != nil {
		if archive != nil {
			err = errors.Join(err, archive.Close())
		}
		return err
	}
	if archive != nil {
		api.ownedArchive = archive
	}
	swagger.DocPrefix = "Agentic"
	if err := runtime.AddService(api); err != nil {
		return errors.Join(err, api.closeArchive())
	}
	return nil
}

// buildArchiver constructs the Memory archive adapter when an artifact
// endpoint is configured. Without one the deployment keeps Memory writes
// local; recall and Off continue to work.
func buildArchiver(cfg *Config) (*client.ArtifactStore, error) {
	if cfg.Artifact.Endpoint == "" {
		return nil, nil
	}
	store, err := client.NewArtifactStore(client.ArtifactConfig{
		Endpoint:        cfg.Artifact.Endpoint,
		StoreID:         cfg.Artifact.StoreID,
		TenantID:        cfg.Artifact.TenantID,
		BearerToken:     cfg.Artifact.BearerToken,
		Timeout:         time.Duration(cfg.Artifact.TimeoutSeconds) * time.Second,
		MaxPayloadBytes: cfg.Artifact.MaxPayloadBytes,
	})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// Keep the public route contract when the framework rebuilds handlers on reload.
func prepareHTTPConfig(_, next *conf.Config, _ []string) (func(), error) {
	if next.HTTPServer.APIBasePath != "" {
		return nil, errors.New("HTTPServer.APIBasePath must remain empty for the public API")
	}
	return nil, nil
}

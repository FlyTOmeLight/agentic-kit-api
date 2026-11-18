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

// artifactModule adapts the artifact client to the runtime lifecycle.
type artifactModule struct{ client client.ArtifactStore }

func (m *artifactModule) Initialize(context.Context) error { return nil }

func (m *artifactModule) Close(_ context.Context) error { return m.client.Close() }

type tenantModule struct{ client *client.Tenant }

func (m *tenantModule) Initialize(context.Context) error { return nil }

func (m *tenantModule) Close(context.Context) error { return m.client.Close() }

// Build owns standalone connections; combined applications inject their own
// shared connections through New instead.
func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
	if err := cfg.ValidateAPI(); err != nil {
		return err
	}
	tenant, err := client.NewTenant(cfg.Tenant.Endpoint, cfg.Tenant.ServiceAPIKey,
		time.Duration(cfg.Tenant.Timeout)*time.Millisecond)
	if err != nil {
		return err
	}
	if err = runtime.Initialize(ctx, &tenantModule{client: tenant}); err != nil {
		return err
	}
	authentication := Authentication{User: tenant.User()}
	authentication.Propagation, err = tenant.Propagation(cfg.Tenant.AllowedPropagationServices)
	if err != nil {
		return err
	}
	authentication.Service, err = tenant.Service(cfg.Sandbox.AllowedServices)
	if err != nil {
		return err
	}
	if cfg.Callback.Enabled {
		authentication.Callback, err = tenant.Service(cfg.Callback.AllowedServices)
		if err != nil {
			return err
		}
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
	artifact, err := client.NewArtifact(cfg.AgentArtifact.Endpoint, cfg.Tenant.ServiceAPIKey,
		time.Duration(cfg.AgentArtifact.Timeout)*time.Millisecond)
	if err != nil {
		return err
	}
	if err = runtime.Initialize(ctx, &artifactModule{client: artifact}); err != nil {
		artifact.Close()
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
	api, err := NewWithSandbox(
		*cfg,
		runtime.Log,
		store,
		cache.client,
		artifact,
		archiver,
		database.DB,
		authentication,
	)
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
func buildArchiver(cfg *Config) (*client.MemoryArchive, error) {
	if cfg.Artifact.Endpoint == "" {
		return nil, nil
	}
	store, err := client.NewMemoryArchive(client.ArtifactConfig{
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

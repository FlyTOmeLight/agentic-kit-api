// Package service assembles the API and its server-go lifecycle.
package service

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/sandboxcenter"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

var _ host.Service = (*API)(nil)

type Authentication struct {
	User        []core.HandlerFunc
	Service     []core.HandlerFunc
	Propagation []core.HandlerFunc
	Callback    []core.HandlerFunc
}

type API struct {
	routes     *routes
	log        *logger.Logger
	stopped    bool
	store      *durable.Store
	redis      frameworkredis.Redis
	reconciler *sandboxcenter.Reconciler
	cancel     context.CancelFunc
	done       chan struct{}
}

// New constructs an API using caller-owned resources. Stop joins background tasks;
// the caller releases Store and Redis after Stop succeeds.
func New(
	cfg Config,
	log *logger.Logger,
	store *durable.Store,
	cache frameworkredis.Redis,
	artifact client.ArtifactStore,
	authentication Authentication,
) (*API, error) {
	if err := cfg.ValidateAPI(); err != nil {
		return nil, err
	}
	if log == nil || store == nil || cache == nil || artifact == nil {
		return nil, errors.New("API requires logger, store, Redis and artifact client")
	}
	if len(authentication.User) == 0 {
		return nil, errors.New("API requires user authentication")
	}
	if len(cfg.Tenant.AllowedPropagationServices) > 0 && len(authentication.Propagation) == 0 {
		return nil, errors.New("API requires configured propagation authentication")
	}
	if len(cfg.Sandbox.AllowedServices) > 0 && len(authentication.Service) == 0 {
		return nil, errors.New("API requires configured sandbox service authentication")
	}
	if cfg.Callback.Enabled && len(authentication.Callback) == 0 {
		return nil, errors.New("API requires callback service authentication")
	}
	if len(cfg.Tenant.AllowedPropagationServices) == 0 {
		authentication.Propagation = nil
	}
	if len(cfg.Sandbox.AllowedServices) == 0 {
		authentication.Service = nil
	}
	a := &API{log: log, store: store, redis: cache}
	a.routes = newRoutes(store, cache, artifact, nil, nil, cfg.Callback, authentication)
	return a, nil
}

// NewWithSandbox constructs the API with the sandbox control plane. When
// cfg.Sandbox.K8sContext is empty the provider is disabled and only
// metadata routes are served.
func NewWithSandbox(
	cfg Config,
	log *logger.Logger,
	store *durable.Store,
	cache frameworkredis.Redis,
	artifact client.ArtifactStore,
	db *gorm.DB,
	authentication Authentication,
) (*API, error) {
	a, err := New(cfg, log, store, cache, artifact, authentication)
	if err != nil {
		return nil, err
	}
	sandboxStore := sandboxcenter.NewStore(db)
	var provider *sandboxcenter.Provider
	if cfg.Sandbox.K8sContext != "" {
		provider, err = sandboxcenter.NewProvider(sandboxcenter.K8sConfig{
			Context:          cfg.Sandbox.K8sContext,
			Namespace:        cfg.Sandbox.K8sNamespace,
			GuardImage:       cfg.Sandbox.GuardImage,
			RuntimeClassName: cfg.Sandbox.RuntimeClassName,
		})
		if err != nil {
			return nil, err
		}
		a.reconciler = sandboxcenter.NewReconciler(
			sandboxStore,
			provider,
			sandboxcenter.ReconcilerConfig{},
			log,
		)
	}
	a.routes = newRoutes(store, cache, artifact, sandboxStore, provider, cfg.Callback, a.routes.authentication)
	if provider != nil && cfg.Sandbox.CheckpointImage != "" {
		checkpoints := sandboxcenter.NewService(sandboxStore, provider).
			WithCheckpoints(sandboxcenter.CheckpointConfig{
				Image: cfg.Sandbox.CheckpointImage, StoreHostPath: cfg.Sandbox.CheckpointStoreHostPath,
			})
		a.routes.sandbox = sandboxcenter.NewController(checkpoints)
		a.reconciler.WithCheckpoints(checkpoints)
	}
	return a, nil
}

func (a *API) RegisterRoutes(router *core.RouterGroup) error {
	a.routes.register(router)
	return nil
}

func (a *API) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.cancel != nil || a.stopped {
		return errors.New("API has already started")
	}
	ctx, a.cancel = context.WithCancel(ctx)
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		relayOutbox(
			ctx,
			a.store,
			a.redis,
			func(err error) { a.log.Errorf(ctx, "outbox publish failed: %v", err) },
		)
	}()
	if a.reconciler != nil {
		go func() {
			if err := a.reconciler.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.log.Errorf(ctx, "sandbox reconciler stopped: %v", err)
			}
		}()
	}
	return nil
}

func (a *API) Stop(ctx context.Context) error {
	if a.cancel == nil {
		a.stopped = true
		return nil
	}
	a.cancel()
	select {
	case <-a.done:
		a.stopped = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

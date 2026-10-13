// Package service owns API resources and adapts them to server-go's lifecycle.
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type API struct {
	cfg      config.Service
	log      *logger.Logger
	tokens   map[string]string
	tenant   *client.Tenant
	platform *platform.Config
	coding   *client.CodingEnvironment
	store    *durable.Store
	redis    *redis.Client
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(cfg config.Service, log *logger.Logger) (*API, error) {
	if err := cfg.ValidateAPI(); err != nil {
		return nil, err
	}
	tokens, err := auth.ParseBearerTokens(cfg.Auth.TokensJSON)
	if err != nil {
		return nil, err
	}
	platformConfig, err := cfg.ProjectPlatform()
	if err != nil {
		return nil, err
	}
	api := &API{cfg: cfg, log: log, tokens: tokens, platform: platformConfig}
	return api, nil
}

// Initialize is called by Bootstrap before the host installs routes.
func (a *API) Initialize(ctx context.Context) error {
	opts, err := redis.ParseURL(a.cfg.Redis.URL)
	if err != nil {
		return err
	}
	store, err := durable.Open(ctx, a.cfg.Database.URL)
	if err != nil {
		return err
	}
	a.store = store
	a.redis = redis.NewClient(opts)
	if a.platform != nil {
		a.tenant = client.NewTenant(a.platform.TenantURL, a.platform.TenantToken)
	}
	if a.platform != nil && a.platform.CodingURL != "" {
		a.coding, err = client.NewCodingEnvironment(a.platform.CodingURL, a.platform.CodingToken)
		if err != nil {
			if a.tenant != nil {
				a.tenant.Close()
			}
			_ = a.redis.Close()
			a.store.Close()
			return err
		}
	}

	return nil
}

// Close runs after HTTP draining and Stop have finished.
func (a *API) Close(ctx context.Context) error {
	if err := a.Stop(ctx); err != nil {
		return err
	}
	if a.tenant != nil {
		a.tenant.Close()
	}
	if a.coding != nil {
		a.coding.Close()
	}
	var err error
	if a.redis != nil {
		err = a.redis.Close()
	}
	if a.store != nil {
		a.store.Close()
	}
	return err
}

func (a *API) RegisterRoutes(router *gin.RouterGroup) error {
	if a.store == nil || a.redis == nil {
		return errors.New("API resources are not initialized")
	}
	registerRoutes(router, a.store, a.redis, a.cfg.Auth.ConfigOwners, a.tokens, a.platform, a.tenant)
	return nil
}

func (a *API) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	a.done = make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		a.store.Relay(ctx, a.redis, func(err error) { a.log.Errorf(ctx, "outbox publish failed: %v", err) })
	}()
	go func() {
		defer workers.Done()
		a.reconcile(ctx, "evaluation", a.store.ReconcileEvaluations)
	}()
	if a.coding != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			a.reconcile(
				ctx,
				"coding evaluation",
				func(ctx context.Context) error { return a.store.ReconcileCodingEvaluations(ctx, a.coding) },
			)
		}()
	}
	go func() { workers.Wait(); close(a.done) }()
	return nil
}

func (a *API) reconcile(ctx context.Context, name string, run func(context.Context) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := run(ctx); err != nil && ctx.Err() == nil {
			a.log.Errorf(ctx, "%s reconcile failed: %v", name, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) Stop(ctx context.Context) error {
	if a.cancel == nil {
		return nil
	}
	a.cancel()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

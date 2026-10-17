// Package service assembles the API and its server-go lifecycle.
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

var _ host.Service = (*API)(nil)

type API struct {
	routes  *routes
	log     *logger.Logger
	tenant  *client.Tenant
	stopped bool
	coding  *client.CodingEnvironment
	store   *durable.Store
	redis   *redis.Client
	cancel  context.CancelFunc
	done    chan struct{}
}

// New constructs an API using caller-owned resources. Stop joins background tasks;
// the caller releases Store and Redis after Stop succeeds. The API owns its
// platform clients and releases them after its tasks stop.
func New(cfg Config, log *logger.Logger, store *durable.Store, cache *redis.Client) (*API, error) {
	if err := cfg.ValidateAPI(); err != nil {
		return nil, err
	}
	if log == nil || store == nil || cache == nil {
		return nil, errors.New("API requires logger, store and Redis")
	}
	platformConfig, err := cfg.ProjectPlatform()
	if err != nil {
		return nil, err
	}
	tokens, err := auth.ParseBearerTokens(cfg.Auth.TokensJSON)
	if err != nil {
		return nil, err
	}
	a := &API{log: log, store: store, redis: cache}
	if platformConfig != nil {
		a.tenant = client.NewTenant(platformConfig.TenantURL, platformConfig.TenantToken)
		if platformConfig.CodingURL != "" {
			a.coding, err = client.NewCodingEnvironment(
				platformConfig.CodingURL,
				platformConfig.CodingToken,
			)
			if err != nil {
				a.tenant.Close()
				return nil, err
			}
		}
	}
	a.routes = newRoutes(store, cache, cfg.Auth.ConfigOwners, tokens, platformConfig, a.tenant)
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
		a.closeClients()
		return nil
	}
	a.cancel()
	select {
	case <-a.done:
		a.closeClients()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *API) closeClients() {
	a.stopped = true
	if a.tenant != nil {
		a.tenant.Close()
		a.tenant = nil
	}
	if a.coding != nil {
		a.coding.Close()
		a.coding = nil
	}
}

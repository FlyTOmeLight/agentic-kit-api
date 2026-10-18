// Package service assembles the API and its server-go lifecycle.
package service

import (
	"context"
	"errors"

	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

var _ host.Service = (*API)(nil)

type API struct {
	routes  *routes
	log     *logger.Logger
	stopped bool
	store   *durable.Store
	redis   frameworkredis.Redis
	cancel  context.CancelFunc
	done    chan struct{}
}

// New constructs an API using caller-owned resources. Stop joins background tasks;
// the caller releases Store and Redis after Stop succeeds.
func New(cfg Config, log *logger.Logger, store *durable.Store, cache frameworkredis.Redis) (*API, error) {
	if err := cfg.ValidateAPI(); err != nil {
		return nil, err
	}
	if log == nil || store == nil || cache == nil {
		return nil, errors.New("API requires logger, store and Redis")
	}
	a := &API{log: log, store: store, redis: cache}
	a.routes = newRoutes(store, cache)
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

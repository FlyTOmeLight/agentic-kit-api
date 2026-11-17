// Package service assembles the API and its server-go lifecycle.
package service

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	appmemory "github.com/FlyTOmeLight/agentic-kit-api/internal/app/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

var _ host.Service = (*API)(nil)

type API struct {
	routes          *routes
	log             *logger.Logger
	stopped         bool
	store           *durable.Store
	redis           frameworkredis.Redis
	cancel          context.CancelFunc
	done            chan struct{}
	ownedArchive    io.Closer
	archiveClose    sync.Once
	archiveCloseErr error
}

// New constructs an API using caller-owned resources. Stop joins background
// tasks; the caller releases Store, Redis and the archiver after Stop
// succeeds. archive may be nil when Memory archiving is not configured.
func New(
	cfg Config,
	log *logger.Logger,
	store *durable.Store,
	cache frameworkredis.Redis,
	archive appmemory.Archiver,
) (*API, error) {
	if err := cfg.ValidateAPI(); err != nil {
		return nil, err
	}
	if log == nil || store == nil || cache == nil {
		return nil, errors.New("API requires logger, store and Redis")
	}
	a := &API{log: log, store: store, redis: cache}
	a.routes = newRoutes(store, cache, archive)
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
	var tasks sync.WaitGroup
	tasks.Add(2)
	go func() {
		defer tasks.Done()
		relayOutbox(
			ctx,
			a.store,
			a.redis,
			func(err error) { a.log.Errorf(ctx, "outbox publish failed: %v", err) },
		)
	}()
	go func() {
		defer tasks.Done()
		resumePendingMemory(ctx, a.routes.memoryRecovery, func(err error) {
			a.log.Errorf(ctx, "memory pending recovery failed: %v", err)
		})
	}()
	go func() {
		tasks.Wait()
		close(a.done)
	}()
	return nil
}

func (a *API) Stop(ctx context.Context) error {
	if a.cancel == nil {
		a.stopped = true
		return a.closeArchive()
	}
	a.cancel()
	select {
	case <-a.done:
		a.stopped = true
		return a.closeArchive()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *API) closeArchive() error {
	a.archiveClose.Do(func() {
		if a.ownedArchive != nil {
			a.archiveCloseErr = a.ownedArchive.Close()
		}
	})
	return a.archiveCloseErr
}

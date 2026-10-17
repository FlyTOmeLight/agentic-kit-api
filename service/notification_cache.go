package service

import (
	"context"

	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
	"github.com/redis/go-redis/v9"
)

// notificationCache owns the Outbox notification connection. Redis is optional
// for event delivery: unlike the framework's mandatory Redis module, startup
// must allow database event replay while Redis is unavailable.
type notificationCache struct {
	config *frameworkredis.Config
	client frameworkredis.Redis
}

func (m *notificationCache) Initialize(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := frameworkredis.NewRedis(ctx, m.config)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Redis only accelerates notifications. Keep a client for reconnection and
		// let the database remain the event replay source while it is unavailable.
		client = redis.NewUniversalClient(m.config)
	}
	m.client = client
	return nil
}

func (m *notificationCache) Close(context.Context) error {
	if m.client == nil {
		return nil
	}
	err := m.client.Close()
	m.client = nil
	return err
}

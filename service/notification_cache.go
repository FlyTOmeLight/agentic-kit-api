package service

import (
	"context"

	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
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
		// Redis only accelerates notifications. The lazy client keeps a reconnecting
		// handle so startup succeeds while Redis is down and the database remains
		// the event replay source.
		client, err = frameworkredis.NewRedisLazy(m.config)
		if err != nil {
			return err
		}
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

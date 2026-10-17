package service

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// notificationCache owns the Outbox notification connection. Redis is optional
// for event delivery: unlike the framework's mandatory Redis module, startup
// must allow database event replay while Redis is unavailable.
type notificationCache struct {
	url    string
	client *redis.Client
}

func (m *notificationCache) Initialize(context.Context) error {
	options, err := redis.ParseURL(m.url)
	if err != nil {
		return err
	}
	m.client = redis.NewClient(options)
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

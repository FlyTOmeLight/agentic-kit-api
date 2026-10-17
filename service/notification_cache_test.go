package service

import (
	"context"
	"errors"
	"net"
	"testing"

	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
)

func TestNotificationCacheHonorsCancellationDuringPing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &notificationCache{config: &frameworkredis.Config{
		Addrs: []string{"unused:6379"}, MaxRetries: -1,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			cancel()
			return nil, context.Canceled
		},
	}}
	if err := cache.Initialize(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("initialization cancellation was lost", err)
	}
	if cache.client != nil {
		defer cache.Close(context.Background())
		t.Fatal("cancelled initialization published a client")
	}
}

func TestNotificationCacheAllowsOfflineRedis(t *testing.T) {
	cache := &notificationCache{config: &frameworkredis.Config{
		Addrs: []string{"127.0.0.1:1"}, DialTimeout: 1, MaxRetries: -1,
	}}
	if err := cache.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.client == nil {
		t.Fatal("offline Redis client was not retained for reconnection")
	}
	if err := cache.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationCacheHonorsCancelledInitialization(t *testing.T) {
	cache := &notificationCache{config: &frameworkredis.Config{Addrs: []string{"127.0.0.1:1"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cache.Initialize(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if cache.client != nil {
		t.Fatal("cancelled initialization published a client")
	}
}

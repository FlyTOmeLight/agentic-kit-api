package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func TestOutboxRetryAfterPublishAndRedisLoss(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL required")
	}
	s := durableTestStore(t)
	ctx := context.Background()
	r := createDurableRun(t, s)
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	defer rdb.Del(ctx, durable.StreamKey(r.ID))
	// Simulate Redis success followed by database commit failure.
	events, err := s.Events(ctx, "owner", r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(events[0])
	if err = rdb.Eval(ctx, publishLua, []string{durable.StreamKey(r.ID)}, "1-0", string(data)).Err(); err != nil {
		t.Fatal(err)
	}
	sent, err := publishOutbox(ctx, s, rdb)
	if err != nil || !sent {
		t.Fatal(err)
	}
	if n, err := rdb.XLen(ctx, durable.StreamKey(r.ID)).Result(); err != nil || n != 1 {
		t.Fatalf("duplicate publication: %d %v", n, err)
	}
	if err = rdb.Del(ctx, durable.StreamKey(r.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	events, err = s.Events(ctx, "owner", r.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatal("Redis deletion lost durable event", err)
	}
}

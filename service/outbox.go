package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// Explicit stream IDs make a retry after Redis success and database commit failure idempotent.
// The database remains the replay source, so Redis eviction never loses committed run events.
const publishLua = `
local existing=redis.call('XRANGE',KEYS[1],ARGV[1],ARGV[1])
if #existing==0 then redis.call('XADD',KEYS[1],ARGV[1],'event',ARGV[2]) end
redis.call('EXPIRE',KEYS[1],86400)
return 1`

// publishOutbox claims the next unpublished event, delivers it to Redis and marks
// it published in the same database transaction. A false result means the outbox is empty.
func publishOutbox(ctx context.Context, store *durable.Store, rdb frameworkredis.Redis) (bool, error) {
	tx, event, ok, err := store.ClaimOutbox(ctx)
	if err != nil || !ok {
		return false, err
	}
	defer tx.Rollback()
	body, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	if err = rdb.Eval(ctx, publishLua, []string{durable.StreamKey(event.RunID)}, strconv.FormatInt(event.Seq, 10)+"-0", string(body)).
		Err(); err != nil {
		return false, fmt.Errorf("publish outbox: %w", err)
	}
	if err = durable.MarkPublished(tx, *event); err != nil {
		return false, err
	}
	return true, tx.Commit().Error
}

// relayOutbox drains the outbox until the context is canceled.
func relayOutbox(ctx context.Context, store *durable.Store, rdb frameworkredis.Redis, onError func(error)) {
	for ctx.Err() == nil {
		publishCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		sent, err := publishOutbox(publishCtx, store, rdb)
		cancel()
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		if sent && err == nil {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

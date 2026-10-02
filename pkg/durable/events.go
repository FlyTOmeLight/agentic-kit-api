package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// Explicit stream IDs make a retry after Redis success / database commit failure idempotent.
// The database remains the replay source, so Redis eviction never loses committed run events.
const publishLua = `
local existing=redis.call('XRANGE',KEYS[1],ARGV[1],ARGV[1])
if #existing==0 then redis.call('XADD',KEYS[1],ARGV[1],'event',ARGV[2]) end
redis.call('EXPIRE',KEYS[1],86400)
return 1`

func StreamKey(id string) string { return "durable:run:" + id + ":events" }
func (s *Store) PublishOne(ctx context.Context, rdb *redis.Client) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// A single publisher lock preserves per-run sequence order across multiple API replicas.
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(7412102)`).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	var e Event
	err = tx.QueryRow(ctx, `SELECT run_id,seq,kind,payload FROM durable_event e WHERE NOT published AND NOT EXISTS (SELECT 1 FROM durable_event prior WHERE prior.run_id=e.run_id AND prior.seq<e.seq AND NOT prior.published) ORDER BY created_at,run_id,seq LIMIT 1 FOR UPDATE`).Scan(&e.RunID, &e.Seq, &e.Kind, &e.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	if err = rdb.Eval(ctx, publishLua, []string{StreamKey(e.RunID)}, strconv.FormatInt(e.Seq, 10)+"-0", string(b)).Err(); err != nil {
		return false, fmt.Errorf("publish outbox: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE durable_event SET published=true WHERE run_id=$1 AND seq=$2`, e.RunID, e.Seq); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func (s *Store) Relay(ctx context.Context, rdb *redis.Client, onError func(error)) {
	for ctx.Err() == nil {
		publishCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		sent, err := s.PublishOne(publishCtx, rdb)
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

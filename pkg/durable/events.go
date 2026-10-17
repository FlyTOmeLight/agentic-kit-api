package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// Explicit stream IDs make a retry after Redis success / database commit failure idempotent.
// The database remains the replay source, so Redis eviction never loses committed run events.
const publishLua = `
local existing=redis.call('XRANGE',KEYS[1],ARGV[1],ARGV[1])
if #existing==0 then redis.call('XADD',KEYS[1],ARGV[1],'event',ARGV[2]) end
redis.call('EXPIRE',KEYS[1],86400)
return 1`

func StreamKey(id string) string { return "durable:run:" + id + ":events" }

func (s *Store) PublishOne(ctx context.Context, rdb redis.UniversalClient) (bool, error) {
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Only the earliest unpublished event of each run is eligible. Hold its row
	// lock through Redis publication and the database commit. SKIP LOCKED lets
	// other publishers advance unrelated runs without bypassing this run's head.
	var record table.Event
	earlier := tx.Model(&table.Event{}).Table("durable_event AS prior").Select("1").
		Where("prior.run_id = e.run_id AND prior.seq < e.seq AND NOT prior.published")
	err = tx.Model(&table.Event{}).Table("durable_event AS e").Select("e.*").
		Where("NOT e.published").Where("NOT EXISTS (?)", earlier).
		Order("e.created_at, e.run_id, e.seq").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "e"}, Options: "SKIP LOCKED"}).
		Take(&record).Error
	e := eventFromRow(record)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	if err = rdb.Eval(ctx, publishLua, []string{StreamKey(e.RunID)}, strconv.FormatInt(e.Seq, 10)+"-0", string(b)).
		Err(); err != nil {
		return false, fmt.Errorf("publish outbox: %w", err)
	}
	if err = tx.Model(&table.Event{}).
		Where("run_id = ? AND seq = ?", e.RunID, e.Seq).
		Update("published", true).
		Error; err != nil {
		return false, err
	}
	return true, tx.Commit().Error
}

func (s *Store) Relay(ctx context.Context, rdb redis.UniversalClient, onError func(error)) {
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

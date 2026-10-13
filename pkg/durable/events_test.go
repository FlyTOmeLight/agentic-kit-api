package durable

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func eventRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL required")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func checkStream(t *testing.T, ctx context.Context, rdb *redis.Client, id string, count int) {
	t.Helper()
	entries, err := rdb.XRange(ctx, StreamKey(id), "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("run %s: got %d entries, want %d", id, len(entries), count)
	}
	for i, entry := range entries {
		if entry.ID != fmt.Sprintf("%d-0", i+1) {
			t.Fatalf("run %s: out of order entry %s", id, entry.ID)
		}
		var e Event
		raw, ok := entry.Values["event"].(string)
		if !ok {
			t.Fatalf("missing event payload: %+v", entry)
		}
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		if e.RunID != id || e.Seq != int64(i+1) {
			t.Fatalf("wrong event payload: %+v", e)
		}
	}
}

func TestOutboxLockedHeadDoesNotBlockOtherRun(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%t", commit), func(t *testing.T) {
			s := testStore(t)
			rdb := eventRedis(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a := createRun(t, s)
			if err := s.Cancel(ctx, "owner", a.ID); err != nil {
				t.Fatal(err)
			} // A1, A2
			b := createRun(t, s) // B1
			defer rdb.Del(context.Background(), StreamKey(a.ID), StreamKey(b.ID))

			// Hold A1 after Redis succeeds, but before its database marker commits.
			tx := s.db.WithContext(ctx).Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var head table.Event
			if err := tx.Model(&table.Event{}).
				Where("run_id = ? AND seq = ?", a.ID, 1).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Take(&head).
				Error; err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(eventFromRow(head))
			if err != nil {
				t.Fatal(err)
			}
			if err = rdb.Eval(ctx, publishLua, []string{StreamKey(a.ID)}, "1-0", string(data)).
				Err(); err != nil {
				t.Fatal(err)
			}
			if err = tx.Model(&table.Event{}).
				Where("run_id=? AND seq=1", a.ID).
				Update("published", true).
				Error; err != nil {
				t.Fatal(err)
			}

			publishCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			sent, err := s.PublishOne(publishCtx, rdb)
			stop()
			if err != nil || !sent {
				t.Fatalf("B1 blocked by A1: sent=%t err=%v", sent, err)
			}
			checkStream(t, ctx, rdb, b.ID, 1)
			sent, err = s.PublishOne(ctx, rdb)
			if err != nil || sent {
				t.Fatalf("A2 bypassed uncommitted A1: sent=%t err=%v", sent, err)
			}
			checkStream(t, ctx, rdb, a.ID, 1)

			remaining := 2
			if commit {
				if err = tx.Commit().Error; err != nil {
					t.Fatal(err)
				}
				remaining = 1
			} else if err = tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			for i := 0; i < remaining; i++ {
				sent, err = s.PublishOne(ctx, rdb)
				if err != nil || !sent {
					t.Fatalf("resume publication: sent=%t err=%v", sent, err)
				}
			}
			checkStream(t, ctx, rdb, a.ID, 2)
			sent, err = s.PublishOne(ctx, rdb)
			if err != nil || sent {
				t.Fatalf("unexpected pending event: sent=%t err=%v", sent, err)
			}
		})
	}
}

func TestConcurrentOutboxPublishersPreservePerRunOrder(t *testing.T) {
	s := testStore(t)
	rdb := eventRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var runs []*Run
	var keys []string
	for i := 0; i < 8; i++ {
		r := createRun(t, s)
		if err := s.Cancel(ctx, "owner", r.ID); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
		keys = append(keys, StreamKey(r.ID))
	}
	defer rdb.Del(context.Background(), keys...)
	start := make(chan struct{})
	failures := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				sent, err := s.PublishOne(ctx, rdb)
				if err != nil {
					failures <- err
					return
				}
				if !sent {
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	for _, r := range runs {
		checkStream(t, ctx, rdb, r.ID, 2)
	}
	var pending int64
	if err := s.db.WithContext(ctx).Model(&table.Event{}).Where("NOT published").Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d unpublished events remain", pending)
	}
}

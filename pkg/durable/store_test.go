package durable

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + NewID()
	if err = admin.db.WithContext(ctx).Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		err := admin.db.WithContext(ctx).Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func testConversation(t *testing.T, s *Store, spec Spec) *Session {
	t.Helper()
	cfg := AgentConfig{
		SystemPrompt: spec.SystemPrompt,
		Model:        spec.Model,
		Tools:        spec.Tools,
		MaxTurns:     spec.MaxTurns,
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 5
	}
	if cfg.Model.Name == "" {
		cfg.Model.Name = "test"
	}
	if cfg.Model.BaseURL == "" {
		cfg.Model.BaseURL = "http://localhost:11434/v1"
	}
	a, err := s.CreateAgent(context.Background(), "owner", "test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(context.Background(), "owner", a.ID, a.Revision); err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateSession(context.Background(), "owner", a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func createRun(t *testing.T, s *Store) *Run {
	t.Helper()
	session := testConversation(t, s, Spec{MaxTurns: 5})
	r, _, err := s.Submit(context.Background(), "owner", session.ID, NewID(), "question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func claim(t *testing.T, s *Store, worker string) *Run {
	t.Helper()
	r, err := s.Claim(context.Background(), worker, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConcurrentClaimAndFence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := createRun(t, s)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []*Run
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Claim(ctx, NewID(), time.Minute)
			if err == nil {
				mu.Lock()
				winners = append(winners, r)
				mu.Unlock()
			} else if !errors.Is(err, ErrNoWork) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("got %d owners", len(winners))
	}
	old := winners[0]
	if err := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id = ?", r.ID).
		Update("lease_until", gorm.Expr("clock_timestamp()-interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, "replacement")
	if next.Epoch <= old.Epoch {
		t.Fatal("epoch did not increase")
	}
	if err := s.Save(
		ctx,
		old.Lease(),
		json.RawMessage(`{}`),
		Completed,
		"stale",
		"",
		"RunCompleted",
	); !errors.Is(
		err,
		ErrLeaseLost,
	) {
		t.Fatalf("stale save accepted: %v", err)
	}
	if err := s.Renew(ctx, old.Lease(), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale renewal accepted: %v", err)
	}
	if err := s.Save(
		ctx,
		next.Lease(),
		json.RawMessage(`{}`),
		Completed,
		"correct",
		"",
		"RunCompleted",
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil || got.Output != "correct" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestIdempotencyOwnershipAndCancellation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	spec := Spec{Query: "hello", MaxTurns: 2}
	key := NewID()
	session := testConversation(t, s, spec)
	r, created, err := s.Submit(ctx, "owner", session.ID, key, spec.Query, time.Hour, 3)
	if err != nil || !created {
		t.Fatal(err)
	}
	repeat, created, err := s.Submit(ctx, "owner", session.ID, key, spec.Query, time.Hour, 3)
	if err != nil || created || repeat.ID != r.ID {
		t.Fatal("submission not idempotent", err)
	}
	spec.Query = "changed"
	if _, _, err = s.Submit(ctx, "owner", session.ID, key, spec.Query, time.Hour, 3); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, "other", r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross owner read", err)
	}
	active := claim(t, s, "a")
	if err = s.Cancel(ctx, "owner", r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(
		ctx,
		active.Lease(),
		json.RawMessage(`{}`),
		Completed,
		"wrong",
		"",
		"RunCompleted",
	); !errors.Is(
		err,
		ErrLeaseLost,
	) {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, "b", time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal("canceled task reclaimed", err)
	}
}

func TestUnsafeStepAndHumanResolution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "a")
	if _, _, err := s.BeginStep(ctx, r.Lease(), "submit", "arguments", Unsafe); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginStep(ctx, r.Lease(), "submit", "arguments", Unsafe); !errors.Is(err, ErrUncertain) {
		t.Fatal("unsafe repeated", err)
	}
	state := json.RawMessage(`{"waitingStep":"submit"}`)
	if err := s.Save(ctx, r.Lease(), state, NeedsReview, "", "uncertain", "RunNeedsReview"); err != nil {
		t.Fatal(err)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "wrong", "ok", NeedsReview); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "submit", "external receipt", NeedsReview); err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, "b")
	result, cached, err := s.BeginStep(ctx, next.Lease(), "submit", "arguments", Unsafe)
	if err != nil || !cached || result != "external receipt" {
		t.Fatalf("result=%q cached=%v err=%v", result, cached, err)
	}
}

func TestResultAndEventCommitTogether(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "a")
	// Force an event insert failure: the state update MUST roll back too.
	err := s.db.WithContext(ctx).
		Exec(`CREATE FUNCTION reject_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$; CREATE TRIGGER reject_event BEFORE INSERT ON durable_event FOR EACH ROW EXECUTE FUNCTION reject_event()`).
		Error
	if err != nil {
		t.Fatal(err)
	}
	err = s.Save(ctx, r.Lease(), json.RawMessage(`{}`), Completed, "should roll back", "", "RunCompleted")
	if err == nil {
		t.Fatal("expected transaction failure")
	}
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Running || got.Output != "" {
		t.Fatalf("partial commit: %+v", got)
	}
}

func TestOutboxRetryAfterPublishAndRedisLoss(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL required")
	}
	s := testStore(t)
	ctx := context.Background()
	r := createRun(t, s)
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	defer rdb.Del(ctx, StreamKey(r.ID))
	// Simulate Redis success followed by database commit failure.
	events, err := s.Events(ctx, "owner", r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(events[0])
	if err = rdb.Eval(ctx, publishLua, []string{StreamKey(r.ID)}, "1-0", string(data)).Err(); err != nil {
		t.Fatal(err)
	}
	sent, err := s.PublishOne(ctx, rdb)
	if err != nil || !sent {
		t.Fatal(err)
	}
	if n, err := rdb.XLen(ctx, StreamKey(r.ID)).Result(); err != nil || n != 1 {
		t.Fatalf("duplicate publication: %d %v", n, err)
	}
	if err = rdb.Del(ctx, StreamKey(r.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	events, err = s.Events(ctx, "owner", r.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatal("Redis deletion lost durable event", err)
	}
}

func TestDeadlineAndAttemptLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := createRun(t, s)
	if err := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id = ?", r.ID).
		Update("deadline", gorm.Expr("clock_timestamp()-interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "a", time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil || got.Status != Failed {
		t.Fatalf("deadline not enforced: %+v %v", got, err)
	}
}

func TestSessionHistoryAndSingleActiveRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session := testConversation(t, s, Spec{})
	first, _, err := s.Submit(ctx, "owner", session.ID, "first", "first question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Submit(
		ctx,
		"owner",
		session.ID,
		"second",
		"second question",
		time.Hour,
		3,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatal("active predecessor accepted", err)
	}
	running := claim(t, s, "a")
	if running.ID != first.ID {
		t.Fatal("wrong task")
	}
	if err = s.Save(
		ctx,
		running.Lease(),
		json.RawMessage(`{}`),
		Completed,
		"first answer",
		"",
		"RunCompleted",
	); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.Submit(ctx, "owner", session.ID, "second", "second question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Spec.History) != 2 || second.Spec.History[0].Content != "first question" ||
		second.Spec.History[1].Content != "first answer" {
		t.Fatalf("history %+v", second.Spec.History)
	}
}

func TestJSONRoundTripAndRepeatedMigration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	temperature := float32(0.3)
	spec := Spec{
		AgentID:       "agent",
		ConfigVersion: "1",
		Query:         "中文问题",
		MaxTurns:      5,
		Model: ModelSpec{
			Name:        "model",
			BaseURL:     "http://localhost:11434/v1",
			Temperature: &temperature,
		},
		Tools: []ToolSpec{
			{
				Name: "lookup",
				URL:  "http://localhost:9000/tool",
				Parameters: json.RawMessage(
					`{"type":"object","properties":{"term":{"type":"string"}}}`,
				),
				Policy: ReadOnly,
			},
		},
	}
	session := testConversation(t, s, spec)
	spec.AgentID = session.AgentID
	run, _, err := s.Submit(ctx, "owner", session.ID, "request", spec.Query, time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(spec)
	gotJSON, _ := json.Marshal(got.Spec)
	var wantValue, gotValue any
	if err = json.Unmarshal(wantJSON, &wantValue); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("configuration changed: %s", gotJSON)
	}
	active := claim(t, s, "worker")
	if err = s.Save(
		ctx,
		active.Lease(),
		json.RawMessage(`{"waitingStep":"example"}`),
		Running,
		"draft",
		"draft error",
		"Progress",
	); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(ctx, active.Lease(), json.RawMessage(`{}`), Completed, "", "", "RunCompleted"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "" || got.Error != "" || got.WaitingStep != "" || got.LeaseUntil != nil {
		t.Fatalf("zero or NULL values not persisted: %+v", got)
	}
	events, err := s.Events(ctx, "owner", run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Status Status
		Output string
	}
	if err = json.Unmarshal(events[len(events)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != Completed || payload.Output != "" {
		t.Fatalf("event JSON changed: %+v", payload)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Get(canceled, "owner", run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored cancellation: %v", err)
	}
}

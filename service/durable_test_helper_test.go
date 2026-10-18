package service

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func durableTestStore(t *testing.T) *durable.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := durable.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + durable.NewID()
	if err = admin.DB().WithContext(ctx).Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := durable.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		if err := admin.DB().WithContext(ctx).Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error; err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func createDurableRun(t *testing.T, s *durable.Store) *durable.Run {
	t.Helper()
	cfg := durable.AgentConfig{
		Model:    durable.ModelSpec{Name: "test", BaseURL: "http://localhost:11434/v1"},
		MaxTurns: 5,
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
	r, _, err := s.Submit(context.Background(), "owner", session.ID, durable.NewID(), "question", time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

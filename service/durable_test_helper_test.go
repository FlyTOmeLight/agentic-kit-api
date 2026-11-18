package service

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
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
	if err = s.InitSchema(ctx); err != nil {
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
	agentRow := table.Agent{
		ID:       durable.NewID(),
		TenantID: "tenant",
		OwnerID:  "owner",
		Name:     "test",
		Draft:    cfg,
		Revision: 1,
	}
	if err := s.DB().Create(&agentRow).Error; err != nil {
		t.Fatal(err)
	}
	version := table.AgentVersion{
		AgentID:            agentRow.ID,
		Number:             1,
		ArtifactVersionID:  "fixture-version-" + agentRow.ID,
		ArtifactVersionURI: "artifact://fixture/" + agentRow.ID,
	}
	if err := s.DB().Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Model(&agentRow).
		Updates(map[string]any{"published_revision": 1, "published_version": 1}).
		Error; err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateSession(context.Background(), "owner", agentRow.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session.ID, Query: "question"})
	r, _, err := s.Accept(context.Background(), execution.SubmitCommand{
		Owner:       "owner",
		RequestKey:  durable.NewID(),
		RequestHash: durable.Hash(input),
		TTL:         time.Hour,
		MaxAttempts: 3,
		Kind:        execution.SubmitChat,
		SessionID:   session.ID,
		Query:       "question",
	}, durable.PrepareChatSubmission(cfg, agentRow.ID, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

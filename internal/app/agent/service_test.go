package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

func testService(t *testing.T) (*service, *fakeArtifactStore) {
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
	store, err := durable.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		if err := admin.DB().WithContext(ctx).Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error; err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err = store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	fake := newFakeArtifactStore()
	return NewService(store.DB(), fake).(*service), fake
}

func testConfig(prompt string) durable.AgentConfig {
	return durable.AgentConfig{
		SystemPrompt: prompt,
		Model:        durable.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
		MaxTurns:     5,
	}
}

func testIdentity() client.Identity {
	return client.Identity{TenantID: "tenant-1"}
}

func TestPublishUploadsBodyOnceAndReplaysRevision(t *testing.T) {
	s, fake := testService(t)
	ctx := context.Background()
	a, err := s.CreateAgent(ctx, "owner", "agent", testConfig("v1"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.PublishAgent(ctx, testIdentity(), "owner", a.ID, a.Revision)
	if err != nil || first.Number != 1 || first.Config.SystemPrompt != "v1" {
		t.Fatalf("first publish: %+v %v", first, err)
	}
	repeat, err := s.PublishAgent(ctx, testIdentity(), "owner", a.ID, a.Revision)
	if err != nil || repeat.Number != 1 {
		t.Fatalf("repeat publish: %+v %v", repeat, err)
	}
	if fake.publishes != 1 || fake.ensures != 1 {
		t.Fatalf("remote calls: ensures=%d publishes=%d", fake.ensures, fake.publishes)
	}
	payload, ok := fake.payloadFor("artifact-"+digest(a.ID)[:8], publishKey(a.ID, a.Revision))
	if !ok {
		t.Fatal("remote body missing")
	}
	var stored agent.AgentConfig
	if err = json.Unmarshal(payload, &stored); err != nil || stored.SystemPrompt != "v1" {
		t.Fatalf("payload: %s %v", payload, err)
	}
	var count int64
	if err = s.db.Model(&table.AgentVersion{}).
		Where("agent_id = ?", a.ID).
		Count(&count).
		Error; err != nil ||
		count != 1 {
		t.Fatalf("versions=%d err=%v", count, err)
	}
}

func TestPublishConflictLeavesLocalStateUntouched(t *testing.T) {
	s, fake := testService(t)
	ctx := context.Background()
	a, err := s.CreateAgent(ctx, "owner", "agent", testConfig("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(
		ctx,
		testIdentity(),
		"owner",
		a.ID,
		a.Revision+1,
	); !errors.Is(
		err,
		agent.ErrConflict,
	) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if fake.publishes != 0 {
		t.Fatal("remote called for a stale revision")
	}
}

func TestPublishUnavailableKeepsLocalPointer(t *testing.T) {
	s, fake := testService(t)
	ctx := context.Background()
	a, err := s.CreateAgent(ctx, "owner", "agent", testConfig("v1"))
	if err != nil {
		t.Fatal(err)
	}
	fake.failPublish.Store(true)
	if _, err = s.PublishAgent(
		ctx,
		testIdentity(),
		"owner",
		a.ID,
		a.Revision,
	); !errors.Is(
		err,
		agent.ErrUnavailable,
	) {
		t.Fatalf("want unavailable, got %v", err)
	}
	read, err := s.Agent(ctx, "owner", a.ID)
	if err != nil || read.PublishedVersion != 0 {
		t.Fatalf("local pointer moved: %+v %v", read, err)
	}
	fake.failPublish.Store(false)
	version, err := s.PublishAgent(ctx, testIdentity(), "owner", a.ID, a.Revision)
	if err != nil || version.Number != 1 {
		t.Fatalf("retry after outage: %+v %v", version, err)
	}
	if fake.publishes != 2 {
		t.Fatalf("retry must replay the same key: publishes=%d", fake.publishes)
	}
}

func TestPublishOversizedPayloadRejectedBeforeRemote(t *testing.T) {
	s, fake := testService(t)
	ctx := context.Background()
	cfg := testConfig(strings.Repeat("x", maxPayloadBytes))
	a, err := s.CreateAgent(ctx, "owner", "agent", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(ctx, testIdentity(), "owner", a.ID, a.Revision); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
	if fake.publishes != 0 || fake.ensures != 0 {
		t.Fatal("remote called for an oversized payload")
	}
}

func TestConcurrentPublishSameRevisionSingleVersion(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	a, err := s.CreateAgent(ctx, "owner", "agent", testConfig("v1"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			version, err := s.PublishAgent(ctx, testIdentity(), "owner", a.ID, a.Revision)
			if err != nil {
				failures <- err
				return
			}
			if version.Number != 1 {
				failures <- errors.New("unexpected version number")
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var count int64
	if err = s.db.Model(&table.AgentVersion{}).
		Where("agent_id = ?", a.ID).
		Count(&count).
		Error; err != nil ||
		count != 1 {
		t.Fatalf("versions=%d err=%v", count, err)
	}
}

func TestAgentVersionReadsRemoteBody(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	a, err := s.CreateAgent(ctx, "owner", "agent", testConfig("frozen"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(ctx, testIdentity(), "owner", a.ID, a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateAgent(ctx, "owner", a.ID, "agent", a.Revision, testConfig("edited")); err != nil {
		t.Fatal(err)
	}
	version, err := s.AgentVersion(ctx, testIdentity(), "owner", a.ID, 1)
	if err != nil || version.Config.SystemPrompt != "frozen" {
		t.Fatalf("version body: %+v %v", version, err)
	}
	if _, err = s.AgentVersion(ctx, testIdentity(), "other", a.ID, 1); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("owner leak: %v", err)
	}
}

func TestPublishWithoutTenantIsUnavailable(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	a, err := s.CreateAgent(ctx, "owner", "agent", testConfig("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(
		ctx,
		client.Identity{},
		"owner",
		a.ID,
		a.Revision,
	); !errors.Is(
		err,
		agent.ErrUnavailable,
	) {
		t.Fatalf("want unavailable without tenant, got %v", err)
	}
}

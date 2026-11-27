package durable

// Test fixtures for Agent assets. Production asset behavior moved to
// internal/app/agent with remote version bodies; execution tests only need
// consistent local rows, so these helpers write them directly and never
// touch artifact.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// createAgentFixture inserts an agent with the given draft; revision is 1.
func createAgentFixture(t *testing.T, s *Store, owner, name string, config AgentConfig) *table.Agent {
	t.Helper()
	row := table.Agent{ID: NewID(), OwnerID: owner, Name: name, Draft: config, Revision: 1}
	if err := s.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	fixtureVersionConfigs.Store(row.ID, map[int64]AgentConfig{})
	return &row
}

// fixtureVersionConfigs keeps the config body each fixture publish used so
// tests can mirror the production prefetch without an artifact store.
var fixtureVersionConfigs sync.Map // agentID → map[int64]AgentConfig

// fixtureVersionConfig returns the config published as the given version.
func fixtureVersionConfig(t *testing.T, agentID string, version int64) AgentConfig {
	t.Helper()
	versions, ok := fixtureVersionConfigs.Load(agentID)
	if !ok {
		t.Fatalf("no fixture versions for agent %s", agentID)
	}
	cfg, ok := versions.(map[int64]AgentConfig)[version]
	if !ok {
		t.Fatalf("no fixture config for agent %s version %d", agentID, version)
	}
	return cfg
}

// updateAgentFixture replaces the draft and bumps the revision.
func updateAgentFixture(
	t *testing.T,
	s *Store,
	owner, id, name string,
	revision int64,
	config AgentConfig,
) *table.Agent {
	t.Helper()
	var row table.Agent
	if err := s.DB().Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Revision != revision {
		t.Fatalf("revision = %d, want %d", row.Revision, revision)
	}
	if err := s.DB().Model(&row).
		Select("name", "draft", "revision").
		Updates(&table.Agent{Name: name, Draft: config, Revision: revision + 1}).
		Error; err != nil {
		t.Fatal(err)
	}
	return &row
}

// publishAgentFixture advances the published pointers and inserts the local
// version row with a placeholder artifact identity.
func publishAgentFixture(t *testing.T, s *Store, owner, id string, revision int64) *table.AgentVersion {
	t.Helper()
	var row table.Agent
	if err := s.DB().Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Revision != revision {
		t.Fatalf("revision = %d, want %d", row.Revision, revision)
	}
	if row.PublishedRevision == revision {
		var existing table.AgentVersion
		if err := s.DB().
			Where("agent_id = ? AND number = ?", id, row.PublishedVersion).
			Take(&existing).
			Error; err != nil {
			t.Fatal(err)
		}
		return &existing
	}
	version := table.AgentVersion{
		AgentID:            id,
		Number:             row.PublishedVersion + 1,
		ArtifactVersionID:  fmt.Sprintf("fixture-version-%s-%d", id, revision),
		ArtifactVersionURI: fmt.Sprintf("artifact://fixture/%s/%d", id, revision),
	}
	if err := s.DB().Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	versions, _ := fixtureVersionConfigs.Load(id)
	configs := versions.(map[int64]AgentConfig)
	configs[version.Number] = row.Draft
	fixtureVersionConfigs.Store(id, configs)
	if err := s.DB().Model(&row).
		Updates(map[string]any{"published_revision": revision, "published_version": version.Number}).
		Error; err != nil {
		t.Fatal(err)
	}
	return &version
}

// fixtureConfig derives the published config used by the fixture helpers;
// zero fields fall back to the same defaults the removed test helper used.
func fixtureConfig(spec Spec) AgentConfig {
	cfg := AgentConfig{
		SystemPrompt: spec.SystemPrompt,
		Model:        spec.Model,
		Tools:        spec.Tools,
		MCPServers:   spec.MCPServers,
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
	return cfg
}

// chatSessionFixture creates an agent, publishes it and opens a chat session.
func chatSessionFixture(t *testing.T, s *Store, spec Spec) (*Session, AgentConfig) {
	t.Helper()
	cfg := fixtureConfig(spec)
	agentRow := createAgentFixture(t, s, "owner", "test", cfg)
	publishAgentFixture(t, s, "owner", agentRow.ID, agentRow.Revision)
	session, err := s.CreateSession(context.Background(), "owner", agentRow.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return session, cfg
}

// submitFixture accepts a chat run with an inline published config.
func submitFixture(
	t *testing.T,
	s *Store,
	owner, sessionID, key, query string,
	ttl time.Duration,
	maxAttempts int,
	config AgentConfig,
) (*Run, bool, error) {
	t.Helper()
	agentID, version := sessionPublishedFixture(t, s, sessionID)
	input, _ := json.Marshal(struct{ Session, Query string }{Session: sessionID, Query: query})
	return s.Accept(context.Background(), execution.SubmitCommand{
		Owner:       owner,
		RequestKey:  key,
		RequestHash: Hash(input),
		TTL:         ttl,
		MaxAttempts: maxAttempts,
		Kind:        execution.SubmitChat,
		SessionID:   sessionID,
		Query:       query,
	}, PrepareChatSubmission(config, agentID, version, nil))
}

// sessionPublishedFixture resolves the agent and published version bound to
// a session, mirroring the production prefetch; unknown sessions yield empty
// values so Accept reports its own ownership error.
func sessionPublishedFixture(t *testing.T, s *Store, sessionID string) (string, int64) {
	t.Helper()
	var session table.Session
	if err := s.DB().Where("id = ?", sessionID).Take(&session).Error; err != nil {
		return "", 0
	}
	var agentRow table.Agent
	if err := s.DB().Where("id = ?", session.AgentID).Take(&agentRow).Error; err != nil {
		return session.AgentID, 0
	}
	return agentRow.ID, agentRow.PublishedVersion
}

// submitExecutionFixture accepts an application run with an inline config.
func submitExecutionFixture(
	t *testing.T,
	s *Store,
	owner, key string,
	spec execution.ExecutionSpec,
	ttl time.Duration,
	config AgentConfig,
) (*Run, bool, error) {
	t.Helper()
	input, _ := json.Marshal(struct {
		Spec execution.ExecutionSpec
		TTL  time.Duration
	}{Spec: spec, TTL: ttl})
	return s.Accept(context.Background(), execution.SubmitCommand{
		Owner:         owner,
		RequestKey:    key,
		RequestHash:   Hash(input),
		TTL:           ttl,
		MaxAttempts:   5,
		Kind:          execution.SubmitApplication,
		ExecutionSpec: spec,
		GoalID:        spec.GoalID,
	}, PrepareApplicationSubmission(config, spec.AgentID, spec.AgentVersion, nil))
}

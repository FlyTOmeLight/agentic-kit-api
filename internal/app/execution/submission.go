package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// Service accepts chat and application runs through the shared durable algorithm.
type Service struct {
	store    *durable.Store
	artifact client.ArtifactStore
	recaller durable.MemoryRecaller

	collections sync.Map // agentID → artifact collection ID
}

func NewService(store *durable.Store, artifact client.ArtifactStore) *Service {
	return &Service{store: store, artifact: artifact}
}

// WithMemoryRecaller wires recall memory into the accept transaction when a
// submission selects Recall. Call it during construction, before serving.
func (s *Service) WithMemoryRecaller(recaller durable.MemoryRecaller) *Service {
	s.recaller = recaller
	return s
}

func (s *Service) SubmitChat(
	ctx context.Context,
	identity client.Identity,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (*execpkg.Run, bool, error) {
	cmd, prepare, replay, err := s.prepareChat(
		ctx, identity, owner, session, key, query, ttl, maxAttempts, memorySelection, filter,
	)
	if err != nil || replay != nil {
		return replay, false, err
	}
	return s.store.Accept(ctx, cmd, prepare)
}

func (s *Service) prepareChat(
	ctx context.Context,
	identity client.Identity,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (execpkg.SubmitCommand, durable.PrepareSubmission, *execpkg.Run, error) {
	hash := chatRequestHash(session, query, memorySelection, filter)
	if run, err := s.replay(ctx, owner, key, hash); err != nil || run != nil {
		return execpkg.SubmitCommand{}, nil, run, err
	}
	db := s.store.DB().WithContext(ctx)
	conversation, err := table.GetSession(db, owner, session)
	if err != nil {
		return execpkg.SubmitCommand{}, nil, nil, recordError(err)
	}
	agentRow, err := table.GetAgent(db, owner, conversation.AgentID)
	if err != nil {
		return execpkg.SubmitCommand{}, nil, nil, recordError(err)
	}
	if agentRow.PublishedVersion == 0 {
		return execpkg.SubmitCommand{}, nil, nil, agent.ErrConflict
	}
	config, err := s.publishedConfig(ctx, identity, agentRow.ID, agentRow.PublishedVersion)
	if err != nil {
		return execpkg.SubmitCommand{}, nil, nil, err
	}
	cmd := execpkg.SubmitCommand{
		Owner:        owner,
		RequestKey:   key,
		RequestHash:  hash,
		TTL:          ttl,
		MaxAttempts:  maxAttempts,
		Kind:         execpkg.SubmitChat,
		SessionID:    session,
		Query:        query,
		Memory:       normalizeMemorySelection(memorySelection),
		MemoryFilter: filter,
	}
	return cmd, durable.PrepareChatSubmission(config, agentRow.ID, agentRow.PublishedVersion, s.recaller), nil, nil
}

func (s *Service) SubmitApplication(
	ctx context.Context,
	identity client.Identity,
	owner, key string,
	spec execpkg.ExecutionSpec,
	ttl time.Duration,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (*execpkg.Run, bool, error) {
	hash := applicationRequestHash(spec, ttl, memorySelection, filter)
	if run, err := s.replay(ctx, owner, key, hash); err != nil || run != nil {
		return run, false, err
	}
	db := s.store.DB().WithContext(ctx)
	agentRow, err := table.GetAgent(db, owner, spec.AgentID)
	if err != nil {
		return nil, false, recordError(err)
	}
	config, err := s.publishedConfig(ctx, identity, spec.AgentID, spec.AgentVersion)
	if err != nil {
		return nil, false, err
	}
	cmd := execpkg.SubmitCommand{
		Owner:         owner,
		RequestKey:    key,
		RequestHash:   hash,
		TTL:           ttl,
		MaxAttempts:   5,
		Kind:          execpkg.SubmitApplication,
		ExecutionSpec: spec,
		GoalID:        spec.GoalID,
		Query:         spec.Query,
		Memory:        normalizeMemorySelection(memorySelection),
		MemoryFilter:  filter,
	}
	return s.store.Accept(
		ctx,
		cmd,
		durable.PrepareApplicationSubmission(config, agentRow.ID, spec.AgentVersion, s.recaller),
	)
}

func normalizeMemorySelection(selection memory.Selection) memory.Selection {
	if selection == "" {
		return memory.SelectionOff
	}
	return selection
}

func emptyRecallFilter(filter memory.RecallFilter) bool {
	return len(filter.Terms) == 0 && len(filter.Tags) == 0 &&
		filter.MaxEntries == 0 && filter.MaxBytes == 0
}

func chatRequestHash(session, query string, selection memory.Selection, filter memory.RecallFilter) string {
	selection = normalizeMemorySelection(selection)
	if selection == memory.SelectionOff && emptyRecallFilter(filter) {
		input, _ := json.Marshal(struct{ Session, Query string }{Session: session, Query: query})
		return durable.Hash(input)
	}
	input, _ := json.Marshal(struct {
		Session string
		Query   string
		Memory  memory.Selection
		Filter  memory.RecallFilter
	}{Session: session, Query: query, Memory: selection, Filter: filter})
	return durable.Hash(input)
}

func applicationRequestHash(spec execpkg.ExecutionSpec, ttl time.Duration, selection memory.Selection, filter memory.RecallFilter) string {
	selection = normalizeMemorySelection(selection)
	if selection == memory.SelectionOff && emptyRecallFilter(filter) {
		input, _ := json.Marshal(struct {
			Spec execpkg.ExecutionSpec
			TTL  time.Duration
		}{Spec: spec, TTL: ttl})
		return durable.Hash(input)
	}
	input, _ := json.Marshal(struct {
		Spec   execpkg.ExecutionSpec
		TTL    time.Duration
		Memory memory.Selection
		Filter memory.RecallFilter
	}{Spec: spec, TTL: ttl, Memory: selection, Filter: filter})
	return durable.Hash(input)
}

// replay returns the accepted run for an idempotent retry without touching
// the artifact store; the authoritative in-transaction recheck still runs.
func (s *Service) replay(ctx context.Context, owner, key, hash string) (*execpkg.Run, error) {
	var row table.Run
	err := s.store.DB().WithContext(ctx).
		Where("owner_id = ? AND request_key = ?", owner, key).
		Take(&row).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.RequestHash != hash {
		return nil, durable.ErrConflict
	}
	return durable.RunFromRow(row), nil
}

// publishedConfig resolves the immutable version body through the local
// pointer and the artifact store.
func (s *Service) publishedConfig(
	ctx context.Context,
	identity client.Identity,
	agentID string,
	number int64,
) (agent.AgentConfig, error) {
	version, err := table.GetVersion(s.store.DB().WithContext(ctx), agentID, number)
	if err != nil {
		return agent.AgentConfig{}, recordError(err)
	}
	artifactID, err := s.collection(ctx, identity, agentID)
	if err != nil {
		return agent.AgentConfig{}, err
	}
	payload, err := s.artifact.GetPayload(ctx, identity, artifactID, version.ArtifactVersionID)
	if err != nil {
		return agent.AgentConfig{}, err
	}
	var config agent.AgentConfig
	if err = json.Unmarshal(payload, &config); err != nil {
		return agent.AgentConfig{}, err
	}
	return config, nil
}

func (s *Service) collection(ctx context.Context, identity client.Identity, agentID string) (string, error) {
	if cached, ok := s.collections.Load(agentID); ok {
		return cached.(string), nil
	}
	id, err := s.artifact.EnsureCollection(ctx, identity, agentID)
	if err != nil {
		return "", err
	}
	cached, _ := s.collections.LoadOrStore(agentID, id)
	return cached.(string), nil
}

func recordError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agent.ErrNotFound
	}
	return err
}

func (s *Service) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	return s.store.CheckExecutionLease(ctx, owner, id, epoch)
}

func (s *Service) AuthorizeExecution(
	ctx context.Context,
	owner, id, workerID string,
	epoch int64,
) (*execpkg.RunAuthorization, error) {
	return s.store.AuthorizeExecution(ctx, owner, id, workerID, epoch)
}

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
)

// Service accepts chat and application runs through the shared durable algorithm.
type Service struct {
	store    *durable.Store
	artifact client.ArtifactStore

	collections sync.Map // agentID → artifact collection ID
}

func NewService(store *durable.Store, artifact client.ArtifactStore) *Service {
	return &Service{store: store, artifact: artifact}
}

func (s *Service) SubmitChat(
	ctx context.Context,
	identity client.Identity,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
) (*execpkg.Run, bool, error) {
	cmd, prepare, replay, err := s.prepareChat(ctx, identity, owner, session, key, query, ttl, maxAttempts)
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
) (execpkg.SubmitCommand, durable.PrepareSubmission, *execpkg.Run, error) {
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session, Query: query})
	hash := durable.Hash(input)
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
		Owner:       owner,
		RequestKey:  key,
		RequestHash: hash,
		TTL:         ttl,
		MaxAttempts: maxAttempts,
		Kind:        execpkg.SubmitChat,
		SessionID:   session,
		Query:       query,
	}
	return cmd, durable.PrepareChatSubmission(config, agentRow.ID, agentRow.PublishedVersion), nil, nil
}

func (s *Service) SubmitApplication(
	ctx context.Context,
	identity client.Identity,
	owner, key string,
	spec execpkg.ExecutionSpec,
	ttl time.Duration,
) (*execpkg.Run, bool, error) {
	input, _ := json.Marshal(struct {
		Spec execpkg.ExecutionSpec
		TTL  time.Duration
	}{Spec: spec, TTL: ttl})
	hash := durable.Hash(input)
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
	}
	return s.store.Accept(ctx, cmd, durable.PrepareApplicationSubmission(config, agentRow.ID, spec.AgentVersion))
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

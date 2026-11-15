package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// Service accepts chat and application runs through the shared durable algorithm.
type Service struct {
	store    *durable.Store
	recaller durable.MemoryRecaller
}

func NewService(store *durable.Store) *Service {
	return &Service{store: store}
}

// WithMemoryRecaller returns the same service wired to recall memory inside
// the accept transaction when a submission selects Recall.
func (s *Service) WithMemoryRecaller(recaller durable.MemoryRecaller) *Service {
	return &Service{store: s.store, recaller: recaller}
}

func (s *Service) SubmitChat(
	ctx context.Context,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
	memorySelection memory.Selection,
	filter memory.RecallFilter,
) (*execpkg.Run, bool, error) {
	input, _ := json.Marshal(struct {
		Session string
		Query   string
		Memory  memory.Selection
	}{Session: session, Query: query, Memory: memorySelection})
	return s.store.Accept(ctx, execpkg.SubmitCommand{
		Owner:        owner,
		RequestKey:   key,
		RequestHash:  durable.Hash(input),
		TTL:          ttl,
		MaxAttempts:  maxAttempts,
		Kind:         execpkg.SubmitChat,
		SessionID:    session,
		Query:        query,
		Memory:       memorySelection,
		MemoryFilter: filter,
	}, durable.PrepareChatSubmissionWith(s.recaller))
}

func (s *Service) SubmitApplication(
	ctx context.Context,
	owner, key string,
	spec execpkg.ExecutionSpec,
	ttl time.Duration,
) (*execpkg.Run, bool, error) {
	input, _ := json.Marshal(struct {
		Spec execpkg.ExecutionSpec
		TTL  time.Duration
	}{Spec: spec, TTL: ttl})
	return s.store.Accept(ctx, execpkg.SubmitCommand{
		Owner:         owner,
		RequestKey:    key,
		RequestHash:   durable.Hash(input),
		TTL:           ttl,
		MaxAttempts:   5,
		Kind:          execpkg.SubmitApplication,
		ExecutionSpec: spec,
	}, durable.PrepareApplicationSubmissionWith(s.recaller))
}

func (s *Service) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	return s.store.CheckExecutionLease(ctx, owner, id, epoch)
}

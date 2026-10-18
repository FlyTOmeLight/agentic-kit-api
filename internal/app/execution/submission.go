package execution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// Service accepts chat and application runs through the shared durable algorithm.
type Service struct {
	store *durable.Store
}

func NewService(store *durable.Store) *Service {
	return &Service{store: store}
}

func (s *Service) SubmitChat(
	ctx context.Context,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
) (*execpkg.Run, bool, error) {
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session, Query: query})
	return s.store.Accept(ctx, execpkg.SubmitCommand{
		Owner:       owner,
		RequestKey:  key,
		RequestHash: durable.Hash(input),
		TTL:         ttl,
		MaxAttempts: maxAttempts,
		Kind:        execpkg.SubmitChat,
		SessionID:   session,
		Query:       query,
	}, durable.PrepareChatSubmission)
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
	}, durable.PrepareApplicationSubmission)
}

func (s *Service) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	return s.store.CheckExecutionLease(ctx, owner, id, epoch)
}

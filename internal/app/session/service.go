package session

import (
	"context"
	"time"

	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// Service is the transactional behavior required by this HTTP module.
type Service interface {
	CreateSession(ctx context.Context, owner, agent, title string) (*execpkg.Session, error)
	Session(ctx context.Context, owner, id string) (*execpkg.Session, error)
	Sessions(ctx context.Context, owner, agent, after string, limit int) ([]execpkg.Session, error)
	Messages(ctx context.Context, owner, session string, after int64, limit int) ([]execpkg.SessionMessage, error)
	SessionRuns(ctx context.Context, owner, session, after string, limit int) ([]execpkg.Run, error)
	Submit(
		ctx context.Context,
		owner, session, key, query string,
		ttl time.Duration,
		maxAttempts int,
	) (*execpkg.Run, bool, error)
}

type service struct {
	store *durable.Store
	exec  *appexec.Service
}

func NewService(store *durable.Store, exec *appexec.Service) Service {
	return &service{store: store, exec: exec}
}

func (s *service) CreateSession(ctx context.Context, owner, agent, title string) (*execpkg.Session, error) {
	return s.store.CreateSession(ctx, owner, agent, title)
}

func (s *service) Session(ctx context.Context, owner, id string) (*execpkg.Session, error) {
	return s.store.Session(ctx, owner, id)
}

func (s *service) Sessions(ctx context.Context, owner, agent, after string, limit int) ([]execpkg.Session, error) {
	return s.store.Sessions(ctx, owner, agent, after, limit)
}

func (s *service) Messages(
	ctx context.Context,
	owner, session string,
	after int64,
	limit int,
) ([]execpkg.SessionMessage, error) {
	return s.store.Messages(ctx, owner, session, after, limit)
}

func (s *service) SessionRuns(ctx context.Context, owner, session, after string, limit int) ([]execpkg.Run, error) {
	return s.store.SessionRuns(ctx, owner, session, after, limit)
}

func (s *service) Submit(
	ctx context.Context,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
) (*execpkg.Run, bool, error) {
	return s.exec.SubmitChat(ctx, owner, session, key, query, ttl, maxAttempts)
}

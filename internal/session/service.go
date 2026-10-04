package session

import (
	"context"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// Service is the transactional behavior required by this HTTP module.
type Service interface {
	CreateSession(ctx context.Context, owner, agent, title string) (*durable.Session, error)
	Session(ctx context.Context, owner, id string) (*durable.Session, error)
	Sessions(ctx context.Context, owner, agent, after string, limit int) ([]durable.Session, error)
	Messages(ctx context.Context, owner, session string, after int64, limit int) ([]durable.SessionMessage, error)
	SessionRuns(ctx context.Context, owner, session, after string, limit int) ([]durable.Run, error)
	Submit(ctx context.Context, owner, session, key, query string, ttl time.Duration, maxAttempts int) (*durable.Run, bool, error)
}

var _ Service = (*durable.Store)(nil)

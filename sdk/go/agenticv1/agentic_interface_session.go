package agenticv1

import "context"

// SessionAPI contains the session operations exposed over HTTP.
type SessionAPI interface {
	Create(ctx context.Context, req CreateSessionRequest) (*Session, error)
	Get(ctx context.Context, id string) (*Session, error)
	List(ctx context.Context, opts ListSessionsOptions) (*Page[Session], error)

	// SendMessage returns created=false on idempotent replay; reuse the key for retries.
	SendMessage(ctx context.Context, sessionID, key string, req SendMessageRequest) (*Run, bool, error)

	ListMessages(ctx context.Context, sessionID string, opts ListMessagesOptions) (*Page[SessionMessage], error)
	ListRuns(ctx context.Context, sessionID string, opts PageOptions) (*Page[Run], error)
}

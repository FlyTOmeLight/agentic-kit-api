package agenticv1

import "context"

// SessionAPI contains the session operations exposed over HTTP.
type SessionAPI interface {
	Create(ctx context.Context, req *CreateSessionRequest) (*Session, error)
	Get(ctx context.Context, req *GetSessionRequest) (*Session, error)
	List(ctx context.Context, req *ListSessionsRequest) (*Page[Session], error)

	// SendMessage sets response.Created=false on idempotent replay; reuse the key for retries.
	SendMessage(ctx context.Context, req *SendMessageRequest) (*SendMessageResponse, error)

	ListMessages(ctx context.Context, req *ListMessagesRequest) (*Page[SessionMessage], error)
	ListRuns(ctx context.Context, req *ListSessionRunsRequest) (*Page[Run], error)
}

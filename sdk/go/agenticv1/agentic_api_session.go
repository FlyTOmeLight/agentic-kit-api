package agenticv1

import "time"

type Session struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agentID"`
	Title      string    `json:"title"`
	MessageSeq int64     `json:"messageSeq"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type SessionMessage struct {
	SessionID string `json:"sessionID"`
	Seq       int64  `json:"seq"`
	RunID     string `json:"runID"`
	Kind      string `json:"kind"`
	Message
	CreatedAt time.Time `json:"createdAt"`
}

type CreateSessionRequest struct {
	AgentID string `json:"agentID"`
	Title   string `json:"title,omitempty"`
}

type SendMessageRequest struct {
	Query string `json:"query"`
}

type ListSessionsOptions struct {
	PageOptions
	AgentID string
}

type ListMessagesOptions struct {
	After int64
	Limit int
}

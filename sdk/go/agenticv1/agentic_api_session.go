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
	AgentID string `json:"agentID" binding:"required,max=128"`
	Title   string `json:"title,omitempty" binding:"max=200"`
}

type SendMessageRequest struct {
	SessionID      string `json:"-" uri:"id" binding:"required,max=128"`
	IdempotencyKey string `json:"-" form:"-" uri:"-" header:"Idempotency-Key" binding:"omitempty,max=128"`

	Query string `json:"query" binding:"required"`
}

type ListSessionsOptions struct {
	PageOptions
	AgentID string `json:"-" form:"agentID"`
}

type ListMessagesOptions struct {
	After int64 `json:"-" form:"after,default=0" binding:"min=0"`
	Limit int   `json:"-" form:"limit,default=50" binding:"min=1,max=200"`
}

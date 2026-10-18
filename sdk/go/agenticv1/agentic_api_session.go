package agenticv1

import "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"

type Session = execution.Session

type SessionMessage = execution.SessionMessage

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

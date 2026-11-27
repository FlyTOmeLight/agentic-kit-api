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

	Attachments  []Attachment        `json:"attachments,omitempty"`
	Query        string              `json:"query" binding:"required"`
	Memory       string              `json:"memory,omitempty" binding:"omitempty,oneof=Off Recall"`
	MemoryFilter *MemoryRecallFilter `json:"memoryFilter,omitempty"`
}

// MemoryRecallFilter narrows one explicit Recall submission. Terms must all
// match the entry text; Tags must all appear in entry conditions. MaxEntries
// and MaxBytes bound the frozen material; zero falls back to defaults.
type MemoryRecallFilter struct {
	Terms      []string `json:"terms,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	MaxEntries int      `json:"maxEntries,omitempty"`
	MaxBytes   int      `json:"maxBytes,omitempty"`
}

type ListSessionsOptions struct {
	PageOptions
	AgentID string `json:"-" form:"agentID"`
}

type ListMessagesOptions struct {
	After int64 `json:"-" form:"after,default=0" binding:"min=0"`
	Limit int   `json:"-" form:"limit,default=50" binding:"min=1,max=200"`
}

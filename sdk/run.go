package sdk

import (
	"encoding/json"
	"time"
)

type Status string

const (
	Queued               Status = "Queued"
	Running              Status = "Running"
	WaitingClarification Status = "WaitingClarification"
	NeedsReview          Status = "NeedsReview"
	Completed            Status = "Completed"
	Failed               Status = "Failed"
	Canceled             Status = "Canceled"
)

func (s Status) Terminal() bool { return s == Completed || s == Failed || s == Canceled }

type Policy string

const (
	ReadOnly   Policy = "ReadOnly"
	Idempotent Policy = "Idempotent"
	Unsafe     Policy = "Unsafe"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallID,omitempty"`
}
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ModelSpec struct {
	Temperature *float32 `json:"temperature,omitempty"`
	TopP        *float32 `json:"topP,omitempty"`
	MaxTokens   *int     `json:"maxTokens,omitempty"`
	Name        string   `json:"name"`
	BaseURL     string   `json:"baseURL"`
	APIKeyEnv   string   `json:"apiKeyEnv"`
}
type ToolSpec struct {
	Kind        string          `json:"kind,omitempty"`
	ResourceID  string          `json:"resourceID,omitempty"`
	ToolID      string          `json:"toolID,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	URL         string          `json:"url,omitempty"`
	TokenEnv    string          `json:"tokenEnv,omitempty"`
	Policy      Policy          `json:"policy"`
}
type Run struct {
	Scope         *Scope    `json:"scope,omitempty"`
	AgentID       string    `json:"agentID"`
	ConfigVersion string    `json:"configVersion"`
	ID            string    `json:"id"`
	OwnerID       string    `json:"ownerID"`
	SessionID     string    `json:"sessionID"`
	Status        Status    `json:"status"`
	Epoch         int64     `json:"epoch"`
	Deadline      time.Time `json:"deadline"`
	Attempt       int       `json:"attempt"`
	EventSeq      int64     `json:"eventSeq"`
	Output        string    `json:"output,omitempty"`
	WaitingStep   string    `json:"waitingStep,omitempty"`
	Error         string    `json:"error,omitempty"`
}
type Event struct {
	RunID   string          `json:"runID"`
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type SupplyResultRequest struct {
	StepID string `json:"stepID"`
	Result string `json:"result"`
}

package durable

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

var (
	ErrInvalid   = errors.New("invalid input")
	ErrNotFound  = errors.New("resource not found")
	ErrConflict  = errors.New("state or idempotency conflict")
	ErrLeaseLost = errors.New("execution lease lost")
	ErrUncertain = errors.New("tool outcome needs confirmation")
	ErrNoWork    = errors.New("no runnable task")
)

// Spec is an immutable snapshot. Credentials are environment references, never secret values.
type Spec struct {
	AgentID       string     `json:"agentID"`
	ConfigVersion string     `json:"configVersion"`
	Query         string     `json:"query"`
	SystemPrompt  string     `json:"systemPrompt"`
	Model         ModelSpec  `json:"model"`
	Tools         []ToolSpec `json:"tools,omitempty"`
	MaxTurns      int        `json:"maxTurns"`
	History       []Message  `json:"history,omitempty"`
}
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
	AgentID       string          `json:"agentID"`
	ConfigVersion string          `json:"configVersion"`
	ID            string          `json:"id"`
	OwnerID       string          `json:"ownerID"`
	SessionID     string          `json:"sessionID"`
	Spec          Spec            `json:"-"`
	State         json.RawMessage `json:"-"`
	Status        Status          `json:"status"`
	WorkerID      string          `json:"-"`
	Epoch         int64           `json:"epoch"`
	LeaseUntil    *time.Time      `json:"-"`
	Deadline      time.Time       `json:"deadline"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"-"`
	EventSeq      int64           `json:"eventSeq"`
	Output        string          `json:"output,omitempty"`
	WaitingStep   string          `json:"waitingStep,omitempty"`
	Error         string          `json:"error,omitempty"`
}
type Lease struct {
	RunID, WorkerID string
	Epoch           int64
}

func (r Run) Lease() Lease { return Lease{r.ID, r.WorkerID, r.Epoch} }

type Event struct {
	RunID   string          `json:"runID"`
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

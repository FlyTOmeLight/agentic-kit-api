// Package execution holds durable execution values shared by the API, worker
// and SDK: statuses, specs, runs, leases, messages and events. The package
// must not import HTTP, SDK, database or scenario implementations.
package execution

import (
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
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

// Spec is an immutable snapshot. Credentials are environment references, never secret values.
type Spec struct {
	Toolsets   []string           `json:"toolsets,omitempty"`
	Filesystem *FilesystemBinding `json:"filesystem,omitempty"`
	Scope      *workspace.Scope   `json:"scope,omitempty"`

	AgentID       string           `json:"agentID"`
	ConfigVersion string           `json:"configVersion"`
	Query         string           `json:"query"`
	SystemPrompt  string           `json:"systemPrompt"`
	Model         agent.ModelSpec  `json:"model"`
	Tools         []agent.ToolSpec `json:"tools,omitempty"`
	MaxTurns      int              `json:"maxTurns"`
	History       []Message        `json:"history,omitempty"`
}

type Run struct {
	Scope *workspace.Scope `json:"scope,omitempty"`

	AgentID         string          `json:"agentID"`
	ConfigVersion   string          `json:"configVersion"`
	ID              string          `json:"id"`
	OwnerID         string          `json:"ownerID"`
	SessionID       string          `json:"sessionID"`
	Spec            Spec            `json:"-"`
	ContextSnapshot ContextSnapshot `json:"-"`
	State           json.RawMessage `json:"-"`
	Status          Status          `json:"status"`
	WorkerID        string          `json:"-"`
	Epoch           int64           `json:"epoch"`
	LeaseUntil      *time.Time      `json:"-"`
	Deadline        time.Time       `json:"deadline"`
	Attempt         int             `json:"attempt"`
	MaxAttempts     int             `json:"-"`
	EventSeq        int64           `json:"eventSeq"`
	Output          string          `json:"output,omitempty"`
	WaitingStep     string          `json:"waitingStep,omitempty"`
	WaitingKind     InteractionKind `json:"waitingKind,omitempty"`
	Error           string          `json:"error,omitempty"`
}

// ContextSnapshot is an opaque, versioned envelope. Execution stores and
// verifies the envelope but never interprets Payload.
type ContextSnapshot struct {
	Format        string          `json:"format"`
	PolicyVersion string          `json:"policyVersion"`
	Digest        string          `json:"digest"`
	Payload       json.RawMessage `json:"payload"`
}

type Lease struct {
	RunID, WorkerID string
	Epoch           int64
}

func (r Run) Lease() Lease { return Lease{RunID: r.ID, WorkerID: r.WorkerID, Epoch: r.Epoch} }

type Event struct {
	CreatedAt time.Time       `json:"createdAt"`
	RunID     string          `json:"runID"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

// RunTrace is a replayable page of public execution events, never a checkpoint.
type RunTrace struct {
	Run         Run     `json:"run"`
	Events      []Event `json:"events"`
	LatestEvent *Event  `json:"latestEvent,omitempty"`
	NextSeq     int64   `json:"nextSeq"`
	HasMore     bool    `json:"hasMore"`
}

// Session is a conversation bound to one Agent across multiple runs.
type Session struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agentID"`
	Title      string    `json:"title"`
	MessageSeq int64     `json:"messageSeq"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// SessionMessage embeds Message so its fields flatten into the public JSON shape.
type SessionMessage struct {
	SessionID string `json:"sessionID"`
	Seq       int64  `json:"seq"`
	RunID     string `json:"runID"`
	Kind      string `json:"kind"`
	Message
	CreatedAt time.Time `json:"createdAt"`
}

// ExecutionSpec references an immutable published Agent. Applications supply
// business input and scope; they cannot replace its model, prompt or tools.
type ExecutionSpec struct {
	Filesystem   *FilesystemBinding `json:"filesystem,omitempty"`
	Scope        *workspace.Scope   `json:"scope"`
	AgentID      string             `json:"agentID"`
	AgentVersion int64              `json:"agentVersion"`
	Query        string             `json:"query"`
}

// FilesystemBinding is accepted only through the trusted execution endpoint.
// Credentials are server-side references, never values in published Agent config.
type FilesystemBinding struct {
	Provider  string `json:"provider"`
	URL       string `json:"url"`
	TokenEnv  string `json:"tokenEnv"`
	Workspace string `json:"workspace"`
}

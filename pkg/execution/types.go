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
	WaitingExternal      Status = "WaitingExternal"
	Blocked              Status = "Blocked"
)

func (s Status) Terminal() bool { return s == Completed || s == Failed || s == Canceled }

// Suspended reports whether the run is waiting for an external fact. A
// suspended run must not hold a lease or a worker process.
func (s Status) Suspended() bool {
	return s == WaitingClarification || s == NeedsReview || s == WaitingExternal
}

type AttachmentKind string

const (
	AttachmentImage    AttachmentKind = "image"
	AttachmentDocument AttachmentKind = "document"
	AttachmentAudio    AttachmentKind = "audio"
	AttachmentVideo    AttachmentKind = "video"
	AttachmentFile     AttachmentKind = "file"
)

type Attachment struct {
	ID          string         `json:"id,omitempty"`
	Kind        AttachmentKind `json:"kind"`
	URI         string         `json:"uri"`
	Name        string         `json:"name"`
	MediaType   string         `json:"mediaType"`
	SizeBytes   int64          `json:"sizeBytes,omitempty"`
	ArtifactURI string         `json:"artifactURI,omitempty"`
}

type FileArtifact struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Action    string `json:"action"`             // "written" or "edited"
	Provider  string `json:"provider,omitempty"` // "AIO" or "Sandbox"
}

type Message struct {
	Attachments []Attachment   `json:"attachments,omitempty"`
	Artifacts   []FileArtifact `json:"artifacts,omitempty"`
	Role        string         `json:"role"`
	Content     string         `json:"content"`
	ToolCalls   []ToolCall     `json:"toolCalls,omitempty"`
	ToolCallID  string         `json:"toolCallID,omitempty"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Spec is an immutable snapshot. Credentials are environment references, never secret values.
type Spec struct {
	Attachments     []Attachment     `json:"attachments,omitempty"`
	PricingFrozen   bool             `json:"pricingFrozen,omitempty"`
	PricingSnapshot *PricingSnapshot `json:"pricingSnapshot,omitempty"`

	Toolsets   []string           `json:"toolsets,omitempty"`
	Filesystem *FilesystemBinding `json:"filesystem,omitempty"`
	Scope      *workspace.Scope   `json:"scope,omitempty"`

	AgentID       string                `json:"agentID"`
	ConfigVersion string                `json:"configVersion"`
	Query         string                `json:"query"`
	SystemPrompt  string                `json:"systemPrompt"`
	Model         agent.ModelSpec       `json:"model"`
	Tools         []agent.ToolSpec      `json:"tools,omitempty"`
	MCPServers    []agent.MCPServerSpec `json:"mcpServers,omitempty"`
	MaxTurns      int                   `json:"maxTurns"`
	History       []Message             `json:"history,omitempty"`
	SelfDelegate  bool                  `json:"selfDelegate,omitempty"`
	SubAgents     []SubAgentSnapshot    `json:"subAgents,omitempty"`
}

type SubAgentSnapshot struct {
	Name            string                `json:"name"`
	Description     string                `json:"description"`
	AgentID         string                `json:"agentID"`
	ConfigVersion   string                `json:"configVersion"`
	PricingFrozen   bool                  `json:"pricingFrozen,omitempty"`
	PricingSnapshot *PricingSnapshot      `json:"pricingSnapshot,omitempty"`
	Toolsets        []string              `json:"toolsets,omitempty"`
	SystemPrompt    string                `json:"systemPrompt"`
	Model           agent.ModelSpec       `json:"model"`
	Tools           []agent.ToolSpec      `json:"tools,omitempty"`
	MCPServers      []agent.MCPServerSpec `json:"mcpServers,omitempty"`
	MaxTurns        int                   `json:"maxTurns"`
}

type Run struct {
	Scope *workspace.Scope `json:"scope,omitempty"`

	AgentID                 string          `json:"agentID"`
	ConfigVersion           string          `json:"configVersion"`
	ID                      string          `json:"id"`
	OwnerID                 string          `json:"ownerID"`
	SessionID               string          `json:"sessionID"`
	Spec                    Spec            `json:"-"`
	ContextSnapshot         ContextSnapshot `json:"-"`
	State                   json.RawMessage `json:"-"`
	Status                  Status          `json:"status"`
	WorkerID                string          `json:"-"`
	Epoch                   int64           `json:"epoch"`
	LeaseUntil              *time.Time      `json:"-"`
	EvaluationDeadline      *time.Time      `json:"-"`
	EvaluationWorkerID      string          `json:"-"`
	EvaluationClaimWorkerID string          `json:"-"`
	EvaluationEpoch         int64           `json:"-"`
	EvaluationLeaseUntil    *time.Time      `json:"-"`
	Deadline                time.Time       `json:"deadline"`
	Attempt                 int             `json:"attempt"`
	MaxAttempts             int             `json:"-"`
	EventSeq                int64           `json:"eventSeq"`
	Output                  string          `json:"output,omitempty"`
	WaitingStep             string          `json:"waitingStep,omitempty"`
	WaitingKind             InteractionKind `json:"waitingKind,omitempty"`
	WaitingExternalRef      string          `json:"waitingExternalRef,omitempty"`

	GoalID       string `json:"goalID,omitempty"`
	ParentRunID  string `json:"parentRunID,omitempty"`
	ParentStepID string `json:"parentStepID,omitempty"`

	PendingSteer string `json:"-"`

	CompactedSummary string  `json:"-"`
	FirstKeptSeq     int64   `json:"-"`
	StallMark        string  `json:"-"`
	TokensUsed       int64   `json:"tokensUsed,omitempty"`
	CostUSD          float64 `json:"costUSD,omitempty"`
	Error            string  `json:"error,omitempty"`
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

const (
	EventModelReasoningDelta = "ModelReasoningDelta"
	EventModelContentDelta   = "ModelContentDelta"
)

type StreamDelta struct {
	RunID     string    `json:"runID"`
	Kind      string    `json:"kind"`
	Epoch     int64     `json:"epoch"`
	Turn      int64     `json:"turn"`
	Offset    int64     `json:"offset"`
	Delta     string    `json:"delta"`
	CreatedAt time.Time `json:"createdAt"`
}

// RunTrace is a replayable page of public execution events, never a checkpoint.
type RunTrace struct {
	Run         Run     `json:"run"`
	Events      []Event `json:"events"`
	LatestEvent *Event  `json:"latestEvent,omitempty"`
	NextSeq     int64   `json:"nextSeq"`
	HasMore     bool    `json:"hasMore"`
}

type GoalStatus string

const (
	GoalActive   GoalStatus = "Active"
	GoalPaused   GoalStatus = "Paused"
	GoalBlocked  GoalStatus = "Blocked"
	GoalComplete GoalStatus = "Complete"
	GoalFailed   GoalStatus = "Failed"
)

type EvaluatorKind string

const (
	EvaluatorNone    EvaluatorKind = "none"
	EvaluatorCommand EvaluatorKind = "command"
	EvaluatorHTTP    EvaluatorKind = "http"
)

// EvaluatorSpec describes how a goal is accepted. Command and HTTP
// evaluators are deterministic; no model judgement is involved.
type EvaluatorSpec struct {
	Kind   EvaluatorKind `json:"kind"`
	Config string        `json:"config,omitempty"`
}

type BudgetSpec struct {
	MaxWallClockSeconds int64   `json:"maxWallClockSeconds,omitempty"`
	MaxTokens           int64   `json:"maxTokens,omitempty"`
	MaxCostUSD          float64 `json:"maxCostUSD,omitempty"`
}

type EvaluationStatus string

const (
	EvaluationPassed EvaluationStatus = "Passed"
	EvaluationFailed EvaluationStatus = "Failed"
	EvaluationError  EvaluationStatus = "Error"
)

// Evaluation is one deterministic acceptance check for a finished run. It is
// recorded by the worker's execution backend; the control plane only reads it.
type Evaluation struct {
	ID        string           `json:"id"`
	OwnerID   string           `json:"ownerID"`
	GoalID    string           `json:"goalID"`
	RunID     string           `json:"runID"`
	Status    EvaluationStatus `json:"status"`
	Output    string           `json:"output"`
	CreatedAt time.Time        `json:"createdAt"`
}

// Goal is a business objective that may span multiple runs. The session
// keeps conversation facts; the goal owns budget, acceptance and convergence.
type Goal struct {
	ID            string        `json:"id"`
	OwnerID       string        `json:"ownerID"`
	SessionID     string        `json:"sessionID"`
	RequestKey    string        `json:"-"`
	RequestHash   string        `json:"-"`
	Objective     string        `json:"objective"`
	Evaluator     EvaluatorSpec `json:"evaluator"`
	Budget        BudgetSpec    `json:"budget"`
	Status        GoalStatus    `json:"status"`
	ActiveRunID   string        `json:"activeRunID,omitempty"`
	StallCount    int           `json:"stallCount"`
	LastStallMark string        `json:"lastStallMark,omitempty"`
	TokensUsed    int64         `json:"tokensUsed"`
	CostUSD       float64       `json:"costUSD"`
	CreatedAt     time.Time     `json:"createdAt"`
	UpdatedAt     time.Time     `json:"updatedAt"`
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
	Attachments  []Attachment       `json:"attachments,omitempty"`
	Filesystem   *FilesystemBinding `json:"filesystem,omitempty"`
	Scope        *workspace.Scope   `json:"scope"`
	AgentID      string             `json:"agentID"`
	AgentVersion int64              `json:"agentVersion"`
	Query        string             `json:"query"`

	// GoalID optionally binds the run to a durable goal. The goal must exist
	// and be Active; the run is bound atomically at accept time.
	GoalID string `json:"goalID,omitempty"`
}

// FilesystemBinding is accepted only through the trusted execution endpoint.
// Credentials are server-side references, never values in published Agent config.
type FilesystemBinding struct {
	// Provider selects the execution backend: "AIO" for the legacy direct
	// adapter, "Sandbox" for the sandbox-center control plane.
	Provider string `json:"provider"`

	URL       string `json:"url"`
	TokenEnv  string `json:"tokenEnv"`
	Workspace string `json:"workspace"`

	// ProfileID selects the sandbox-center isolation profile when Provider is
	// "Sandbox". Empty means the default coding profile.
	ProfileID string `json:"profileId,omitempty"`

	RestoreFromSandboxID string `json:"restoreFromSandboxId,omitempty"`
}

// RunAuthorization is the authoritative execution lease verdict returned by
// the database clock: it binds the caller's run, worker and claim epoch and
// caps the validity window at min(lease_until, deadline).
type RunAuthorization struct {
	Allowed   bool             `json:"allowed"`
	RunID     string           `json:"runId,omitempty"`
	WorkerID  string           `json:"workerId,omitempty"`
	Epoch     int64            `json:"epoch,omitempty"`
	Scope     *workspace.Scope `json:"scope,omitempty"`
	CheckedAt time.Time        `json:"checkedAt,omitempty"`
	ExpiresAt time.Time        `json:"expiresAt,omitempty"`
}

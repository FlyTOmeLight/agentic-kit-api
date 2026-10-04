package durable

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
)

type Status = sdk.Status
type Policy = sdk.Policy

const (
	Queued               = sdk.Queued
	Running              = sdk.Running
	WaitingClarification = sdk.WaitingClarification
	NeedsReview          = sdk.NeedsReview
	Completed            = sdk.Completed
	Failed               = sdk.Failed
	Canceled             = sdk.Canceled
	ReadOnly             = sdk.ReadOnly
	Idempotent           = sdk.Idempotent
	Unsafe               = sdk.Unsafe
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
// Scope is asserted by a trusted application, never by end-user input.
type Scope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
	TaskID    string `json:"taskID"`
	RoundID   string `json:"roundID"`
}
type Spec struct {
	Scope *Scope `json:"scope,omitempty"`

	AgentID       string     `json:"agentID"`
	ConfigVersion string     `json:"configVersion"`
	Query         string     `json:"query"`
	SystemPrompt  string     `json:"systemPrompt"`
	Model         ModelSpec  `json:"model"`
	Tools         []ToolSpec `json:"tools,omitempty"`
	MaxTurns      int        `json:"maxTurns"`
	History       []Message  `json:"history,omitempty"`
}
type Message = sdk.Message
type ToolCall = sdk.ToolCall
type ModelSpec = sdk.ModelSpec
type ToolSpec = sdk.ToolSpec
type Run struct {
	Scope *Scope `json:"scope,omitempty"`

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

type Event = sdk.Event

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (r Run) Response() sdk.Run {
	return sdk.Run{ID: r.ID, AgentID: r.AgentID, ConfigVersion: r.ConfigVersion, OwnerID: r.OwnerID, SessionID: r.SessionID, Status: r.Status, Epoch: r.Epoch, Deadline: r.Deadline, Attempt: r.Attempt, EventSeq: r.EventSeq, Output: r.Output, WaitingStep: r.WaitingStep, Error: r.Error}
}

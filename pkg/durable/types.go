// Package durable is the execution store shared by the API and the worker.
// Public value names are aliases of the pure domain packages so both sides
// and the SDK describe the same values.
package durable

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

type Status = execution.Status

type Policy = agent.Policy

type Scope = workspace.Scope

type Message = execution.Message

type ToolCall = execution.ToolCall

type ModelSpec = agent.ModelSpec

type ToolSpec = agent.ToolSpec

type AgentConfig = agent.AgentConfig

type Agent = agent.Agent

type AgentVersion = agent.AgentVersion

type Session = execution.Session

type SessionMessage = execution.SessionMessage

type Spec = execution.Spec

type Run = execution.Run

type ContextSnapshot = execution.ContextSnapshot

type Lease = execution.Lease

type Event = execution.Event

type RunTrace = execution.RunTrace

type ExecutionSpec = execution.ExecutionSpec

type FilesystemBinding = execution.FilesystemBinding

type Boundary = execution.Boundary

type BoundaryKind = execution.BoundaryKind

type InteractionKind = execution.InteractionKind

type StepIntent = execution.StepIntent

type StepDecision = execution.StepDecision

type StepOutcome = execution.StepOutcome

const (
	BoundaryModelStarted       = execution.BoundaryModelStarted
	BoundaryComplete           = execution.BoundaryComplete
	BoundaryFail               = execution.BoundaryFail
	BoundaryAwaitClarification = execution.BoundaryAwaitClarification
	BoundaryAwaitReview        = execution.BoundaryAwaitReview
	InteractionClarification   = execution.InteractionClarification
	InteractionReview          = execution.InteractionReview
	StepExecute                = execution.StepExecute
	StepReplay                 = execution.StepReplay
	StepUncertain              = execution.StepUncertain
)

const (
	Queued               = execution.Queued
	Running              = execution.Running
	WaitingClarification = execution.WaitingClarification
	NeedsReview          = execution.NeedsReview
	Completed            = execution.Completed
	Failed               = execution.Failed
	Canceled             = execution.Canceled
	ReadOnly             = agent.ReadOnly
	Idempotent           = agent.Idempotent
	Unsafe               = agent.Unsafe
)

var (
	ErrInvalid   = execution.ErrInvalid
	ErrNotFound  = execution.ErrNotFound
	ErrConflict  = execution.ErrConflict
	ErrLeaseLost = execution.ErrLeaseLost
	ErrNoWork    = execution.ErrNoWork
)

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

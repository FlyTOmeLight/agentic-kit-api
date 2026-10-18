package execution

import (
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

// SubmitKind selects how a session is resolved before prepare runs.
type SubmitKind string

const (
	SubmitChat        SubmitKind = "chat"
	SubmitApplication SubmitKind = "application"
)

// SubmitCommand is the durable accept input for chat and application entrypoints.
type SubmitCommand struct {
	Owner       string
	RequestKey  string
	RequestHash string
	TTL         time.Duration
	MaxAttempts int
	Kind        SubmitKind

	// Chat: explicit session and user query.
	SessionID string

	Query string

	// Application: immutable version and trusted scope/bindings.
	ExecutionSpec ExecutionSpec
}

// SubmissionFacts are passed to prepare after session locks succeed.
type SubmissionFacts struct {
	Owner          string
	SessionID      string
	SessionAgentID string
	Kind           SubmitKind
	Query          string
	ExecutionSpec  ExecutionSpec
}

// PreparedSubmission is the compiled snapshot ready to persist.
type PreparedSubmission struct {
	Spec        Spec
	UserMessage Message
}

// Submission is the accept result.
type Submission struct {
	Run     *Run
	Created bool
}

// BoundaryKind names a legal execution boundary. The store derives the run
// status, message and event from it; callers cannot combine them arbitrarily.
type BoundaryKind string

const (
	BoundaryModelStarted       BoundaryKind = "ModelStarted"
	BoundaryComplete           BoundaryKind = "Complete"
	BoundaryFail               BoundaryKind = "Fail"
	BoundaryAwaitClarification BoundaryKind = "AwaitClarification"
	BoundaryAwaitReview        BoundaryKind = "AwaitReview"
)

// InteractionKind distinguishes a clarification pause from a human review pause.
type InteractionKind string

const (
	InteractionClarification InteractionKind = "Clarification"
	InteractionReview        InteractionKind = "Review"
)

// Boundary is one atomic execution commit. State is the executor's opaque
// checkpoint envelope; the store never parses it.
type Boundary struct {
	Kind        BoundaryKind
	State       json.RawMessage
	Output      string
	Detail      string
	WaitingStep string
}

// StepIntent is the recorded intent of a tool call, stable across retries.
type StepIntent struct {
	ID     string
	Input  string
	Policy agent.Policy
}

// StepOutcome tells the executor whether to invoke the tool, reuse a committed
// result, or stop for human confirmation.
type StepOutcome string

const (
	StepExecute   StepOutcome = "Execute"
	StepReplay    StepOutcome = "Replay"
	StepUncertain StepOutcome = "Uncertain"
)

type StepDecision struct {
	Outcome StepOutcome
	Result  string
}

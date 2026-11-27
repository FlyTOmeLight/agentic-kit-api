package execution

import (
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// SubmitKind selects how a session is resolved before prepare runs.
type SubmitKind string

const (
	SubmitChat        SubmitKind = "chat"
	SubmitApplication SubmitKind = "application"
)

// SubmitCommand is the durable accept input for chat and application entrypoints.
type SubmitCommand struct {
	Attachments     []Attachment
	PricingSnapshot *PricingSnapshot
	Skills          []SkillSnapshot
	SubAgents       []SubAgentSnapshot
	Owner           string
	RequestKey      string
	RequestHash     string
	TTL             time.Duration
	MaxAttempts     int
	Kind            SubmitKind

	// Chat: explicit session and user query.
	SessionID string

	Query string

	// Application: immutable version and trusted scope/bindings.
	ExecutionSpec ExecutionSpec

	// GoalID optionally binds the run to a durable goal for budget tracking
	// and convergence evaluation.
	GoalID string

	Memory       memory.Selection
	MemoryFilter memory.RecallFilter
}

// SubmissionFacts are passed to prepare after session locks succeed.
type SubmissionFacts struct {
	Attachments     []Attachment
	PricingSnapshot *PricingSnapshot
	Skills          []SkillSnapshot
	SubAgents       []SubAgentSnapshot
	Owner           string
	SessionID       string
	SessionAgentID  string
	Kind            SubmitKind
	Query           string
	ExecutionSpec   ExecutionSpec
	Memory          memory.Selection
	MemoryFilter    memory.RecallFilter
}

type ChildSpawnRequest struct {
	StepID      string
	SubAgent    string
	Instruction string
	Query       string
	TTL         time.Duration
}

// PreparedSubmission is the compiled snapshot ready to persist.
type PreparedSubmission struct {
	Spec            Spec
	UserMessage     Message
	ContextSnapshot ContextSnapshot
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
	BoundaryAwaitExternal      BoundaryKind = "AwaitExternal"
	BoundaryBlock              BoundaryKind = "Block"
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
	ExternalRef string
	Context     *ContextInfo
	Turn        *int64
}

// ContextInfo exposes the observable facts about model input construction.
// It helps operators diagnose budget trimming and source omission without
// parsing the executor's opaque checkpoint state.
type ContextInfo struct {
	InputTokens          int64    `json:"inputTokens"`
	InputBytes           int64    `json:"inputBytes"`
	TokenLimit           int64    `json:"tokenLimit,omitempty"`
	ByteLimit            int64    `json:"byteLimit,omitempty"`
	OutputReserve        int64    `json:"outputReserve,omitempty"`
	OmittedSources       []Source `json:"omittedSources,omitempty"`
	CollapsedSources     []Source `json:"collapsedSources,omitempty"`
	ProjectedToolResults int      `json:"projectedToolResults,omitempty"`
	LadderPreviewed      int      `json:"ladderPreviewed,omitempty"`
	LadderStubbed        int      `json:"ladderStubbed,omitempty"`
	DroppedRounds        int      `json:"droppedRounds,omitempty"`
	UsedUsageAnchor      bool     `json:"usedUsageAnchor,omitempty"`
	ReactiveAttempt      int      `json:"reactiveAttempt,omitempty"`
	SummaryBreakerOpen   bool     `json:"summaryBreakerOpen,omitempty"`
}

// Source identifies one frozen material for stable ordering and omission
// reporting. This is a lightweight alias to avoid coupling the execution
// domain to contextbuilder types.
type Source struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision int64  `json:"revision,omitempty"`
	Version  string `json:"version,omitempty"`
}

// StepIntent is the recorded intent of a tool call, stable across retries.
type StepIntent struct {
	ID           string
	Input        string
	Policy       agent.Policy
	ExternalRef  string
	ExternalWait time.Duration
}

// StepOutcome tells the executor whether to invoke the tool, reuse a committed
// result, or stop for human confirmation.
type StepOutcome string

const (
	StepExecute      StepOutcome = "Execute"
	StepReplay       StepOutcome = "Replay"
	StepUncertain    StepOutcome = "Uncertain"
	StepExternalWait StepOutcome = "ExternalWait"
)

type StepDecision struct {
	Outcome StepOutcome
	Result  string
}

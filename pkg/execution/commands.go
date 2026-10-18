package execution

import "time"

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

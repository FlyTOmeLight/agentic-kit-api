package contextbuilder

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidInput rejects malformed messages, sources, tools or options.
	ErrInvalidInput = errors.New("invalid context builder input")
	// ErrIncompleteGroup rejects a turn whose tool calls and results do not pair.
	ErrIncompleteGroup = errors.New("incomplete message group")
	// ErrUnsupportedPolicy rejects a policy version this builder cannot apply.
	ErrUnsupportedPolicy = errors.New("unsupported context policy")
	// ErrCounterRequired rejects a token budget without a token counter.
	ErrCounterRequired = errors.New("token counter required")
	// ErrCounterVersionRequired keeps the counting policy in the frozen budget.
	ErrCounterVersionRequired = errors.New("token counter version required")
	// ErrInvalidBudget rejects negative or impossible budget values.
	ErrInvalidBudget = errors.New("invalid context budget")
	// ErrBudgetExceeded marks required or selected input above the frozen budget.
	ErrBudgetExceeded = errors.New("context budget exceeded")
	// ErrInvalidSnapshot rejects an inconsistent frozen snapshot payload.
	ErrInvalidSnapshot = errors.New("invalid context snapshot")
	// ErrUnsupportedSnapshotFormat rejects an unknown snapshot format.
	ErrUnsupportedSnapshotFormat = errors.New("unsupported context snapshot format")
	// ErrSnapshotDigest rejects a snapshot whose payload changed after encoding.
	ErrSnapshotDigest = errors.New("context snapshot digest mismatch")
)

// BudgetExceededError reports the measured input and the limits that rejected
// it. A zero limit means that dimension was not constrained.
type BudgetExceededError struct {
	InputTokens int64
	InputBytes  int64
	TokenLimit  int64
	ByteLimit   int64
}

func (e *BudgetExceededError) Error() string {
	parts := make([]string, 0, 2)
	if e.TokenLimit > 0 {
		parts = append(parts, fmt.Sprintf("tokens %d > %d", e.InputTokens, e.TokenLimit))
	}
	if e.ByteLimit > 0 {
		parts = append(parts, fmt.Sprintf("bytes %d > %d", e.InputBytes, e.ByteLimit))
	}
	return fmt.Sprintf("%s: %s", ErrBudgetExceeded, strings.Join(parts, ", "))
}

// Unwrap lets callers match the stable ErrBudgetExceeded value.
func (e *BudgetExceededError) Unwrap() error { return ErrBudgetExceeded }

// Code is the stable public failure code for this condition.
func (e *BudgetExceededError) Code() string { return "ContextBudgetExceeded" }

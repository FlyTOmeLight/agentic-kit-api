package agent

import "errors"

// The asset domain maps storage failures onto these sentinels so HTTP handlers
// can answer without depending on the artifact SDK.
var (
	ErrInvalid     = errors.New("invalid agent input")
	ErrNotFound    = errors.New("agent resource not found")
	ErrConflict    = errors.New("agent state or idempotency conflict")
	ErrUnavailable = errors.New("agent dependency unavailable")
)

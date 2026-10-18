package execution

import "errors"

var (
	ErrInvalid   = errors.New("invalid input")
	ErrNotFound  = errors.New("resource not found")
	ErrConflict  = errors.New("state or idempotency conflict")
	ErrLeaseLost = errors.New("execution lease lost")
	ErrUncertain = errors.New("tool outcome needs confirmation")
	ErrNoWork    = errors.New("no runnable task")
)

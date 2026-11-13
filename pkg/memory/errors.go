package memory

import "errors"

var (
	ErrInvalid       = errors.New("memory: invalid input")
	ErrNotFound      = errors.New("memory: entry not found")
	ErrConflict      = errors.New("memory: revision or idempotency conflict")
	ErrForbidden     = errors.New("memory: access denied")
	ErrUnavailable   = errors.New("memory: dependency unavailable")
)

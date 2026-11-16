package memory

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid                 = errors.New("memory: invalid input")
	ErrNotFound                = errors.New("memory: entry not found")
	ErrConflict                = errors.New("memory: revision or idempotency conflict")
	ErrForbidden               = errors.New("memory: access denied")
	ErrUnavailable             = errors.New("memory: dependency unavailable")
	ErrVersionUnavailable      = errors.New("memory: version unavailable")
	ErrOperationOutcomeUnknown = errors.New("memory: operation outcome unknown")

	// ErrPending reports that the operation was accepted but its remote
	// archive or local activation has not finished; retry with the same key.
	ErrPending = errors.New("memory: operation pending")
)

// VersionUnavailableError reports that an earlier publication succeeded but
// its archived body is no longer available. Ref keeps the original version
// identity so callers never replace it with a new key or collection.
type VersionUnavailableError struct {
	Ref     VersionRef
	Version string
	Cause   error
}

func (e *VersionUnavailableError) Error() string {
	if e == nil {
		return ErrVersionUnavailable.Error()
	}
	if e.Cause == nil {
		return fmt.Sprintf("memory: version %s unavailable", e.Ref.VersionID)
	}
	return fmt.Sprintf("memory: version %s unavailable: %v", e.Ref.VersionID, e.Cause)
}

func (e *VersionUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *VersionUnavailableError) Is(target error) bool {
	return target == ErrVersionUnavailable || target == ErrUnavailable
}

// Code is the stable public reason recorded by the Memory service.
func (e *VersionUnavailableError) Code() string { return "MemoryVersionUnavailable" }

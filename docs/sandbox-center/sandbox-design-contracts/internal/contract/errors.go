package contract

import "time"

type FaultCode string

const (
	FaultConflict        FaultCode = "Conflict"
	FaultNotFound        FaultCode = "NotFound"
	FaultUnsupported     FaultCode = "Unsupported"
	FaultUnavailable     FaultCode = "Unavailable"
	FaultUnauthorized    FaultCode = "Unauthorized"
	FaultInvalid         FaultCode = "Invalid"
	FaultIdentityChanged FaultCode = "IdentityChanged"
	FaultOutcomeUnknown  FaultCode = "OutcomeUnknown"
)

// SafeToRetry means the same adapter call is safe with the same operation ID.
// It never authorizes a new execution/operation ID or retrying an Unknown Start.
type PortError struct {
	Code        FaultCode
	Message     string
	SafeToRetry bool
	RetryAfter  time.Duration
	Cause       error
}

func (e *PortError) Error() string {
	return string(e.Code) + ": " + e.Message
}

func (e *PortError) Unwrap() error { return e.Cause }

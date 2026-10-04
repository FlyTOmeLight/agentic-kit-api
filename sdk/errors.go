package sdk

import "fmt"

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// APIError preserves the HTTP status and stable server error code for errors.As.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("agentic-kit HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

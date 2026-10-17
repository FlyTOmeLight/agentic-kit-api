package agenticv1

import "context"

// ExecutionAPI is restricted to trusted application identities, not browser users.
type ExecutionAPI interface {
	Submit(ctx context.Context, req *SubmitExecutionRequest) (*SubmitExecutionResponse, error)
	CheckLease(ctx context.Context, req *CheckExecutionLeaseRequest) (*LeaseCheck, error)
}

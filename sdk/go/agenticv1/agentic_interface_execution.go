package agenticv1

import "context"

// ExecutionAPI is restricted to trusted application identities, not browser users.
type ExecutionAPI interface {
	Submit(ctx context.Context, key string, request SubmitExecutionRequest) (*Run, bool, error)
	CheckLease(ctx context.Context, id string, epoch int64) (*LeaseCheck, error)
}

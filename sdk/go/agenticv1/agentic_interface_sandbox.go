package agenticv1

import "context"

// SandboxAPI is the sandbox-center control surface used by workers. Every
// mutating call is idempotent by request key; a lost response is resolved by
// replaying the same key or querying the operation, never by a blind retry
// with a new key.
type SandboxAPI interface {
	Acquire(ctx context.Context, req *AcquireSandboxRequest) (*SandboxView, error)
	Get(ctx context.Context, id string) (*SandboxView, error)
	Renew(ctx context.Context, req *RenewSandboxRequest) (*SandboxLease, error)
	Exec(ctx context.Context, req *ExecSandboxRequest) (*SandboxOperation, error)
	GetOperation(ctx context.Context, req *GetSandboxOperationRequest) (*SandboxOperation, error)
	Release(ctx context.Context, id string) (*Sandbox, error)
}

package agenticv1

import (
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

type Scope = workspace.Scope

type ExecutionSpec = execution.ExecutionSpec

type SubmitExecutionRequest struct {
	IdempotencyKey string `json:"-" form:"-" uri:"-" header:"Idempotency-Key" binding:"omitempty,max=128"`

	Spec           ExecutionSpec `json:"spec"`
	TimeoutSeconds int           `json:"timeoutSeconds" binding:"min=1,max=86400"`
}

type LeaseCheck struct {
	Allowed bool `json:"allowed"`
}

type FilesystemBinding = execution.FilesystemBinding

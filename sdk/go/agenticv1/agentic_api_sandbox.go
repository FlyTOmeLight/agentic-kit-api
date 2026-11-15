package agenticv1

import "time"

// Sandbox lifecycle status values returned by the sandbox center.
const (
	SandboxStatusProvisioning = "Provisioning"
	SandboxStatusActive       = "Active"
	SandboxStatusSaveBlocked  = "SaveBlocked"
	SandboxStatusReleasing    = "Releasing"
	SandboxStatusDestroyed    = "Destroyed"
	SandboxStatusLost         = "Lost"

	SandboxOpPending   = "Pending"
	SandboxOpSucceeded = "Succeeded"
	SandboxOpFailed    = "Failed"
	SandboxOpUnknown   = "Unknown"
)

type AcquireSandboxRequest struct {
	TenantID             string `json:"tenantId"`
	OwnerID              string `json:"ownerId"`
	RequestKey           string `json:"requestKey"`
	ProfileID            string `json:"profileId"`
	WorkerID             string `json:"workerId"`
	RunID                string `json:"runId"`
	RunEpoch             int64  `json:"runEpoch"`
	TTLSeconds           int64  `json:"ttlSeconds"`
	RequiredSave         bool   `json:"requiredSave,omitempty"`
	RestoreFromSandboxID string `json:"restoreFromSandboxId,omitempty"`
}

type Sandbox struct {
	ID                   string     `json:"id"`
	TenantID             string     `json:"tenantId"`
	OwnerID              string     `json:"ownerId"`
	RequestKey           string     `json:"requestKey"`
	ProfileID            string     `json:"profileId"`
	ProfileRevision      int64      `json:"profileRevision"`
	Status               string     `json:"status"`
	RequiredSave         bool       `json:"requiredSave"`
	RestoreFromSandboxID string     `json:"restoreFromSandboxId,omitempty"`
	ResumeCheckpoint     string     `json:"resumeCheckpoint,omitempty"`
	StoppedAt            *time.Time `json:"stoppedAt,omitempty"`
	SavedCheckpoint      string     `json:"savedCheckpoint,omitempty"`
	PodUID               string     `json:"podUid"`
	WorkspaceUID         string     `json:"workspaceUid"`
	Generation           int64      `json:"generation"`
	Version              int64      `json:"version"`
	LeaseID              string     `json:"leaseId"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	DestroyedAt          *time.Time `json:"destroyedAt,omitempty"`
}

type SandboxLease struct {
	ID                string    `json:"id"`
	SandboxID         string    `json:"sandboxId"`
	SandboxGeneration int64     `json:"sandboxGeneration"`
	HolderRunID       string    `json:"holderRunId"`
	HolderRunEpoch    int64     `json:"holderRunEpoch"`
	WorkerID          string    `json:"workerId"`
	Revision          int64     `json:"revision"`
	Status            string    `json:"status"`
	HardExpiresAt     time.Time `json:"hardExpiresAt"`
}

type SandboxView struct {
	Sandbox *Sandbox      `json:"sandbox"`
	Lease   *SandboxLease `json:"lease,omitempty"`
}

type RenewSandboxRequest struct {
	ID         string `json:"-" uri:"id"`
	Revision   int64  `json:"revision"`
	RequestKey string `json:"requestKey"`
	TTLSeconds int64  `json:"ttlSeconds"`
}

type ExecSandboxRequest struct {
	ID                 string `json:"-" uri:"id"`
	RequestKey         string `json:"requestKey"`
	Script             string `json:"script"`
	EvaluationWorkerID string `json:"evaluationWorkerId,omitempty"`
	EvaluationEpoch    int64  `json:"evaluationEpoch,omitempty"`
}

type SandboxOperation struct {
	ID              string     `json:"id"`
	LeaseID         string     `json:"leaseId"`
	SandboxID       string     `json:"sandboxId"`
	RequestKey      string     `json:"requestKey"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	ExecutionID     string     `json:"executionId"`
	ExitCode        *int       `json:"exitCode"`
	Output          string     `json:"output"`
	Error           string     `json:"error"`
	DispatchedAt    *time.Time `json:"dispatchedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	ReceiptStoredAt *time.Time `json:"receiptStoredAt,omitempty"`
}

type GetSandboxOperationRequest struct {
	ID          string `json:"-" uri:"id"`
	OperationID string `json:"-" uri:"opID"`
}

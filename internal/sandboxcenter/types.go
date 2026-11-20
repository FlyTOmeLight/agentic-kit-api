// Package sandboxcenter implements the minimal phase-2 sandbox control plane:
// acquire/renew/exec/release with durable operation receipts backed by
// PostgreSQL and the guard-execd boundary in Kubernetes.
package sandboxcenter

import "time"

const (
	SandboxStatusProvisioning = "Provisioning"
	SandboxStatusActive       = "Active"
	SandboxStatusSaveBlocked  = "SaveBlocked"
	SandboxStatusReleasing    = "Releasing"
	SandboxStatusDestroyed    = "Destroyed"
	SandboxStatusLost         = "Lost"

	// DefaultMaxLifetime bounds one sandbox generation: renewals are clamped
	// to it and the guard exits at this point even without an explicit stop.
	DefaultMaxLifetime = 24 * time.Hour

	LeaseStatusActive    = "Active"
	LeaseStatusClosed    = "Closed"
	LeaseStatusReleasing = "Releasing"

	OpStatusPending         = "Pending"
	OpStatusDispatchStarted = "DispatchStarted"
	OpStatusSucceeded       = "Succeeded"
	OpStatusFailed          = "Failed"
	OpStatusUnknown         = "Unknown"
)

// Sandbox records the lifecycle fact of one allocated environment.
type Sandbox struct {
	ID                   string     `gorm:"type:text;primaryKey;not null" json:"id"`
	TenantID             string     `gorm:"type:text;not null;index" json:"tenantId"`
	OwnerID              string     `gorm:"type:text;not null;index" json:"ownerId"`
	RequestKey           string     `gorm:"type:text;not null;uniqueIndex:sandbox_owner_request_key" json:"requestKey"`
	RequestHash          string     `gorm:"type:text;not null" json:"-"`
	ProfileID            string     `gorm:"type:text;not null" json:"profileId"`
	ProfileRevision      int64      `gorm:"type:bigint;not null" json:"profileRevision"`
	Status               string     `gorm:"type:text;not null;check:sandbox_status_check,status IN ('Provisioning','Active','SaveBlocked','Releasing','Destroyed','Lost')" json:"status"`
	RequiredSave         bool       `gorm:"not null;default:false" json:"requiredSave"`
	RestoreFromSandboxID string     `gorm:"type:text;not null;default:''" json:"restoreFromSandboxId,omitempty"`
	ResumeCheckpoint     string     `gorm:"type:text;not null;default:''" json:"resumeCheckpoint,omitempty"`
	StoppedAt            *time.Time `gorm:"type:timestamptz" json:"stoppedAt,omitempty"`
	SavedCheckpoint      string     `gorm:"type:text;not null;default:''" json:"savedCheckpoint,omitempty"`
	CheckpointAttempt    string     `gorm:"type:text;not null;default:''" json:"-"`
	CheckpointRetryCount int        `gorm:"not null;default:0" json:"-"`
	ResumeCheckpointDir  string     `gorm:"type:text;not null;default:''" json:"-"`
	PodUID               string     `gorm:"type:text;not null;default:''" json:"podUid"`
	WorkspaceUID         string     `gorm:"type:text;not null;default:''" json:"workspaceUid"`
	Generation           int64      `gorm:"type:bigint;not null;default:0" json:"generation"`
	LeaseID              string     `gorm:"type:text;not null;default:''" json:"leaseId"`
	Version              int64      `gorm:"type:bigint;not null;default:0" json:"version"`
	CreatedAt            time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp()" json:"createdAt"`
	UpdatedAt            time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp()" json:"updatedAt"`
	DestroyedAt          *time.Time `gorm:"type:timestamptz" json:"destroyedAt"`
}

func (Sandbox) TableName() string { return "sandbox" }

// Lease is the exclusive admission token for a sandbox generation.
type Lease struct {
	ID                string     `gorm:"type:text;primaryKey;not null" json:"id"`
	SandboxID         string     `gorm:"type:text;not null;index" json:"sandboxId"`
	SandboxGeneration int64      `gorm:"type:bigint;not null" json:"sandboxGeneration"`
	HolderRunID       string     `gorm:"type:text;not null" json:"holderRunId"`
	HolderRunEpoch    int64      `gorm:"type:bigint;not null" json:"holderRunEpoch"`
	WorkerID          string     `gorm:"type:text;not null" json:"workerId"`
	Revision          int64      `gorm:"type:bigint;not null" json:"revision"`
	RequestKey        string     `gorm:"type:text;not null;default:''" json:"requestKey"`
	RequestHash       string     `gorm:"type:text;not null;default:''" json:"-"`
	Status            string     `gorm:"type:text;not null;check:lease_status_check,status IN ('Active','Closed','Releasing')" json:"status"`
	HardExpiresAt     time.Time  `gorm:"type:timestamptz;not null" json:"hardExpiresAt"`
	RenewedAt         time.Time  `gorm:"type:timestamptz;not null" json:"renewedAt"`
	ClosedAt          *time.Time `gorm:"type:timestamptz" json:"closedAt"`
	Version           int64      `gorm:"type:bigint;not null;default:0" json:"version"`
	CreatedAt         time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp()" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp()" json:"updatedAt"`
}

func (Lease) TableName() string { return "sandbox_lease" }

// Operation is the durable execution receipt. OperationID = LeaseID + RequestKey.
type Operation struct {
	ID              string     `gorm:"type:text;primaryKey;not null" json:"id"`
	LeaseID         string     `gorm:"type:text;not null;index" json:"leaseId"`
	SandboxID       string     `gorm:"type:text;not null;index" json:"sandboxId"`
	RequestKey      string     `gorm:"type:text;not null;uniqueIndex:sandbox_operation_key" json:"requestKey"`
	RequestHash     string     `gorm:"type:text;not null" json:"-"`
	Kind            string     `gorm:"type:text;not null" json:"kind"`
	InputHash       string     `gorm:"type:text;not null" json:"-"`
	Status          string     `gorm:"type:text;not null;check:sandbox_operation_status_check,status IN ('Pending','DispatchStarted','Succeeded','Failed','Unknown')" json:"status"`
	ExecutionID     string     `gorm:"type:text;not null;default:''" json:"executionId"`
	ExitCode        *int       `gorm:"type:integer" json:"exitCode"`
	Output          string     `gorm:"type:text;not null;default:''" json:"output"`
	Error           string     `gorm:"type:text;not null;default:''" json:"error"`
	DispatchedAt    *time.Time `gorm:"type:timestamptz" json:"dispatchedAt"`
	CompletedAt     *time.Time `gorm:"type:timestamptz" json:"completedAt"`
	ReceiptStoredAt *time.Time `gorm:"type:timestamptz" json:"receiptStoredAt"`
	Version         int64      `gorm:"type:bigint;not null;default:0" json:"version"`
	CreatedAt       time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp()" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp()" json:"updatedAt"`
}

func (Operation) TableName() string { return "sandbox_operation" }

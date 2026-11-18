// Package table defines pure storage rows for schema/postgres.sql and schema/mysql.sql.
// DATETIME(6) values are UTC. Nullable JSON is nil RawMessage; non-null JSON
// uses the schemaVersion/data envelope and the storage_json.go type catalog.
// All BIGINT values use signed int64; the application enforces the SDK JSON
// counter bound at admission/encoding. No ORM or domain dependency is used.
package table

import (
	"database/sql"
	"encoding/json"
	"time"
)

// ProfileRevisionRow maps sandbox_profile_revision; field order follows the DDL.
type ProfileRevisionRow struct {
	TenantID         string          `db:"tenant_id"`
	ProjectID        string          `db:"project_id"`
	ProfileID        string          `db:"profile_id"`
	Revision         int64           `db:"revision"`
	SpecHash         string          `db:"spec_hash"`
	ImageDigest      string          `db:"image_digest"`
	Enabled          bool            `db:"enabled"`
	DisabledAt       sql.NullTime    `db:"disabled_at"`
	DisableReason    sql.NullString  `db:"disable_reason"`
	ResolvedTemplate json.RawMessage `db:"resolved_template"`
	Capabilities     json.RawMessage `db:"capabilities"`
	Conditions       json.RawMessage `db:"conditions"`
	CreatedAt        time.Time       `db:"created_at"`
	UpdatedAt        time.Time       `db:"updated_at"`
	Version          int64           `db:"version"`
}

// QuotaAccountRow maps sandbox_quota_account; field order follows the DDL.
type QuotaAccountRow struct {
	ID             string       `db:"id"`
	TenantID       string       `db:"tenant_id"`
	ProjectID      string       `db:"project_id"`
	AccountKind    string       `db:"account_kind"`
	ScopeID        string       `db:"scope_id"`
	Resource       string       `db:"resource"`
	LimitAmount    int64        `db:"limit_amount"`
	ReservedAmount int64        `db:"reserved_amount"`
	ChargedAmount  int64        `db:"charged_amount"`
	ObservedAt     sql.NullTime `db:"observed_at"`
	CreatedAt      time.Time    `db:"created_at"`
	UpdatedAt      time.Time    `db:"updated_at"`
	Version        int64        `db:"version"`
}

// QuotaReservationRow maps sandbox_quota_reservation; field order follows the DDL.
type QuotaReservationRow struct {
	ID                       string         `db:"id"`
	TenantID                 string         `db:"tenant_id"`
	ProjectID                string         `db:"project_id"`
	AccountID                string         `db:"account_id"`
	LedgerKey                string         `db:"ledger_key"`
	SubjectKind              string         `db:"subject_kind"`
	SubjectID                string         `db:"subject_id"`
	SubjectGeneration        int64          `db:"subject_generation"`
	ChargeClass              string         `db:"charge_class"`
	Amount                   int64          `db:"amount"`
	Status                   string         `db:"status"`
	AcquisitionID            sql.NullString `db:"acquisition_id"`
	InstanceID               sql.NullString `db:"instance_id"`
	InstanceGeneration       sql.NullInt64  `db:"instance_generation"`
	LeaseID                  sql.NullString `db:"lease_id"`
	TransferredFromAccountID sql.NullString `db:"transferred_from_account_id"`
	ReservedAt               time.Time      `db:"reserved_at"`
	ChargedAt                sql.NullTime   `db:"charged_at"`
	ReleasedAt               sql.NullTime   `db:"released_at"`
	ReleaseEvidenceRef       sql.NullString `db:"release_evidence_ref"`
	CreatedAt                time.Time      `db:"created_at"`
	UpdatedAt                time.Time      `db:"updated_at"`
	Version                  int64          `db:"version"`
}

// WarmPoolRow maps sandbox_warm_pool; field order follows the DDL.
type WarmPoolRow struct {
	ID                string          `db:"id"`
	TenantID          string          `db:"tenant_id"`
	ProjectID         string          `db:"project_id"`
	PoolKey           string          `db:"pool_key"`
	ProfileTenantID   string          `db:"profile_tenant_id"`
	ProfileID         string          `db:"profile_id"`
	ProfileRevision   int64           `db:"profile_revision"`
	ClusterID         string          `db:"cluster_id"`
	Namespace         string          `db:"namespace"`
	SpecHash          string          `db:"spec_hash"`
	Enabled           bool            `db:"enabled"`
	MinReady          int64           `db:"min_ready"`
	MaxReady          int64           `db:"max_ready"`
	MaxProvisioning   int64           `db:"max_provisioning"`
	MaxIdleSeconds    int64           `db:"max_idle_seconds"`
	ReadyCount        int64           `db:"ready_count"`
	ProvisioningCount int64           `db:"provisioning_count"`
	DeletingCount     int64           `db:"deleting_count"`
	ObservedAt        sql.NullTime    `db:"observed_at"`
	PoolSpec          json.RawMessage `db:"pool_spec"`
	ResolvedSpec      json.RawMessage `db:"resolved_spec"`
	Conditions        json.RawMessage `db:"conditions"`
	NextReconcileAt   sql.NullTime    `db:"next_reconcile_at"`
	WorkerID          sql.NullString  `db:"worker_id"`
	ClaimToken        sql.NullString  `db:"claim_token"`
	ClaimUntil        sql.NullTime    `db:"claim_until"`
	CreatedAt         time.Time       `db:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at"`
	Version           int64           `db:"version"`
}

// WorkspaceBindingRow maps sandbox_workspace_binding; field order follows the DDL.
type WorkspaceBindingRow struct {
	ID                       string          `db:"id"`
	TenantID                 string          `db:"tenant_id"`
	ProjectID                string          `db:"project_id"`
	WorkspaceGeneration      int64           `db:"workspace_generation"`
	Mode                     string          `db:"mode"`
	SourceRef                string          `db:"source_ref"`
	BaseCommit               string          `db:"base_commit"`
	DataVersion              string          `db:"data_version"`
	StorageRef               sql.NullString  `db:"storage_ref"`
	StorageIdentityHash      sql.NullString  `db:"storage_identity_hash"`
	RecoveryLevel            string          `db:"recovery_level"`
	Status                   string          `db:"status"`
	PreparingAcquisitionID   sql.NullString  `db:"preparing_acquisition_id"`
	PreparationExpiresAt     sql.NullTime    `db:"preparation_expires_at"`
	WriterLeaseID            sql.NullString  `db:"writer_lease_id"`
	BoundInstanceID          sql.NullString  `db:"bound_instance_id"`
	BoundInstanceGeneration  sql.NullInt64   `db:"bound_instance_generation"`
	NextFencingToken         int64           `db:"next_fencing_token"`
	CheckpointID             sql.NullString  `db:"checkpoint_id"`
	SavePolicyID             string          `db:"save_policy_id"`
	SavePolicyRevision       int64           `db:"save_policy_revision"`
	WriteBarrierOperationID  sql.NullString  `db:"write_barrier_operation_id"`
	WriteBarrierEpoch        int64           `db:"write_barrier_epoch"`
	PhysicalFenceVerifiedAt  sql.NullTime    `db:"physical_fence_verified_at"`
	PhysicalFenceEvidenceRef sql.NullString  `db:"physical_fence_evidence_ref"`
	Layout                   json.RawMessage `db:"layout"`
	Failure                  json.RawMessage `db:"failure"`
	CreatedAt                time.Time       `db:"created_at"`
	UpdatedAt                time.Time       `db:"updated_at"`
	Version                  int64           `db:"version"`
}

// AcquireRequestRow maps sandbox_acquire_request; field order follows the DDL.
type AcquireRequestRow struct {
	ID                    string          `db:"id"`
	TenantID              string          `db:"tenant_id"`
	ProjectID             string          `db:"project_id"`
	CallerID              string          `db:"caller_id"`
	RequestKey            string          `db:"request_key"`
	SpecHash              string          `db:"spec_hash"`
	Mode                  string          `db:"mode"`
	ScopeType             string          `db:"scope_type"`
	ScopeID               string          `db:"scope_id"`
	HolderRunID           string          `db:"holder_run_id"`
	HolderRunEpoch        int64           `db:"holder_run_epoch"`
	ProfileTenantID       string          `db:"profile_tenant_id"`
	ProfileID             string          `db:"profile_id"`
	ProfileRevision       int64           `db:"profile_revision"`
	WorkspaceID           string          `db:"workspace_id"`
	WorkspaceGeneration   int64           `db:"workspace_generation"`
	ReuseKey              sql.NullString  `db:"reuse_key"`
	PoolKey               string          `db:"pool_key"`
	CapacityPolicy        string          `db:"capacity_policy"`
	Status                string          `db:"status"`
	Phase                 sql.NullString  `db:"phase"`
	QueueDeadline         time.Time       `db:"queue_deadline"`
	ProvisionDeadline     time.Time       `db:"provision_deadline"`
	RequestedLeaseSeconds int64           `db:"requested_lease_seconds"`
	InstanceID            sql.NullString  `db:"instance_id"`
	InstanceGeneration    sql.NullInt64   `db:"instance_generation"`
	LeaseID               sql.NullString  `db:"lease_id"`
	AllocationSource      sql.NullString  `db:"allocation_source"`
	Attempt               int64           `db:"attempt"`
	CompletedAt           sql.NullTime    `db:"completed_at"`
	RequestSpec           json.RawMessage `db:"request_spec"`
	ReadyResult           json.RawMessage `db:"ready_result"`
	ReadyEvidence         json.RawMessage `db:"ready_evidence"`
	CredentialGrants      json.RawMessage `db:"credential_grants"`
	Failure               json.RawMessage `db:"failure"`
	Conditions            json.RawMessage `db:"conditions"`
	NextReconcileAt       sql.NullTime    `db:"next_reconcile_at"`
	WorkerID              sql.NullString  `db:"worker_id"`
	ClaimToken            sql.NullString  `db:"claim_token"`
	ClaimUntil            sql.NullTime    `db:"claim_until"`
	TombstonedAt          sql.NullTime    `db:"tombstoned_at"`
	ResultExpiresAt       sql.NullTime    `db:"result_expires_at"`
	ReplayClosedAt        sql.NullTime    `db:"replay_closed_at"`
	PurgeAfter            sql.NullTime    `db:"purge_after"`
	CreatedAt             time.Time       `db:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at"`
	Version               int64           `db:"version"`
}

// InstanceRow maps sandbox_instance; field order follows the DDL.
type InstanceRow struct {
	ID                     string          `db:"id"`
	TenantID               string          `db:"tenant_id"`
	ProjectID              string          `db:"project_id"`
	Generation             int64           `db:"generation"`
	Status                 string          `db:"status"`
	Phase                  sql.NullString  `db:"phase"`
	Reason                 sql.NullString  `db:"reason"`
	ProfileTenantID        string          `db:"profile_tenant_id"`
	ProfileID              string          `db:"profile_id"`
	ProfileRevision        int64           `db:"profile_revision"`
	PoolKey                sql.NullString  `db:"pool_key"`
	ReuseKey               sql.NullString  `db:"reuse_key"`
	SpecHash               string          `db:"spec_hash"`
	ClusterID              string          `db:"cluster_id"`
	Namespace              string          `db:"namespace"`
	PodName                string          `db:"pod_name"`
	PodUID                 sql.NullString  `db:"pod_uid"`
	RuntimeBootID          sql.NullString  `db:"runtime_boot_id"`
	RuntimeProtocolVersion sql.NullString  `db:"runtime_protocol_version"`
	PhysicalPhase          sql.NullString  `db:"physical_phase"`
	EverClaimed            bool            `db:"ever_claimed"`
	ClaimedAcquisitionID   sql.NullString  `db:"claimed_acquisition_id"`
	ClaimExpiresAt         sql.NullTime    `db:"claim_expires_at"`
	ActiveLeaseID          sql.NullString  `db:"active_lease_id"`
	WorkspaceID            sql.NullString  `db:"workspace_id"`
	WorkspaceGeneration    sql.NullInt64   `db:"workspace_generation"`
	AdmissionEpoch         int64           `db:"admission_epoch"`
	ProvisionDeadline      time.Time       `db:"provision_deadline"`
	HardExpiresAt          time.Time       `db:"hard_expires_at"`
	IdleExpiresAt          sql.NullTime    `db:"idle_expires_at"`
	ObservedAt             sql.NullTime    `db:"observed_at"`
	PhysicalGoneAt         sql.NullTime    `db:"physical_gone_at"`
	WritesStoppedAt        sql.NullTime    `db:"writes_stopped_at"`
	DeletedAt              sql.NullTime    `db:"deleted_at"`
	ResolvedSpec           json.RawMessage `db:"resolved_spec"`
	PhysicalRef            json.RawMessage `db:"physical_ref"`
	Endpoint               json.RawMessage `db:"endpoint"`
	RuntimeInfo            json.RawMessage `db:"runtime_info"`
	EffectiveCapabilities  json.RawMessage `db:"effective_capabilities"`
	FenceEvidence          json.RawMessage `db:"fence_evidence"`
	Failure                json.RawMessage `db:"failure"`
	Conditions             json.RawMessage `db:"conditions"`
	NextReconcileAt        sql.NullTime    `db:"next_reconcile_at"`
	WorkerID               sql.NullString  `db:"worker_id"`
	ClaimToken             sql.NullString  `db:"claim_token"`
	ClaimUntil             sql.NullTime    `db:"claim_until"`
	CreatedAt              time.Time       `db:"created_at"`
	UpdatedAt              time.Time       `db:"updated_at"`
	Version                int64           `db:"version"`
}

// LeaseRow maps sandbox_lease; field order follows the DDL.
type LeaseRow struct {
	ID                        string          `db:"id"`
	TenantID                  string          `db:"tenant_id"`
	ProjectID                 string          `db:"project_id"`
	AcquisitionID             string          `db:"acquisition_id"`
	InstanceID                string          `db:"instance_id"`
	InstanceGeneration        int64           `db:"instance_generation"`
	PodUID                    string          `db:"pod_uid"`
	RuntimeBootID             string          `db:"runtime_boot_id"`
	WorkspaceID               string          `db:"workspace_id"`
	WorkspaceGeneration       int64           `db:"workspace_generation"`
	HolderRunID               string          `db:"holder_run_id"`
	HolderRunEpoch            int64           `db:"holder_run_epoch"`
	CallerID                  string          `db:"caller_id"`
	ScopeType                 string          `db:"scope_type"`
	ScopeID                   string          `db:"scope_id"`
	FencingToken              int64           `db:"fencing_token"`
	AdmissionEpoch            int64           `db:"admission_epoch"`
	Status                    string          `db:"status"`
	ExpiresAt                 time.Time       `db:"expires_at"`
	HardExpiresAt             time.Time       `db:"hard_expires_at"`
	AdmissionClosedAt         sql.NullTime    `db:"admission_closed_at"`
	ReleaseMode               sql.NullString  `db:"release_mode"`
	ReleaseOperationID        sql.NullString  `db:"release_operation_id"`
	ReleasingAt               sql.NullTime    `db:"releasing_at"`
	ReleasedAt                sql.NullTime    `db:"released_at"`
	RuntimeBarrierConfirmedAt sql.NullTime    `db:"runtime_barrier_confirmed_at"`
	ProcessTreeStoppedAt      sql.NullTime    `db:"process_tree_stopped_at"`
	SessionsClosedAt          sql.NullTime    `db:"sessions_closed_at"`
	CredentialsRevokedAt      sql.NullTime    `db:"credentials_revoked_at"`
	Permissions               json.RawMessage `db:"permissions"`
	CredentialGrants          json.RawMessage `db:"credential_grants"`
	Termination               json.RawMessage `db:"termination"`
	DrainEvidence             json.RawMessage `db:"drain_evidence"`
	Failure                   json.RawMessage `db:"failure"`
	NextReconcileAt           sql.NullTime    `db:"next_reconcile_at"`
	WorkerID                  sql.NullString  `db:"worker_id"`
	ClaimToken                sql.NullString  `db:"claim_token"`
	ClaimUntil                sql.NullTime    `db:"claim_until"`
	CreatedAt                 time.Time       `db:"created_at"`
	UpdatedAt                 time.Time       `db:"updated_at"`
	Version                   int64           `db:"version"`
}

// ReuseSlotRow maps sandbox_reuse_slot; field order follows the DDL.
type ReuseSlotRow struct {
	TenantID                string         `db:"tenant_id"`
	ProjectID               string         `db:"project_id"`
	ReuseKey                string         `db:"reuse_key"`
	ScopeType               string         `db:"scope_type"`
	ScopeID                 string         `db:"scope_id"`
	ProfileTenantID         string         `db:"profile_tenant_id"`
	ProfileID               string         `db:"profile_id"`
	ProfileRevision         int64          `db:"profile_revision"`
	WorkspaceID             string         `db:"workspace_id"`
	WorkspaceGeneration     int64          `db:"workspace_generation"`
	IsolationPolicyRevision int64          `db:"isolation_policy_revision"`
	AccessPolicyRevision    int64          `db:"access_policy_revision"`
	ReadonlyAssetDigest     string         `db:"readonly_asset_digest"`
	PreparingAcquisitionID  sql.NullString `db:"preparing_acquisition_id"`
	InstanceID              sql.NullString `db:"instance_id"`
	InstanceGeneration      sql.NullInt64  `db:"instance_generation"`
	WriterLeaseID           sql.NullString `db:"writer_lease_id"`
	CreatedAt               time.Time      `db:"created_at"`
	UpdatedAt               time.Time      `db:"updated_at"`
	Version                 int64          `db:"version"`
}

// ActionRow maps sandbox_action; field order follows the DDL.
type ActionRow struct {
	ID                  string          `db:"id"`
	TenantID            string          `db:"tenant_id"`
	ProjectID           string          `db:"project_id"`
	CallerID            string          `db:"caller_id"`
	Action              string          `db:"action"`
	RequestKey          string          `db:"request_key"`
	SpecHash            string          `db:"spec_hash"`
	Status              string          `db:"status"`
	TargetKind          string          `db:"target_kind"`
	TargetID            string          `db:"target_id"`
	TargetRevision      int64           `db:"target_revision"`
	LeaseID             sql.NullString  `db:"lease_id"`
	AcquisitionID       sql.NullString  `db:"acquisition_id"`
	InstanceID          sql.NullString  `db:"instance_id"`
	InstanceGeneration  sql.NullInt64   `db:"instance_generation"`
	PodUID              sql.NullString  `db:"pod_uid"`
	RuntimeBootID       sql.NullString  `db:"runtime_boot_id"`
	FencingToken        sql.NullInt64   `db:"fencing_token"`
	AdmissionEpoch      sql.NullInt64   `db:"admission_epoch"`
	WorkspaceID         sql.NullString  `db:"workspace_id"`
	WorkspaceGeneration sql.NullInt64   `db:"workspace_generation"`
	DispatchAttemptID   sql.NullString  `db:"dispatch_attempt_id"`
	DispatchStartedAt   sql.NullTime    `db:"dispatch_started_at"`
	DispatchOutcome     sql.NullString  `db:"dispatch_outcome"`
	ReplayPolicy        string          `db:"replay_policy"`
	ExternalRef         sql.NullString  `db:"external_ref"`
	EvidenceRef         sql.NullString  `db:"evidence_ref"`
	CompletedAt         sql.NullTime    `db:"completed_at"`
	Input               json.RawMessage `db:"input"`
	Result              json.RawMessage `db:"result"`
	Failure             json.RawMessage `db:"failure"`
	NextReconcileAt     sql.NullTime    `db:"next_reconcile_at"`
	WorkerID            sql.NullString  `db:"worker_id"`
	ClaimToken          sql.NullString  `db:"claim_token"`
	ClaimUntil          sql.NullTime    `db:"claim_until"`
	TombstonedAt        sql.NullTime    `db:"tombstoned_at"`
	ResultExpiresAt     sql.NullTime    `db:"result_expires_at"`
	ReplayClosedAt      sql.NullTime    `db:"replay_closed_at"`
	PurgeAfter          sql.NullTime    `db:"purge_after"`
	CreatedAt           time.Time       `db:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"`
	Version             int64           `db:"version"`
}

// ExecutionRow maps sandbox_execution; field order follows the DDL.
type ExecutionRow struct {
	ID                  string          `db:"id"`
	TenantID            string          `db:"tenant_id"`
	ProjectID           string          `db:"project_id"`
	LeaseID             string          `db:"lease_id"`
	RequestKey          string          `db:"request_key"`
	SpecHash            string          `db:"spec_hash"`
	InstanceID          string          `db:"instance_id"`
	InstanceGeneration  int64           `db:"instance_generation"`
	PodUID              string          `db:"pod_uid"`
	RuntimeBootID       string          `db:"runtime_boot_id"`
	WorkspaceID         string          `db:"workspace_id"`
	WorkspaceGeneration int64           `db:"workspace_generation"`
	HolderRunID         string          `db:"holder_run_id"`
	MemberID            sql.NullString  `db:"member_id"`
	WorktreeID          sql.NullString  `db:"worktree_id"`
	FencingToken        int64           `db:"fencing_token"`
	AdmissionEpoch      int64           `db:"admission_epoch"`
	Status              string          `db:"status"`
	Cwd                 string          `db:"cwd"`
	CommandMode         string          `db:"command_mode"`
	Deadline            time.Time       `db:"deadline"`
	MaxOutputBytes      int64           `db:"max_output_bytes"`
	StdinClosed         bool            `db:"stdin_closed"`
	NextInputSequence   int64           `db:"next_input_sequence"`
	DispatchAttemptID   sql.NullString  `db:"dispatch_attempt_id"`
	DispatchEpoch       sql.NullInt64   `db:"dispatch_epoch"`
	DispatchStartedAt   sql.NullTime    `db:"dispatch_started_at"`
	DispatchOutcome     sql.NullString  `db:"dispatch_outcome"`
	ReplayPolicy        string          `db:"replay_policy"`
	BackendRef          sql.NullString  `db:"backend_ref"`
	StartedAt           sql.NullTime    `db:"started_at"`
	CompletedAt         sql.NullTime    `db:"completed_at"`
	ObservedAt          sql.NullTime    `db:"observed_at"`
	ExitCode            sql.NullInt64   `db:"exit_code"`
	ExitSignal          sql.NullString  `db:"exit_signal"`
	OutputCompleteness  string          `db:"output_completeness"`
	OutputBytes         int64           `db:"output_bytes"`
	SourceCursor        sql.NullString  `db:"source_cursor"`
	SourceCursorKind    sql.NullString  `db:"source_cursor_kind"`
	CancelOperationID   sql.NullString  `db:"cancel_operation_id"`
	RequestSpec         json.RawMessage `db:"request_spec"`
	CommandSpec         json.RawMessage `db:"command_spec"`
	Termination         json.RawMessage `db:"termination"`
	Failure             json.RawMessage `db:"failure"`
	NextReconcileAt     sql.NullTime    `db:"next_reconcile_at"`
	WorkerID            sql.NullString  `db:"worker_id"`
	ClaimToken          sql.NullString  `db:"claim_token"`
	ClaimUntil          sql.NullTime    `db:"claim_until"`
	TombstonedAt        sql.NullTime    `db:"tombstoned_at"`
	ResultExpiresAt     sql.NullTime    `db:"result_expires_at"`
	ReplayClosedAt      sql.NullTime    `db:"replay_closed_at"`
	PurgeAfter          sql.NullTime    `db:"purge_after"`
	CreatedAt           time.Time       `db:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"`
	Version             int64           `db:"version"`
}

// WorktreeRow maps sandbox_worktree; field order follows the DDL.
type WorktreeRow struct {
	ID                  string          `db:"id"`
	TenantID            string          `db:"tenant_id"`
	ProjectID           string          `db:"project_id"`
	OperationID         string          `db:"operation_id"`
	LeaseID             string          `db:"lease_id"`
	RequestKey          string          `db:"request_key"`
	SpecHash            string          `db:"spec_hash"`
	WorkspaceID         string          `db:"workspace_id"`
	WorkspaceGeneration int64           `db:"workspace_generation"`
	TaskID              string          `db:"task_id"`
	HolderRunID         string          `db:"holder_run_id"`
	MemberID            string          `db:"member_id"`
	BaseCommit          string          `db:"base_commit"`
	Branch              string          `db:"branch"`
	Path                string          `db:"path"`
	PathHash            string          `db:"path_hash"`
	ActiveBranch        sql.NullString  `db:"active_branch"`
	ActivePathHash      sql.NullString  `db:"active_path_hash"`
	HeadCommit          sql.NullString  `db:"head_commit"`
	Status              string          `db:"status"`
	RemovedAt           sql.NullTime    `db:"removed_at"`
	Failure             json.RawMessage `db:"failure"`
	CreatedAt           time.Time       `db:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"`
	Version             int64           `db:"version"`
}

// CheckpointRow maps sandbox_checkpoint; field order follows the DDL.
type CheckpointRow struct {
	ID                  string          `db:"id"`
	TenantID            string          `db:"tenant_id"`
	ProjectID           string          `db:"project_id"`
	OperationID         string          `db:"operation_id"`
	LeaseID             string          `db:"lease_id"`
	RequestKey          string          `db:"request_key"`
	SpecHash            string          `db:"spec_hash"`
	WorkspaceID         string          `db:"workspace_id"`
	WorkspaceGeneration int64           `db:"workspace_generation"`
	InstanceID          string          `db:"instance_id"`
	InstanceGeneration  int64           `db:"instance_generation"`
	BaseCommit          string          `db:"base_commit"`
	DataVersion         string          `db:"data_version"`
	Status              string          `db:"status"`
	Consistency         string          `db:"consistency"`
	ManifestRef         sql.NullString  `db:"manifest_ref"`
	ManifestSHA256      sql.NullString  `db:"manifest_sha256"`
	StorageRef          sql.NullString  `db:"storage_ref"`
	SizeBytes           sql.NullInt64   `db:"size_bytes"`
	PublishedAt         sql.NullTime    `db:"published_at"`
	Coverage            json.RawMessage `db:"coverage"`
	Manifest            json.RawMessage `db:"manifest"`
	QuiescenceEvidence  json.RawMessage `db:"quiescence_evidence"`
	Failure             json.RawMessage `db:"failure"`
	CreatedAt           time.Time       `db:"created_at"`
	UpdatedAt           time.Time       `db:"updated_at"`
	Version             int64           `db:"version"`
}

// AccessSessionRow maps sandbox_access_session; field order follows the DDL.
type AccessSessionRow struct {
	ID                 string          `db:"id"`
	TenantID           string          `db:"tenant_id"`
	ProjectID          string          `db:"project_id"`
	OperationID        string          `db:"operation_id"`
	LeaseID            string          `db:"lease_id"`
	RequestKey         string          `db:"request_key"`
	SpecHash           string          `db:"spec_hash"`
	Kind               string          `db:"kind"`
	Status             string          `db:"status"`
	InstanceID         string          `db:"instance_id"`
	InstanceGeneration int64           `db:"instance_generation"`
	PodUID             string          `db:"pod_uid"`
	RuntimeBootID      string          `db:"runtime_boot_id"`
	FencingToken       int64           `db:"fencing_token"`
	AdmissionEpoch     int64           `db:"admission_epoch"`
	Protocol           string          `db:"protocol"`
	GatewayRouteRef    sql.NullString  `db:"gateway_route_ref"`
	BackendRef         sql.NullString  `db:"backend_ref"`
	PreviewPort        sql.NullInt64   `db:"preview_port"`
	WriterConnectionID sql.NullString  `db:"writer_connection_id"`
	WriterSubjectID    sql.NullString  `db:"writer_subject_id"`
	WriterEpoch        int64           `db:"writer_epoch"`
	WriterExpiresAt    sql.NullTime    `db:"writer_expires_at"`
	NextInputSequence  int64           `db:"next_input_sequence"`
	TerminalColumns    sql.NullInt64   `db:"terminal_columns"`
	TerminalRows       sql.NullInt64   `db:"terminal_rows"`
	ExpiresAt          time.Time       `db:"expires_at"`
	ClosedAt           sql.NullTime    `db:"closed_at"`
	DisconnectedAt     sql.NullTime    `db:"disconnected_at"`
	RequestSpec        json.RawMessage `db:"request_spec"`
	Endpoint           json.RawMessage `db:"endpoint"`
	Failure            json.RawMessage `db:"failure"`
	CreatedAt          time.Time       `db:"created_at"`
	UpdatedAt          time.Time       `db:"updated_at"`
	Version            int64           `db:"version"`
}

// AccessTicketRow maps sandbox_access_ticket; field order follows the DDL.
type AccessTicketRow struct {
	ID           string         `db:"id"`
	TenantID     string         `db:"tenant_id"`
	ProjectID    string         `db:"project_id"`
	SessionID    string         `db:"session_id"`
	LeaseID      string         `db:"lease_id"`
	TokenHash    string         `db:"token_hash"`
	SubjectID    string         `db:"subject_id"`
	Audience     string         `db:"audience"`
	Role         string         `db:"role"`
	WriterEpoch  int64          `db:"writer_epoch"`
	ExpiresAt    time.Time      `db:"expires_at"`
	ConsumedAt   sql.NullTime   `db:"consumed_at"`
	RevokedAt    sql.NullTime   `db:"revoked_at"`
	ConnectionID sql.NullString `db:"connection_id"`
	CreatedAt    time.Time      `db:"created_at"`
	UpdatedAt    time.Time      `db:"updated_at"`
	Version      int64          `db:"version"`
}

// EventStreamRow maps sandbox_event_stream; field order follows the DDL.
type EventStreamRow struct {
	ID                     string         `db:"id"`
	TenantID               string         `db:"tenant_id"`
	ProjectID              string         `db:"project_id"`
	EntityKind             string         `db:"entity_kind"`
	EntityID               string         `db:"entity_id"`
	NextSequence           int64          `db:"next_sequence"`
	FirstAvailableSequence int64          `db:"first_available_sequence"`
	ClosedAt               sql.NullTime   `db:"closed_at"`
	ArchiveRef             sql.NullString `db:"archive_ref"`
	CreatedAt              time.Time      `db:"created_at"`
	UpdatedAt              time.Time      `db:"updated_at"`
	Version                int64          `db:"version"`
}

// EventRow maps sandbox_event; field order follows the DDL.
type EventRow struct {
	ID                 string          `db:"id"`
	TenantID           string          `db:"tenant_id"`
	ProjectID          string          `db:"project_id"`
	StreamID           string          `db:"stream_id"`
	EntityKind         string          `db:"entity_kind"`
	EntityID           string          `db:"entity_id"`
	Sequence           int64           `db:"sequence"`
	Type               string          `db:"type"`
	EntityVersion      sql.NullInt64   `db:"entity_version"`
	PreviousVersion    sql.NullInt64   `db:"previous_version"`
	InstanceGeneration sql.NullInt64   `db:"instance_generation"`
	TraceID            sql.NullString  `db:"trace_id"`
	SourceKey          sql.NullString  `db:"source_key"`
	OccurredAt         time.Time       `db:"occurred_at"`
	ExpiresAt          time.Time       `db:"expires_at"`
	Payload            json.RawMessage `db:"payload"`
	CreatedAt          time.Time       `db:"created_at"`
}

// OutputSegmentRow maps sandbox_output_segment; field order follows the DDL.
type OutputSegmentRow struct {
	ID                 string          `db:"id"`
	TenantID           string          `db:"tenant_id"`
	ProjectID          string          `db:"project_id"`
	ExecutionID        string          `db:"execution_id"`
	InstanceGeneration int64           `db:"instance_generation"`
	RuntimeBootID      string          `db:"runtime_boot_id"`
	SourceCursorHash   string          `db:"source_cursor_hash"`
	SourceCursor       string          `db:"source_cursor"`
	NextSourceCursor   string          `db:"next_source_cursor"`
	CursorKind         string          `db:"cursor_kind"`
	FirstSequence      int64           `db:"first_sequence"`
	LastSequence       int64           `db:"last_sequence"`
	SizeBytes          int64           `db:"size_bytes"`
	SHA256             string          `db:"sha256"`
	StorageRef         string          `db:"storage_ref"`
	Completeness       string          `db:"completeness"`
	ArchivedAt         time.Time       `db:"archived_at"`
	ExpiresAt          sql.NullTime    `db:"expires_at"`
	Archive            json.RawMessage `db:"archive"`
	CreatedAt          time.Time       `db:"created_at"`
}

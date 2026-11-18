package contract

import (
	"time"

	sandboxv1 "example.com/sandbox-design-contracts/sdk/go/sandboxv1"
)

// JSONValue is the envelope for every non-NULL SQL JSON column. Version 1 is
// the only supported version. Unknown versions fail decoding, not silent defaults.
// Data must use a type listed in the schema catalog, never map[string]any.
// SQL NULL uses a nil json.RawMessage field; non-NULL envelopes cannot have null data.
type JSONValue[T any] struct {
	SchemaVersion int `json:"schemaVersion"`
	Data          T   `json:"data"`
}

// StoredAcquireRequest excludes HTTP wait and credentials. Resolved defaults are
// explicit; request hashes use canonical effective request data, not raw JSON or
// the server-only AuthorizedPrincipal snapshot.
// Scope, workspace mode/save policy and durations are validated against the exact
// bound Profile first. Profile ID/revision and immutable policy refs stay pinned.
type StoredAcquireRequest struct {
	// Frozen from verified MutationKey.Principal, never from the public request.
	// RunAttempt must match HolderRunID/HolderRunEpoch; identity and attempt are
	// immutable. Contains no bearer token. Roles are audit-only: controllers must
	// revalidate identity, current roles and exact attempt with trusted authority.
	// Leases resolve this snapshot through acquisition_id after controller restart;
	// looking up the current worker must never replace the original attempt.
	// Retain request_spec while its lease or queued effects need this identity;
	// acquisition completion alone does not authorize tombstoning the snapshot.
	AuthorizedPrincipal Principal `json:"authorizedPrincipal"`

	RequestKey              string                    `json:"requestKey"`
	ProjectID               string                    `json:"projectId"`
	Mode                    sandboxv1.AcquisitionMode `json:"mode"`
	Scope                   sandboxv1.ReuseScope      `json:"scope"`
	HolderRunID             string                    `json:"holderRunId"`
	HolderRunEpoch          int64                     `json:"holderRunEpoch"`
	ProfileRef              sandboxv1.ProfileRef      `json:"profileRef"`
	RequiredCapabilities    []sandboxv1.Capability    `json:"requiredCapabilities"`
	MinimumIsolation        sandboxv1.IsolationLevel  `json:"minimumIsolation"`
	Workspace               sandboxv1.WorkspaceSpec   `json:"workspace"`
	CapacityPolicy          sandboxv1.CapacityPolicy  `json:"capacityPolicy"`
	ProvisionTimeoutSeconds int64                     `json:"provisionTimeoutSeconds"`
	RequestedLeaseSeconds   int64                     `json:"requestedLeaseSeconds"`
}

// StoredExecutionRequest retains authorized credential configuration IDs, not
// credential values. Environment is intended for non-secret configuration;
// credential injection is resolved separately through CredentialRefs.
type StoredExecutionRequest struct {
	RequestKey     string                          `json:"requestKey"`
	Proof          sandboxv1.LeaseProof            `json:"proof"`
	Command        sandboxv1.Command               `json:"command"`
	Workspace      sandboxv1.WorkspaceRef          `json:"workspace"`
	MemberID       *string                         `json:"memberId"`
	CWD            string                          `json:"cwd"`
	Environment    []sandboxv1.EnvironmentVariable `json:"environment"`
	CredentialRefs []string                        `json:"credentialRefs"`
	TimeoutSeconds int64                           `json:"timeoutSeconds"`
	MaxOutputBytes int64                           `json:"maxOutputBytes"`
	OpenStdin      bool                            `json:"openStdin"`
}

type QuotaTransferEvidence struct {
	ReservationID        string `json:"reservationId"`
	SourceAccountID      string `json:"sourceAccountId"`
	DestinationAccountID string `json:"destinationAccountId"`
	Resource             string `json:"resource"`
	Amount               int64  `json:"amount"`
}

type RevokeCredentialRequest struct {
	GrantID string `json:"grantId"`
}

// StoredSaveRetryRequest preserves the exact frozen target, verified authority,
// previous failed attempt (null for a gap) and independent save-only capability.
// It refers to the immutable original input in SaveProtectionState.Intents.
type StoredSaveRetryRequest struct {
	Request            sandboxv1.RetryRequiredSaveRequest `json:"request"`
	Authorization      SaveRecoveryAuthorization          `json:"authorization"`
	RetryOfOperationID *string                            `json:"retryOfOperationId"`
	Permit             SaveRecoveryPermit                 `json:"permit"`
}

type StoredDiscardRequest struct {
	Request       sandboxv1.DiscardBlockedSaveRequest `json:"request"`
	Authorization SaveRecoveryAuthorization           `json:"authorization"`
}

type CredentialRevocation struct {
	GrantID     string    `json:"grantId"`
	ConfirmedAt time.Time `json:"confirmedAt"`
}

// AuditEvent is used by internal streams; public streams store sandboxv1.Event.
// It records evidence references and state transitions, never raw provider bodies.
type AuditEvent struct {
	EntityKind    string                 `json:"entityKind"`
	EntityID      string                 `json:"entityId"`
	Action        string                 `json:"action"`
	OperationID   *string                `json:"operationId"`
	ActorID       string                 `json:"actorId"`
	OldState      *string                `json:"oldState"`
	NewState      string                 `json:"newState"`
	OldVersion    *int64                 `json:"oldVersion"`
	NewVersion    int64                  `json:"newVersion"`
	ReasonCode    string                 `json:"reasonCode"`
	EvidenceRefs  []string               `json:"evidenceRefs"`
	QuotaTransfer *QuotaTransferEvidence `json:"quotaTransfer"`
	OccurredAt    time.Time              `json:"occurredAt"`
}

// SQL column payload types (the outer JSONValue envelope is always required):
//
// profile_revision.resolved_template: sandboxv1.ProfileSpec
// *.capabilities / effective_capabilities: []sandboxv1.Capability
// *.conditions: []sandboxv1.Condition
// warm_pool.pool_spec: sandboxv1.PoolSpec
// *.resolved_spec: InstanceSpec
// workspace_binding.layout: sandboxv1.WorkspaceLayout
// acquire_request.request_spec: StoredAcquireRequest
// acquire_request.ready_result: sandboxv1.AcquisitionResult
// acquire_request.ready_evidence: ReadyEvidence
// *.credential_grants: []CredentialGrant
// instance.physical_ref: PhysicalRef
// *.endpoint: Endpoint
// instance.runtime_info: RuntimeInfo
// instance.fence_evidence: FenceEvidence
// lease.permissions: []string
// *.termination: sandboxv1.TerminationResult
// lease.drain_evidence: DrainEvidence
//   SaveProtection retains frozen inputs/gaps, current revision/attempts and
//   disposition link; PublicationFence retains external late-publish evidence.
// *.failure: sandboxv1.Error
// execution.request_spec: StoredExecutionRequest
// execution.command_spec: CommandSpec
// checkpoint.coverage: sandboxv1.CheckpointCoverage
// checkpoint.manifest: CheckpointManifest
// checkpoint.quiescence_evidence: QuiescenceEvidence
// access_session.request_spec: sandboxv1.CreateTerminalRequest,
//   sandboxv1.CreateBrowserRequest or sandboxv1.CreatePreviewRequest (by kind)
// event.payload: sandboxv1.Event (public), AuditEvent (internal)
// output_segment.archive: sandboxv1.ArchiveRef
// action.input / result: the action-specific request / response from sandboxv1;
//   full mapping is in schema-reference.md. Asynchronous results persist the
//   final resource DTO, not a recursive copy of Operation. Acknowledged input
//   stores InputReceipt; unknown delivery never creates a success receipt.
// action.input for RetryRequiredSave: StoredSaveRetryRequest;
//   result: sandboxv1.RequiredSaveResult.
// action.input for DiscardBlockedSave: StoredDiscardRequest;
//   result: sandboxv1.DiscardReceipt. Authorization is server-produced; persist
//   evidence references, never bearer credentials or user-supplied authority.
//
// Fields in the internal stored types carry explicit JSON tags. Duration fields
// end in Nanos in storage tags and use signed int64 nanoseconds; public SDK fields
// use seconds. References are broker/storage configuration IDs, not secret values.

package contract

import (
	"context"
	"encoding/json"
	"time"

	sandboxv1 "example.com/sandbox-design-contracts/sdk/go/sandboxv1"
)

type MutationKey struct {
	Principal   Principal
	RequestKey  string
	RequestHash string
	Action      string
	Resource    ResourceScope

	// Required for new worker-owned effects; excluded from semantic request hashes.
	// The transaction checks principal/action/resource/expiry and the bound epoch.
	RunAuthorization *RunAuthorization
}

type OperationIdentity struct {
	Principal  Principal
	Action     string
	Resource   ResourceScope
	RequestKey string
}

type RootHolder struct {
	ScopeType string
	ScopeID   string
	RunID     string
	RunEpoch  int64
}

type AcquireIntent struct {
	Key               MutationKey
	AcquisitionID     string
	Mode              string
	Holder            RootHolder
	Profile           ResolvedProfile
	Workspace         WorkspaceSpec
	ReuseKey          string
	PoolKey           string
	CapacityPolicy    string
	QueueDeadline     time.Time
	ProvisionDeadline time.Time
	RequestedLease    time.Duration
}

type AcquisitionSnapshot struct {
	ID               string
	Version          int64
	State            string
	Phase            string
	Instance         *InstanceKey
	LeaseID          *string
	AllocationSource string
	NextReconcileAt  time.Time
}

type LeaseSnapshot struct {
	ID            string
	Version       int64
	Instance      InstanceKey
	Holder        RootHolder
	Workspace     WorkspaceKey
	Proof         AdmissionProof
	State         string
	HardExpiresAt time.Time
}

type MutationReceipt struct {
	OperationID string
	State       string
	Replayed    bool
	ResultRef   string
}

type ReadyEvidence struct {
	Instance       InstanceKey     `json:"instance"`
	Observation    Observation     `json:"observation"`
	Runtime        RuntimeInfo     `json:"runtime"`
	Workspace      WorkspaceReady  `json:"workspace"`
	Grant          CredentialGrant `json:"grant"`
	PolicyRevision string          `json:"policyRevision"`
	SelfTestPassed bool            `json:"selfTestPassed"`
}

type ActivateIntent struct {
	AcquisitionID   string
	ExpectedVersion int64
	NewLeaseID      string
	Evidence        ReadyEvidence

	// Revalidated before activation; the original request's authorization expires
	// independently of provisioning. Never activate under a replacement epoch.
	RunAuthorization RunAuthorization
}

type RenewIntent struct {
	Key      MutationKey
	Proof    AdmissionProof
	ExtendBy time.Duration
}

type ReleaseIntent struct {
	Key                      MutationKey
	Proof                    AdmissionProof
	Mode                     string   // Retain or Destroy.
	RequiredSaveOperationIDs []string // Server-derived; not a request to create a checkpoint.
}

type DrainPlan struct {
	OperationID          string
	Lease                LeaseSnapshot
	Target               RuntimeTarget
	AdmissionEpoch       int64
	PendingExecutionIDs  []string
	StartingExecutionIDs []string
	RunningExecutionIDs  []string
	InFlightOperationIDs []string
	AccessSessionIDs     []string
	CredentialGrantIDs   []string
}

type DrainEvidence struct {
	OperationID              string                `json:"operationId"`
	LeaseID                  string                `json:"leaseId"`
	ExpectedLeaseVersion     int64                 `json:"expectedLeaseVersion"`
	Barrier                  *AdmissionBarrier     `json:"barrier"`
	PhysicalFence            *FenceEvidence        `json:"physicalFence"`
	OutstandingStarts        int64                 `json:"outstandingStarts"`
	UnknownStarts            int64                 `json:"unknownStarts"`
	OutstandingOperations    int64                 `json:"outstandingOperations"`
	UnknownOperations        int64                 `json:"unknownOperations"`
	ProcessTreeEmpty         bool                  `json:"processTreeEmpty"`
	AccessSessionsClosed     bool                  `json:"accessSessionsClosed"`
	CredentialsRevoked       bool                  `json:"credentialsRevoked"`
	CheckpointID             string                `json:"checkpointId"`
	RequiredSaveOperationIDs []string              `json:"requiredSaveOperationIds"`
	SaveProtection           *SaveProtectionState  `json:"saveProtection"`
	PublicationFence         *SavePublicationFence `json:"publicationFence"`
	EvidenceRef              string                `json:"evidenceRef"`
}

// FrozenRequiredSave retains the original semantic input for an obligation,
// including a save that failed before operation admission. Exactly one payload
// matches Requirement.Kind. Historical request keys/proofs cannot authorize retry;
// retry derives a new operation identity and bounded recovery permit instead.
type FrozenRequiredSave struct {
	Requirement sandboxv1.RequiredSaveRequirement  `json:"requirement"`
	Checkpoint  *sandboxv1.CreateCheckpointRequest `json:"checkpoint"`
	Artifact    *ArtifactSpec                      `json:"artifact"`
}

// SaveProtectionState lives in lease.drain_evidence. BeginRelease freezes every
// Profile-required obligation and its complete original input, not merely IDs of
// accepted operations. If this derivation is incomplete, deletion stays blocked.
// RequiredSaveOperationIDs retains admitted original IDs; it is not the complete
// requirement set. Snapshot tracks gaps and current attempts without rewriting
// original Failed rows. Intents and Snapshot.Target.RequiredSaves match exactly.
type SaveProtectionState struct {
	Snapshot           sandboxv1.BlockedSave `json:"snapshot"`
	Intents            []FrozenRequiredSave  `json:"intents"`
	PublicationEpoch   int64                 `json:"publicationEpoch"`
	DiscardOperationID *string               `json:"discardOperationId"`
}

// SavePublicationFence proves no operation in the closed epoch can still publish
// or read files being deleted. CoveredOperationIDs includes original attempts and
// every retry. Evidence must prove storage-side rejection/cancellation of late
// calls or conclusive completion of all such calls plus revoked save access.
// Advancing a database epoch/worker claim alone is insufficient. Unknown calls
// require reconciliation, or authoritative external fencing, before deletion.
type SavePublicationFence struct {
	ReleaseOperationID  string    `json:"releaseOperationId"`
	ClosedEpoch         int64     `json:"closedEpoch"`
	CoveredOperationIDs []string  `json:"coveredOperationIds"`
	EvidenceRef         string    `json:"evidenceRef"`
	ConfirmedAt         time.Time `json:"confirmedAt"`
}

// AllocationStore methods each own a short transaction; they perform no network IO.
// Reserve atomically enforces request identity, reuse/workspace ownership and quota.
// Activate transfers acquisition ownership to a lease and advances fencing token.
// BeginRelease closes admission before returning any dispatch/drain work.
// FinishRelease requires physical/runtime evidence and cannot infer it from row state.
// BeginRelease derives and persists required saves with the closed admission barrier;
// an empty caller-supplied set cannot bypass the bound Profile's save requirements.
// FinishRelease resolves each frozen requirement to its latest admitted attempt
// (original or authorized replacement), then checks confirmed publication,
// coverage, data identity and absence of writes after that snapshot. Missing, failed,
// pending or unknown saves keep the instance Draining/SaveBlocked and charged. Old
// CheckpointID alone is insufficient. Lease expiry stops work, not file retention.
// A successful replacement satisfies that requirement even when its original
// operation remains Failed; original operation IDs are history, not an AND gate.
// SaveProtection is required whenever the Profile has required saves, including
// gaps with no action ID. An empty operation-ID list never discharges them.
// Alternatively, FinishRelease verifies a committed DiscardReceipt and its exact
// waived requirements, plus SavePublicationFence and normal process isolation.
// All guards run before Provider.Delete, not only after data is already removed.
type AllocationStore interface {
	Reserve(context.Context, AcquireIntent) (AcquisitionSnapshot, error)
	GetAcquisition(context.Context, Principal, string) (AcquisitionSnapshot, error)
	FindAcquisition(ctx context.Context, principal Principal, requestKey string) (AcquisitionSnapshot, error)
	Activate(context.Context, ActivateIntent) (LeaseSnapshot, error)
	CancelAcquisition(context.Context, MutationKey, string) (MutationReceipt, error)
	Renew(context.Context, RenewIntent) (LeaseSnapshot, error)
	BeginRelease(context.Context, ReleaseIntent) (DrainPlan, error)
	FinishRelease(context.Context, DrainEvidence) (MutationReceipt, error)
}

type ExecutionIntent struct {
	Key      MutationKey
	Instance InstanceKey
	Command  CommandSpec
}

type ExecutionSnapshot struct {
	ID        string
	Version   int64
	Instance  InstanceKey
	Proof     AdmissionProof
	Command   CommandSpec
	State     string
	Backend   *BackendExecutionRef
	AttemptID *string
}

type DispatchTicket struct {
	Execution     ExecutionSnapshot
	AttemptID     string
	DispatchEpoch int64
	Target        RuntimeTarget
	Deadline      time.Time
}

type DispatchEvidence struct {
	ExecutionID     string
	AttemptID       string
	ExpectedVersion int64
	Result          StartResult
	ReasonCode      string
	ObservedAt      time.Time
}

// BeginDispatch locks execution and lease admission together. It records Starting
// BEFORE external Start. Expiring a dispatcher claim never resets Starting to Pending.
// It requires fresh Run authorization, not the evidence captured at Admit. The
// controller revalidates the persisted trusted principal/attempt using its own
// restricted delegation; it never stores or impersonates the old bearer token.
// Check action/resource/validity and the original lease's RunID/epoch under lock.
type ExecutionStore interface {
	Admit(context.Context, ExecutionIntent) (ExecutionSnapshot, error)
	BeginDispatch(
		ctx context.Context,
		executionID string,
		expectedVersion int64,
		dispatcherID string,
		authorization RunAuthorization,
	) (DispatchTicket, error)
	RecordDispatch(context.Context, DispatchEvidence) error
	RecordObservation(
		ctx context.Context,
		executionID string,
		expectedVersion int64,
		observation ExecutionObservation,
	) error
	BeginCancel(context.Context, MutationKey, string) (MutationReceipt, error)
	GetExecution(context.Context, Principal, string) (ExecutionSnapshot, error)
	FindExecution(ctx context.Context, principal Principal, leaseID, requestKey string) (ExecutionSnapshot, error)
}

type OperationIntent struct {
	Key         MutationKey
	OperationID string
	Instance    *InstanceKey
	Proof       *AdmissionProof

	// Typed DTO encoded by its action-specific service, not arbitrary caller JSON.
	Input json.RawMessage
}

type OperationSnapshot struct {
	ID             string
	Version        int64
	Action         string
	State          string
	Input          json.RawMessage
	Result         json.RawMessage
	ExternalRef    string
	ReconcileAfter *time.Time
}

type OperationCompletion struct {
	OperationID     string
	ExpectedVersion int64
	State           string // Succeeded, Failed (including cancellation), or Unknown.
	Result          json.RawMessage
	ExternalRef     string
	ReasonCode      string
}

// Begin reserves both the idempotency key and action-specific resource ownership.
// For worktrees/checkpoints/access, their intent row is committed in the same TX.
// Complete updates the result row and its event atomically. Unknown is reconciled,
// never automatically re-executed because a worker claim expired.
// RetryRequiredSave/DiscardBlockedSave admission uses BlockedSaveStore's atomic
// extensions of this ledger, never a separate Begin followed by a state update.
// Saves already enrolled by BeginRelease in SaveProtectionState (including their
// original attempts) complete through BlockedSaveStore.CompleteRequiredSave.
// Before enrollment, ordinary business checkpoints/artifacts use Complete without
// a ReleaseOperationID or protection revision. Complete and BeginRelease share
// the same resource/action lock order and decide enrollment under those locks:
// completion-first is frozen as observed success; enrollment-first rejects the
// generic completion with Conflict so the caller reloads and uses the protected
// path. Never re-publish on this conflict or bypass protection CAS.
// Failed never becomes Pending/Running/Succeeded. Unknown must be reconciled
// before replacement. After discard, late results remain audit evidence and
// cannot become a publish commit or a RequiredSaveResult.
type OperationStore interface {
	Begin(context.Context, OperationIntent) (OperationSnapshot, error)
	Get(context.Context, Principal, string) (OperationSnapshot, error)
	Lookup(context.Context, OperationIdentity) (OperationSnapshot, error)
	Complete(context.Context, OperationCompletion) error
}

type SaveRetryIntent struct {
	Key           MutationKey
	OperationID   string
	Request       sandboxv1.RetryRequiredSaveRequest
	Authorization SaveRecoveryAuthorization
}

type DiscardIntent struct {
	Key           MutationKey
	OperationID   string
	Request       sandboxv1.DiscardBlockedSaveRequest
	Authorization SaveRecoveryAuthorization
}

// RequiredSaveCompletion binds observed publication to the current attempt and
// the same durable protection revision used by retry/discard. Completion.Result
// follows the action JSON mapping; publication evidence must match the immutable
// requirement, source data and coverage. The worker's claim is not this CAS token.
type RequiredSaveCompletion struct {
	Completion                 OperationCompletion
	ReleaseOperationID         string
	RequirementID              string
	ExpectedProtectionRevision int64
	PublicationEpoch           int64
}

// BlockedSaveStore supplies narrow transactions on the existing OperationStore
// ledger and lease/instance/workspace rows. All recovery/publication/disposition
// writers use the same lock order and protection revision. No network IO in TX.
// Get requires Authorizer.Authorize("GetBlockedSave", exact resource scope) and
// returns a consistent snapshot; reads neither require nor renew a lease.
// Both writes validate a trusted unexpired authorization, principal and complete
// Target against server state, including both generations, data version, all
// requirements and snapshot revision. No block -> InvalidState; stale generation
// -> GenerationMismatch; other stale target -> RevisionConflict. Reject empty,
// omitted or extra requirements. Admission stays closed even after success.
// Exact-key lookup precedes current-target CAS after current read authorization:
// identical replay returns the recorded result, changed input conflicts. It never
// consumes confirmation twice or applies the old target to new data.
type BlockedSaveStore interface {
	GetBlockedSave(context.Context, Principal, sandboxv1.SandboxRef) (sandboxv1.BlockedSave, error)

	// Atomically records RetryRequiredSave with StoredSaveRetryRequest, reserves
	// one new attempt and save-only permit, updates progress/revision and events.
	// Only gaps or terminal Failed attempts can be replaced, preserving the frozen
	// requirement and prior attempt link. Pending/Running -> InvalidState;
	// Unknown -> OutcomeUnknown and reconcile first. Never resurrect Failed.
	BeginSaveRetry(context.Context, SaveRetryIntent) (OperationSnapshot, error)

	// Applies only after enrollment in SaveProtectionState. Locks the same
	// protection/instance/workspace/lease/action rows as retry and
	// discard, compares ExpectedProtectionRevision and PublicationEpoch, verifies
	// the selected latest attempt, then commits result, progress, revision and
	// events in one TX. Succeeded replaces the requirement's unsatisfied status,
	// not the original Failed record. A conflicting revision requires re-reading
	// and reconciling the observed result, never repeating the external publish.
	// Once discarded, reject save-success commit; retain late evidence for audit
	// and fencing. Unknown remains unresolved; Failed remains terminal.
	CompleteRequiredSave(context.Context, RequiredSaveCompletion) error

	// Atomically consumes the verified business confirmation for this action,
	// writes StoredDiscardRequest + DiscardReceipt as a Succeeded action and audit,
	// records the disposition and advances revision/publication epoch to close
	// further save admission/commit. Concurrent save completion/retry conflicts.
	// Waived IDs are server-derived unsatisfied members, including unadmitted gaps;
	// published members are retained. Keep charges and ownership until cleanup.
	// No external deletion occurs here; physical and publication fences remain due.
	ConfirmDiscard(context.Context, DiscardIntent) (sandboxv1.DiscardReceipt, error)
}

type ReconcileKind string

const (
	ReconcileAcquire   ReconcileKind = "Acquire"
	ReconcileInstance  ReconcileKind = "Instance"
	ReconcileLease     ReconcileKind = "Lease"
	ReconcileExecution ReconcileKind = "Execution"
	ReconcileOperation ReconcileKind = "Operation"
	ReconcilePool      ReconcileKind = "Pool"
)

type ReconcileClaim struct {
	Kind            ReconcileKind
	EntityID        string
	WorkerID        string
	ClaimToken      string
	ExpectedVersion int64
	ClaimUntil      time.Time
}

type ReconcileQuery struct {
	Kind     ReconcileKind
	WorkerID string
	Limit    int
	ClaimTTL time.Duration
}

type ReconcileStore interface {
	ClaimDue(context.Context, ReconcileQuery) ([]ReconcileClaim, error)
	ExtendClaim(context.Context, ReconcileClaim, time.Duration) (ReconcileClaim, error)
	Schedule(context.Context, ReconcileClaim, time.Time, string) error
}

// A reconciler returns after one bounded step; durable state is already committed.
type Reconciler interface {
	Step(context.Context, ReconcileClaim) error
}

type PoolIntent struct {
	PoolKey         string
	ExpectedVersion int64
	DesiredReady    int64
	MaxProvisioning int64
	InventoryBudget Resources
	Template        InstanceSpec
}

type PoolPlan struct {
	PoolKey    string
	Version    int64
	Create     []InstanceSpec
	Drain      []PhysicalRef
	RetryAfter time.Duration
}

// Plan atomically reserves inventory budget and records provisioning/deletion intent.
// Controller replicas cannot each create the same deficit independently.
type PoolStore interface {
	Plan(context.Context, PoolIntent) (PoolPlan, error)
}

type InstanceUpdate struct {
	Claim           ReconcileClaim
	Key             InstanceKey
	Observation     Observation
	Runtime         *RuntimeInfo
	NextState       string
	ReasonCode      string
	NextReconcileAt time.Time
}

type InstanceStore interface {
	GetSpec(context.Context, InstanceKey) (InstanceSpec, error)
	Record(context.Context, InstanceUpdate) error
	ConfirmDeleted(context.Context, ReconcileClaim, PhysicalRef, FenceEvidence) error
}

type EventRecord struct {
	TenantID   string
	ProjectID  string
	EntityKind string
	EntityID   string
	Sequence   int64
	Type       string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

type EventPage struct {
	Items                  []EventRecord
	NextSequence           int64
	FirstAvailableSequence int64
	Gap                    bool
}

// Events are appended in the state mutation transaction, never by a second write.
// EventReader is read-only; sequence allocation is owned by the relevant store.
type EventReader interface {
	Read(
		ctx context.Context,
		principal Principal,
		entityKind, entityID string,
		afterSequence int64,
		limit int,
	) (EventPage, error)
}

type LogAppend struct {
	ExecutionID      string
	Instance         InstanceKey
	BootID           string
	SourceCursor     string
	NextSourceCursor string
	Chunks           []OutputChunk
	EOF              bool
	Gap              bool
	Truncated        bool
}

type LogPosition struct {
	Cursor     string
	Bytes      int64
	ArchiveRef string
}

// Append is idempotent by execution, boot and source cursor; backend cursors stay private.
// Advance the durable read position only after bytes have been durably archived.
type OutputArchive interface {
	Append(context.Context, LogAppend) (LogPosition, error)
	Read(ctx context.Context, principal Principal, executionID, cursor string, maxBytes int64) (OutputPage, error)
}

package sandboxv1

// Mutation requires a nonempty caller-generated requestKey on every durable
// action, including cancel, renew, release, close, publish, and disable.
// Keys are scoped by authenticated tenant/caller, action, and target resource.
// Acquire uses (tenant, caller, requestKey); StartExecution uses
// leaseID plus requestKey. Equal keys with different semantic
// parameters return IdempotencyConflict. Transport waits are not semantic
// parameters. Expired retained results return RequestExpired, never a new action.
// The SDK MUST NOT automatically retry mutations, even with a request key.
type Mutation struct {
	RequestKey string `json:"requestKey"`
}

// PageOptions.Limit is in [1, 1000]. Tokens are opaque and bound to the original
// filters, tenant, and authorization scope; an empty token starts the first page.
type PageOptions struct {
	Limit     int    `json:"limit"`
	PageToken string `json:"pageToken"`
}

type Page[T any] struct {
	Items         []T     `json:"items"`
	NextPageToken *string `json:"nextPageToken"`
}

type ListProfilesRequest struct {
	PageOptions
	ProjectID            string       `json:"projectId"`
	RequiredCapabilities []Capability `json:"requiredCapabilities"`
	IncludeDisabled      bool         `json:"includeDisabled"`
}

// WorkspaceSpec refers only to authorized platform sources and storage. A
// PersistentVolume request requires StorageRef; EphemeralCopy requires it null.
// CheckpointID, when present, selects an immutable published data checkpoint.
// Mode and SavePolicyRef assert the selected Profile revision's exact policy;
// they cannot override it. Mismatches fail with InvalidArgument before allocation.
type WorkspaceSpec struct {
	ID            string        `json:"id"`
	Generation    int64         `json:"generation"`
	Mode          WorkspaceMode `json:"mode"`
	SourceRef     string        `json:"sourceRef"`
	BaseCommit    string        `json:"baseCommit"`
	DataVersion   *string       `json:"dataVersion"`
	StorageRef    *string       `json:"storageRef"`
	CheckpointID  *string       `json:"checkpointId"`
	SavePolicyRef PolicyRef     `json:"savePolicyRef"`
}

// AcquireRequest.New creates a new isolated environment, except that replay of
// the same key returns the original acquisition. Reuse requires explicit scope
// ownership validation. ProjectID, HolderRunID and Scope select resources; they
// cannot change the authenticated identity. No tenantID is accepted here.
// WaitTimeoutSeconds (0..30) bounds only the current HTTP wait. The server fixes
// ProvisionDeadline from acceptance plus ProvisionTimeoutSeconds (positive),
// and does not extend it on replay. Lease time starts only at Ready.
// Scope.Type must equal Profile.EnvironmentPolicy.ReuseScope. Provision and lease
// durations must fit published bounds; arbitrary lifecycle overrides are rejected.
// Mode New/Reuse selects allocation intent within that fixed ownership scope.
// HolderRunEpoch is positive and must match the authenticated, current upstream
// Run attempt. It is semantic input, included in the request hash. On takeover,
// look up the old acquisition first; a new epoch does not inherit its lease.
type AcquireRequest struct {
	Mutation
	ProjectID            string          `json:"projectId"`
	Mode                 AcquisitionMode `json:"mode"`
	Scope                ReuseScope      `json:"scope"`
	HolderRunID          string          `json:"holderRunId"`
	HolderRunEpoch       int64           `json:"holderRunEpoch"`
	ProfileRef           ProfileRef      `json:"profileRef"`
	RequiredCapabilities []Capability    `json:"requiredCapabilities"`

	// A lower-bound assertion, never an override of ProfileSpec.Isolation.
	// Omitted means the selected Profile's fixed level (default Hardened).
	// An unmet minimum rejects selection; it never silently changes the Profile.
	// Standard selection still requires a platform-verified controlled task scope.
	MinimumIsolation IsolationLevel `json:"minimumIsolation"`

	Workspace               WorkspaceSpec  `json:"workspace"`
	CapacityPolicy          CapacityPolicy `json:"capacityPolicy"`
	WaitTimeoutSeconds      int64          `json:"waitTimeoutSeconds"`
	ProvisionTimeoutSeconds int64          `json:"provisionTimeoutSeconds"`
	RequestedLeaseSeconds   int64          `json:"requestedLeaseSeconds"`
}

// Exactly one of AcquisitionID and RequestKey is required. Request-key lookup
// uses the authenticated original caller and can recover a lost Acquire response.
type GetAcquisitionRequest struct {
	AcquisitionID *string `json:"acquisitionId"`
	RequestKey    *string `json:"requestKey"`
}

// CancelAcquisitionRequest needs acquisition-owner authorization, not a lease:
// no lease exists before Ready. Ready races return InvalidState; release the
// assigned lease explicitly. Cleanup remains visible as an Operation.
type CancelAcquisitionRequest struct {
	Mutation
	AcquisitionID string `json:"acquisitionId"`
	Reason        string `json:"reason"`
}

// WaitAcquisitionRequest is local read-only polling. Context cancellation stops
// waiting, not provisioning. PollIntervalSeconds is positive; server retry
// hints can lengthen it. A terminal failure returns the last Acquisition and its
// Failure; local context/transport errors also preserve the last observed value.
type WaitAcquisitionRequest struct {
	AcquisitionID       string `json:"acquisitionId"`
	PollIntervalSeconds int64  `json:"pollIntervalSeconds"`
}

// EventCursor.AfterSequence is exclusive; nil starts at the earliest retained
// history only if nothing was lost, otherwise OutputGap must be reported.
// Sandbox subscriptions may span generations; each snapshot identifies its
// own generation. SSE ids and polling cursors share the same sequence space.
type EventCursor struct {
	AfterSequence *int64 `json:"afterSequence"`
}

type ListEventsRequest struct {
	EventCursor
	Limit              int   `json:"limit"`
	WaitTimeoutSeconds int64 `json:"waitTimeoutSeconds"`
}

// RenewLeaseRequest must fit the originally bound Profile lease range and cannot
// extend the lease beyond its original hard deadline. New Profile revisions do
// not change existing leases.
// The authenticated current Run epoch must equal the lease's HolderRunEpoch.
type RenewLeaseRequest struct {
	Mutation
	LeaseProof
	RequestedLeaseSeconds int64 `json:"requestedLeaseSeconds"`
}

// ReleaseLeaseRequest closes execution admission before draining processes and
// access sessions. Success means accepted, not Released. Destroy removes the
// instance but does not delete persistent workspace data. A replay returns the
// original operation after authorization; it never needs to revive the lease.
// Mode asserts the fixed Profile release mode, not a caller-selected override.
// A mismatch fails with InvalidArgument. Faults can require quarantine instead of
// Retain; destruction still requires the save guard as well as fencing evidence.
// Required-save failures block normal deletion and remain quota-charged after expiry.
// The service stops user processes while retaining files and limited save access.
// This API has no force/discard switch and cannot turn a failed save into success.
// A verified newer epoch of the same Run may request cleanup of an older lease;
// it cannot renew or write through it. Separately authorized cleanup also works
// after the Run terminates. Both paths preserve normal save and fencing guards.
type ReleaseLeaseRequest struct {
	Mutation
	LeaseProof
	Mode   ReleaseMode `json:"mode"`
	Reason string      `json:"reason"`
}

// StartExecutionRequest uses requestKey consistently across SDK actions.
// TimeoutSeconds is positive and must fit the lease hard deadline. Command is
// embedded so commandMode/argv/shell are top-level request fields. Credentials
// use authorized CredentialRefs, not environment literals. Values are not logs.
// TimeoutSeconds must also fit Profile.ExecutionPolicy.Timeout.
type StartExecutionRequest struct {
	Mutation
	LeaseProof
	Command
	Workspace      WorkspaceRef          `json:"workspace"`
	MemberID       *string               `json:"memberId"`
	Cwd            string                `json:"cwd"`
	Environment    []EnvironmentVariable `json:"environment"`
	CredentialRefs []string              `json:"credentialRefs"`
	TimeoutSeconds int64                 `json:"timeoutSeconds"`
	MaxOutputBytes int64                 `json:"maxOutputBytes"`
	OpenStdin      bool                  `json:"openStdin"`
}

// Select exactly ExecutionID or the pair LeaseID/RequestKey. The second form
// permits read-only reconciliation after a lost StartExecution response.
type GetExecutionRequest struct {
	ExecutionID *string `json:"executionId"`
	LeaseID     *string `json:"leaseId"`
	RequestKey  *string `json:"requestKey"`
}

// ReadExecutionOutputRequest returns whole event chunks with a bounded byte
// budget; MaxBytes must fit at least one permitted output chunk. ArchiveID is
// optional and must belong to this execution. A gap returns both available
// partial output and *Error with CodeOutputGap; it never silently skips bytes.
type ReadExecutionOutputRequest struct {
	ExecutionID string `json:"executionId"`
	EventCursor
	MaxBytes  int64   `json:"maxBytes"`
	ArchiveID *string `json:"archiveId"`
}

// WriteStdinRequest is non-replayable input. Sequence increases across writes
// for this execution; duplicate accepted sequences may be acknowledged without
// refeeding bytes only if verified by the Runtime. If acceptance is uncertain,
// return InputUnknown. RequestKey does not establish application consumption.
// CloseInput sends EOF after Data and rejects all later input. Never auto-retry.
type WriteStdinRequest struct {
	Mutation
	LeaseProof
	ExecutionID string `json:"executionId"`
	Sequence    int64  `json:"sequence"`
	Data        []byte `json:"data"`
	CloseInput  bool   `json:"closeInput"`
}

// CancelExecutionRequest refuses new input, waits GracePeriodSeconds, escalates
// to process-tree or sandbox termination, and records the confirmed granularity.
type CancelExecutionRequest struct {
	Mutation
	LeaseProof
	ExecutionID        string `json:"executionId"`
	GracePeriodSeconds int64  `json:"gracePeriodSeconds"`
	Reason             string `json:"reason"`
}

// File read/search are read-only despite POST routes. Authorization and exact
// sandbox generation are checked; neither request grants write access/renewal.
// Paths are constrained to authorized roots, including symlink resolution.
type ReadFileRequest struct {
	Sandbox  SandboxRef `json:"sandbox"`
	Path     string     `json:"path"`
	Offset   int64      `json:"offset"`
	MaxBytes int64      `json:"maxBytes"`
}

type FileWriteMode string

const (
	FileReplace    FileWriteMode = "Replace"
	FileCreateOnly FileWriteMode = "CreateOnly"
)

// WriteFileRequest replaces the complete contents, never appends. CreateOnly
// requires no existing path. ExpectedVersion requires FileConditionalWrite and
// is incompatible with CreateOnly. Unsupported conditions fail explicitly;
// a remote read followed by write is not accepted as atomic compare-and-replace.
// Permissions is an optional POSIX permission mask (0..0777), not file type bits.
type WriteFileRequest struct {
	Mutation
	LeaseProof
	Path            string        `json:"path"`
	Data            []byte        `json:"data"`
	Mode            FileWriteMode `json:"mode"`
	ExpectedVersion *FileVersion  `json:"expectedVersion"`
	Permissions     *uint32       `json:"permissions"`
}

type SearchFilesRequest struct {
	Sandbox       SandboxRef `json:"sandbox"`
	Root          string     `json:"root"`
	Kind          SearchKind `json:"kind"`
	Pattern       string     `json:"pattern"`
	Regex         bool       `json:"regex"`
	CaseSensitive bool       `json:"caseSensitive"`
	IncludeGlobs  []string   `json:"includeGlobs"`
	ExcludeGlobs  []string   `json:"excludeGlobs"`
	MaxResults    int64      `json:"maxResults"`
	MaxBytes      int64      `json:"maxBytes"`
	PageToken     string     `json:"pageToken"`
}

type CreateTerminalRequest struct {
	Mutation
	LeaseProof
	Cwd                      string                `json:"cwd"`
	Size                     TerminalSize          `json:"size"`
	Environment              []EnvironmentVariable `json:"environment"`
	RequestedLifetimeSeconds int64                 `json:"requestedLifetimeSeconds"`
}

type CreateBrowserRequest struct {
	Mutation
	LeaseProof
	InitialURL               *string `json:"initialUrl"`
	RequestedLifetimeSeconds int64   `json:"requestedLifetimeSeconds"`
}

// PreviewPort is 1..65535, scoped to the sandbox's authorized local application;
// clients cannot supply an arbitrary proxy upstream or internal control endpoint.
type CreatePreviewRequest struct {
	Mutation
	LeaseProof
	Port                     int   `json:"port"`
	RequestedLifetimeSeconds int64 `json:"requestedLifetimeSeconds"`
}

// ConnectAccessRequest requires Proof for Writer and requires null for Observer.
// Observers still require tenant/resource read authorization and a live session.
// Writers are checked against the exact bound lease/generation; at most one PTY
// writer may be active. A connection key cannot take over an existing writer.
type ConnectAccessRequest struct {
	Mutation
	SessionID string      `json:"sessionId"`
	Role      AccessRole  `json:"role"`
	Proof     *LeaseProof `json:"proof"`
}

type CloseAccessSessionRequest struct {
	Mutation
	LeaseProof
	SessionID string `json:"sessionId"`
	Reason    string `json:"reason"`
}

// Terminal writes share one strictly increasing connection-local sequence across
// Input, Resize, and Signal. The caller serializes these methods. Every message
// revalidates the writer grant and LeaseProof; no frame is automatically replayed.
type TerminalInputRequest struct {
	Mutation
	LeaseProof
	Sequence int64  `json:"sequence"`
	Data     []byte `json:"data"`
}

type TerminalResizeRequest struct {
	Mutation
	LeaseProof
	Sequence int64        `json:"sequence"`
	Size     TerminalSize `json:"size"`
}

type TerminalSignalRequest struct {
	Mutation
	LeaseProof
	Sequence int64          `json:"sequence"`
	Signal   TerminalSignal `json:"signal"`
}

// CreateWorktreeRequest uses a fixed commit and a fresh branch/path under the
// authorized worktree root. It does not clone a security boundary or change HOME.
type CreateWorktreeRequest struct {
	Mutation
	LeaseProof
	Workspace    WorkspaceRef `json:"workspace"`
	MemberID     string       `json:"memberId"`
	BaseCommit   string       `json:"baseCommit"`
	Branch       string       `json:"branch"`
	RelativePath string       `json:"relativePath"`
}

type ListWorktreesRequest struct {
	PageOptions
	Workspace      WorkspaceRef `json:"workspace"`
	IncludeRemoved bool         `json:"includeRemoved"`
}

// RemoveWorktreeRequest refuses active writers. DiscardChanges must explicitly
// authorize removal of dirty/untracked files. It does not delete the branch.
type RemoveWorktreeRequest struct {
	Mutation
	LeaseProof
	Workspace      WorkspaceRef `json:"workspace"`
	WorktreeID     string       `json:"worktreeId"`
	DiscardChanges bool         `json:"discardChanges"`
}

// CreateCheckpointRequest creates an immutable manifest/data snapshot, not a
// new sandbox or a process snapshot. ApplicationConsistent requires all writers
// (including direct process/shell writers) quiesced. If that cannot be proven, fail;
// do not downgrade silently to CrashConsistent. Ready follows durable publication
// in the existing storage service; requested coverage must be fully met.
// The Agent platform explicitly triggers checkpoints at its business milestones;
// the sandbox does not initiate periodic checkpoints. Acceptance is not Ready.
// The caller waits for durable publication before relying on the recovery point.
// On response loss, query the original operation key rather than issue a new key.
type CreateCheckpointRequest struct {
	Mutation
	LeaseProof
	Workspace             WorkspaceRef          `json:"workspace"`
	Consistency           CheckpointConsistency `json:"consistency"`
	Coverage              CheckpointCoverage    `json:"coverage"`
	QuiesceTimeoutSeconds int64                 `json:"quiesceTimeoutSeconds"`
}

type ListCheckpointsRequest struct {
	PageOptions
	WorkspaceID string `json:"workspaceId"`
	Generation  *int64 `json:"generation"`
}

// RestoreCheckpointRequest never creates a checkpoint. It reads a published
// checkpoint and writes to Target, which must be bound to Proof and authorized.
// InPlace requires ExpectedDataVersion and an existing target generation, stops
// every target writer, replaces covered data and advances workspace generation.
// IndependentCopy requires ExpectedDataVersion=null and a fresh authorized empty
// target workspace bound to Proof; it rejects any existing nonempty target.
// It can copy a lost source's checkpoint without mounting or mutating that source.
// Neither mode revives a lease, migrates Backend, resumes memory, or replays
// commands. The returned WorkspaceBinding is authoritative for subsequent writes.
type RestoreCheckpointRequest struct {
	Mutation
	LeaseProof
	CheckpointID          string       `json:"checkpointId"`
	Target                WorkspaceRef `json:"target"`
	Mode                  RestoreMode  `json:"mode"`
	ExpectedDataVersion   *string      `json:"expectedDataVersion"`
	QuiesceTimeoutSeconds int64        `json:"quiesceTimeoutSeconds"`
}

// OperationLookup selects exactly ID or the complete Kind/TargetID/RequestKey
// triple. TargetID is the action's path resource ID, including for workspace
// create actions. For profile disable it is profileID and ProfileRevision is
// additionally required. Lookup is authorized as the original action; an admin
// lookup uses GetTenantOperation and explicit tenant scope.
type OperationLookup struct {
	ID              *string        `json:"id"`
	Kind            *OperationKind `json:"kind"`
	TargetID        *string        `json:"targetId"`
	ProfileRevision *int64         `json:"profileRevision"`
	RequestKey      *string        `json:"requestKey"`
}

// RetryRequiredSaveRequest selects one requirement in the complete current Target.
// It can fill an unadmitted gap or replace a terminal Failed attempt. Unknown must
// first be reconciled; Pending/Running cannot be duplicated. Target is compared
// atomically, not trusted as a caller-defined save set. No LeaseProof is needed.
type RetryRequiredSaveRequest struct {
	Mutation
	Target        BlockedSaveTarget `json:"target"`
	RequirementID string            `json:"requirementId"`
}

// ConfirmationRef locates the existing business system's explicit confirmation;
// it is not a bearer credential or proof by itself. The server verifies its
// issuer, authenticated subject/delegate, owner, exact Target, validity and use.
// No userID/isConfirmed body field or worker token establishes discard authority.
// A stale snapshot returns RevisionConflict (generation changes use
// GenerationMismatch); the user must view and confirm the refreshed target.
// Exact-key replay returns the original receipt after authorization without
// consuming confirmation twice or applying it to a newer target.
type DiscardBlockedSaveRequest struct {
	Mutation
	Target          BlockedSaveTarget `json:"target"`
	ConfirmationRef string            `json:"confirmationRef"`
	Reason          string            `json:"reason"`
}

// TenantScope is a management target, NOT an assertion of caller identity.
// Every admin method requires an authenticated principal with an explicit grant
// over this nonempty TenantID. Wildcard/implicit-current-tenant scopes are invalid.
type TenantScope struct {
	TenantID string `json:"tenantId"`
}

// PublishProfileRequest allocates a new positive immutable revision on success;
// it never edits the referenced prior revision. ExpectedLatestRevision=null is
// create-only for a new profile ID; otherwise it is a publication CAS condition.
// Isolation defaults to Hardened. Publishing Standard requires platform-verified
// controlled-task eligibility; neither publication nor selection allows a caller
// to downgrade an already-bound Profile or bypass that eligibility check.
type PublishProfileRequest struct {
	Mutation
	ProjectID              string      `json:"projectId"`
	ProfileID              string      `json:"profileId"`
	ExpectedLatestRevision *int64      `json:"expectedLatestRevision"`
	Spec                   ProfileSpec `json:"spec"`
}

type ProfileDisableMode string

const (
	ProfileStopNew         ProfileDisableMode = "StopNew"
	ProfileTerminateActive ProfileDisableMode = "TerminateActive"
)

// StopNew lets existing leases finish under their original deadlines and drains
// unclaimed old inventory. TerminateActive additionally closes admission and
// confirms termination of active instances; both complete asynchronously.
type DisableProfileRequest struct {
	Mutation
	ProfileRef ProfileRef         `json:"profileRef"`
	Mode       ProfileDisableMode `json:"mode"`
	Reason     string             `json:"reason"`
}

type ListPoolsRequest struct {
	PageOptions
	ProjectID       *string     `json:"projectId"`
	ProfileRef      *ProfileRef `json:"profileRef"`
	IncludeDisabled bool        `json:"includeDisabled"`
}

// PutPoolRequest is full desired-configuration replacement. ExpectedRevision
// null is create-only; otherwise CAS is required. Identity-defining Profile and
// placement/volume layout cannot change for an existing pool; use a new pool ID.
// Put enables replenishment. Observed inventory is never client-writable.
type PutPoolRequest struct {
	Mutation
	PoolID           string   `json:"poolId"`
	ExpectedRevision *int64   `json:"expectedRevision"`
	Spec             PoolSpec `json:"spec"`
}

type DisablePoolRequest struct {
	Mutation
	PoolID           string `json:"poolId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Reason           string `json:"reason"`
}

// PutQuotaRequest replaces limits, never usage. Reductions below existing usage
// block new reservations; they do not terminate existing leases. Null revision
// is create-only; existing quota changes require a positive CAS revision.
type PutQuotaRequest struct {
	Mutation
	ExpectedRevision *int64      `json:"expectedRevision"`
	Limits           QuotaLimits `json:"limits"`
}

// Package sandboxv1 defines the public sandbox control-plane contract.
// It contains no transport, service, provider, or runtime implementation.
//
// JSON field names use lower camel case, including Id and Url. Times use
// time.Time's RFC 3339 JSON representation (fractional seconds are allowed).
// Binary input and output use []byte and therefore base64 JSON strings.
// Nullable observed fields use pointers without omitempty: null means not yet
// known or not applicable, never zero, success, or an empty identifier.
// Required collections are encoded as [] when empty, not null.
//
// Every generation, revision, fencing token, and event/input sequence is int64
// in [1, MaxCounter]. Counters never wrap or reset within their identity scope;
// exhaustion rejects the operation with CounterExhausted. Nil cursors mean the
// beginning of a stream; zero is not an event sequence. Offsets and counts may
// be zero, are nonnegative, and must not exceed MaxCounter or a smaller policy
// limit. This bound also permits exact JSON numbers in JavaScript clients.
package sandboxv1

import "time"

const MaxCounter int64 = 1<<53 - 1

// ResourceScope is server-derived ownership, never an authentication input.
type ResourceScope struct {
	TenantID  string `json:"tenantId"`
	ProjectID string `json:"projectId"`
}

type ProfileRef struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

type SandboxRef struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
}

type WorkspaceRef struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
}

type PolicyRef struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

// LeaseProof binds a write to the authenticated holder and exact instance.
// Possession of these values alone grants no authority. Every field is required.
// Worker writes/renewals also require a verified current Run attempt matching
// Lease.HolderRunEpoch. A replacement worker cannot inherit the old write grant.
type LeaseProof struct {
	SandboxID    string `json:"sandboxId"`
	Generation   int64  `json:"generation"`
	LeaseID      string `json:"leaseId"`
	FencingToken int64  `json:"fencingToken"`
}

type Capability string

const (
	CapabilityExec                 Capability = "Exec"
	CapabilityAsyncExec            Capability = "AsyncExec"
	CapabilityIncrementalOutput    Capability = "IncrementalOutput"
	CapabilityCancelProcessTree    Capability = "CancelProcessTree"
	CapabilityFileRead             Capability = "FileRead"
	CapabilityFileWrite            Capability = "FileWrite"
	CapabilityFileSearch           Capability = "FileSearch"
	CapabilityFileConditionalWrite Capability = "FileConditionalWrite"
	CapabilityStdin                Capability = "Stdin"
	CapabilityPty                  Capability = "Pty"
	CapabilityBrowser              Capability = "Browser"
	CapabilityPreview              Capability = "Preview"
	CapabilityLayeredWorkspace     Capability = "LayeredWorkspace"
	CapabilityWorkspaceCheckpoint  Capability = "WorkspaceCheckpoint"
	CapabilityProcessCheckpoint    Capability = "ProcessCheckpoint"
)

type ConditionStatus string

const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// Condition is optional diagnostic evidence, not a capability declaration or
// a second lifecycle state machine. Missing conditions mean unreported, not
// false. Unknown means the server explicitly could not determine the condition.
// Condition types are open strings; clients must preserve unfamiliar types.
type Condition struct {
	Type               string          `json:"type"`
	Status             ConditionStatus `json:"status"`
	Reason             string          `json:"reason"`
	Message            string          `json:"message"`
	ObservedGeneration *int64          `json:"observedGeneration"`
	LastTransitionAt   time.Time       `json:"lastTransitionAt"`
}

// IsolationLevel is fixed by the published Profile. Hardened is the default;
// Standard is permitted only for platform-verified controlled tasks.
type IsolationLevel string

const (
	IsolationStandard IsolationLevel = "Standard"
	IsolationHardened IsolationLevel = "Hardened"
)

type RuntimeKind string

const (
	RuntimeAIO   RuntimeKind = "Aio"
	RuntimeExecd RuntimeKind = "Execd"
)

// ResourceLimits contains platform units, not Kubernetes resource quantities.
type ResourceLimits struct {
	CPUMillis          int64 `json:"cpuMillis"`
	MemoryBytes        int64 `json:"memoryBytes"`
	EphemeralDiskBytes int64 `json:"ephemeralDiskBytes"`
	MaxProcesses       int64 `json:"maxProcesses"`
}

type WorkspaceMode string

const (
	WorkspaceEphemeralCopy    WorkspaceMode = "EphemeralCopy"
	WorkspacePersistentVolume WorkspaceMode = "PersistentVolume"
)

// RecoveryLevel promises file recovery only, never process, memory, or automatic
// command replay. Checkpointed requires a successfully published durable snapshot;
// no checkpoint means no recoverable edits. Persistent follows verified storage
// durability and requires old-writer fencing before remounting on another instance.
type RecoveryLevel string

const (
	RecoveryEphemeral    RecoveryLevel = "Ephemeral"
	RecoveryCheckpointed RecoveryLevel = "Checkpointed"
	RecoveryPersistent   RecoveryLevel = "Persistent"
)

type WorkspaceLayout struct {
	RepositoryRoot string `json:"repositoryRoot"`
	WorktreesRoot  string `json:"worktreesRoot"`
	ArtifactsRoot  string `json:"artifactsRoot"`
	TemporaryRoot  string `json:"temporaryRoot"`
	HomeRoot       string `json:"homeRoot"`
	SkillsRoot     string `json:"skillsRoot"`
}

// LayeredWorkspace declares how an EphemeralCopy workspace is assembled from a
// read-only base layer and a per-instance writable layer. The base layer is
// content-addressed and may be shared read-only across instances on one node;
// the writable layer is discarded with the instance. PersistentVolume profiles
// leave Engine empty and mount the authorized volume directly. A profile with a
// non-empty Engine must also declare CapabilityLayeredWorkspace; instance
// readiness must verify the mount facts rather than trusting the declaration.
type LayeredWorkspace struct {
	// Engine identifies the verified layering implementation, for example
	// "overlayfs". Empty means no layering; the Profile must not claim
	// fast layered restore without a verified engine.
	Engine string `json:"engine,omitempty"`

	// BaseLayerPolicy pins which base layers a revision may use: repository
	// snapshots, published checkpoints, or both.
	BaseLayerPolicy string `json:"baseLayerPolicy,omitempty"`
}

type ReadonlyAsset struct {
	SourceRef string `json:"sourceRef"`
	Digest    string `json:"digest"`
	MountPath string `json:"mountPath"`
}

// Lease bounds satisfy 0 < Min <= Default <= Max <= HardLifetime. Equal Min
// and Max disable caller adjustment. Idle retention is fixed by the Profile;
// Destroy requires it to be zero. Terminal retention never outlives a lease.
// HardLifetime ends user execution; it is not a deadline for deleting protected files.
type LeasePolicy struct {
	MinLeaseSeconds                    int64 `json:"minLeaseSeconds"`
	DefaultLeaseSeconds                int64 `json:"defaultLeaseSeconds"`
	MaxLeaseSeconds                    int64 `json:"maxLeaseSeconds"`
	HardLifetimeSeconds                int64 `json:"hardLifetimeSeconds"`
	IdleRetentionSeconds               int64 `json:"idleRetentionSeconds"`
	TerminalDisconnectRetentionSeconds int64 `json:"terminalDisconnectRetentionSeconds"`
}

// TimeoutPolicy is platform-published: 0 < Min <= Default <= Max. Equal bounds
// disable caller adjustment. Requests must supply a value in this range.
type TimeoutPolicy struct {
	MinSeconds     int64 `json:"minSeconds"`
	DefaultSeconds int64 `json:"defaultSeconds"`
	MaxSeconds     int64 `json:"maxSeconds"`
}

// EnvironmentPolicy fixes reuse ownership and normal release behavior. Retain
// always drains processes before IdleBound; used instances never enter WarmPool.
type EnvironmentPolicy struct {
	ReuseScope  ReuseScopeType `json:"reuseScope"`
	ReleaseMode ReleaseMode    `json:"releaseMode"`
}

// WorkspaceDataPolicy fixes file retention/coverage via an immutable authorized
// policy reference. RecoveryLevel describes data, not a process resume promise.
// Default coding profiles pair EphemeralCopy with Checkpointed; development
// profiles pair PersistentVolume with Persistent. These are published defaults,
// not a client-side fallback or an implicit conversion between recovery levels.
// Required-save failures block normal deletion until publication is confirmed or
// an explicitly authorized disposition exists; time or quota pressure is not consent.
type WorkspaceDataPolicy struct {
	SavePolicyRef PolicyRef     `json:"savePolicyRef"`
	RecoveryLevel RecoveryLevel `json:"recoveryLevel"`
}

// ExecutionPolicy bounds commands independently of output connections. Closing
// an output stream does not stop a process. Lease expiry/release always drains
// processes; terminal disconnect retention is bounded by the same lease deadline.
type ExecutionPolicy struct {
	Timeout TimeoutPolicy `json:"timeout"`
}

type OutputPolicy struct {
	MaxExecutionOutputBytes int64 `json:"maxExecutionOutputBytes"`
	MaxReadBytes            int64 `json:"maxReadBytes"`
	RetentionSeconds        int64 `json:"retentionSeconds"`
}

// ProfileSpec fixes a verified combination. Placement/storage policy references
// are authorized platform configuration IDs, not arbitrary Pod/PVC specs.
// Publishing requires the autonomous-agent baseline from design section 4.3.
// WorkspaceModes contains exactly one verified mode per revision. Lifecycle
// policies are mandatory; no request may assemble an alternative combination.
// Publication resolves omitted Isolation to Hardened and stores an explicit level.
// Standard requires platform verification of the controlled task scope; callers
// cannot lower the bound Profile's isolation or fall back to Standard on failure.
type ProfileSpec struct {
	DisplayName         string              `json:"displayName"`
	Provider            string              `json:"provider"`
	Runtime             RuntimeKind         `json:"runtime"`
	RuntimeVersion      string              `json:"runtimeVersion"`
	AdapterVersion      string              `json:"adapterVersion"`
	ProtocolVersion     string              `json:"protocolVersion"`
	ImageDigest         string              `json:"imageDigest"`
	Architecture        string              `json:"architecture"`
	ResourceClass       string              `json:"resourceClass"`
	Resources           ResourceLimits      `json:"resources"`
	Isolation           IsolationLevel      `json:"isolation"`
	IsolationPolicy     PolicyRef           `json:"isolationPolicy"`
	AccessPolicy        PolicyRef           `json:"accessPolicy"`
	NetworkPolicy       PolicyRef           `json:"networkPolicy"`
	PlacementPolicy     PolicyRef           `json:"placementPolicy"`
	StoragePolicy       PolicyRef           `json:"storagePolicy"`
	ReadonlyAssetDigest string              `json:"readonlyAssetDigest"`
	ReadonlyAssets      []ReadonlyAsset     `json:"readonlyAssets"`
	WorkspaceModes      []WorkspaceMode     `json:"workspaceModes"`
	Layout              WorkspaceLayout     `json:"layout"`
	LayeredWorkspace    LayeredWorkspace    `json:"layeredWorkspace"`
	Capabilities        []Capability        `json:"capabilities"`
	EnvironmentPolicy   EnvironmentPolicy   `json:"environmentPolicy"`
	WorkspaceDataPolicy WorkspaceDataPolicy `json:"workspaceDataPolicy"`
	ExecutionPolicy     ExecutionPolicy     `json:"executionPolicy"`
	ProvisionTimeout    TimeoutPolicy       `json:"provisionTimeout"`
	LeasePolicy         LeasePolicy         `json:"leasePolicy"`
	OutputPolicy        OutputPolicy        `json:"outputPolicy"`
	ReadinessCheckRefs  []string            `json:"readinessCheckRefs"`
}

// ProfileRevision.Spec is immutable after publication; Enabled is administrative
// availability. Capabilities in Spec are declarations, not instance negotiation.
type ProfileRevision struct {
	Scope         ResourceScope `json:"scope"`
	Ref           ProfileRef    `json:"ref"`
	Spec          ProfileSpec   `json:"spec"`
	Enabled       bool          `json:"enabled"`
	PublishedAt   time.Time     `json:"publishedAt"`
	DisabledAt    *time.Time    `json:"disabledAt"`
	DisableReason *string       `json:"disableReason"`
	Conditions    []Condition   `json:"conditions,omitempty"`
}

type ReuseScopeType string

const (
	ReuseTask      ReuseScopeType = "Task"
	ReuseSession   ReuseScopeType = "Session"
	ReuseWorkspace ReuseScopeType = "Workspace"
)

// ReuseScope identifies caller-owned reuse intent. A Session ID is opaque to
// the sandbox center; it is not a managed CLI session or a resume handle.
type ReuseScope struct {
	Type ReuseScopeType `json:"type"`
	ID   string         `json:"id"`
}

type AcquisitionMode string

const (
	AcquisitionNew   AcquisitionMode = "New"
	AcquisitionReuse AcquisitionMode = "Reuse"
)

type CapacityPolicy string

const (
	CapacityAllowCold        CapacityPolicy = "AllowCold"
	CapacityWaitForWarm      CapacityPolicy = "WaitForWarm"
	CapacityFailIfNoCapacity CapacityPolicy = "FailIfNoCapacity"
)

type AcquisitionStatus string

const (
	AcquisitionQueued       AcquisitionStatus = "Queued"
	AcquisitionProvisioning AcquisitionStatus = "Provisioning"
	AcquisitionPreparing    AcquisitionStatus = "Preparing"
	AcquisitionReady        AcquisitionStatus = "Ready"
	AcquisitionCancelling   AcquisitionStatus = "Cancelling"
	AcquisitionCancelled    AcquisitionStatus = "Cancelled"
	AcquisitionFailed       AcquisitionStatus = "Failed"
	AcquisitionTimedOut     AcquisitionStatus = "TimedOut"
)

type AllocationSource string

const (
	AllocationWarmPool AllocationSource = "WarmPool"
	AllocationCold     AllocationSource = "Cold"
	AllocationReuse    AllocationSource = "Reuse"
)

// Acquisition.ReadyResult is non-null only after Ready. A replay preserves the
// original result; it does not renew a returned lease or revive a released one.
type Acquisition struct {
	Scope             ResourceScope      `json:"scope"`
	ID                string             `json:"id"`
	RequestKey        string             `json:"requestKey"`
	CallerID          string             `json:"callerId"`
	HolderRunID       string             `json:"holderRunId"`
	HolderRunEpoch    int64              `json:"holderRunEpoch"`
	Mode              AcquisitionMode    `json:"mode"`
	ReuseScope        ReuseScope         `json:"reuseScope"`
	ProfileRef        ProfileRef         `json:"profileRef"`
	Status            AcquisitionStatus  `json:"status"`
	Phase             *string            `json:"phase"`
	Attempt           int64              `json:"attempt"`
	Sandbox           *SandboxRef        `json:"sandbox"`
	AllocationSource  *AllocationSource  `json:"allocationSource"`
	ReadyResult       *AcquisitionResult `json:"readyResult"`
	CreatedAt         time.Time          `json:"createdAt"`
	ProvisionDeadline time.Time          `json:"provisionDeadline"`
	CompletedAt       *time.Time         `json:"completedAt"`
	RetryAfterSeconds *int64             `json:"retryAfterSeconds"`
	EventsURL         *string            `json:"eventsUrl"`
	Failure           *Error             `json:"failure"`
	Conditions        []Condition        `json:"conditions,omitempty"`
}

type AcquisitionResult struct {
	Sandbox Sandbox `json:"sandbox"`
	Lease   Lease   `json:"lease"`
}

type SandboxStatus string

const (
	SandboxProvisioning SandboxStatus = "Provisioning"
	SandboxWarmReady    SandboxStatus = "WarmReady"
	SandboxClaimed      SandboxStatus = "Claimed"
	SandboxPreparing    SandboxStatus = "Preparing"
	SandboxReady        SandboxStatus = "Ready"
	SandboxIdleBound    SandboxStatus = "IdleBound"
	SandboxLost         SandboxStatus = "Lost"
	SandboxFailed       SandboxStatus = "Failed"
	SandboxDraining     SandboxStatus = "Draining"
	SandboxDeleting     SandboxStatus = "Deleting"
	SandboxDeleted      SandboxStatus = "Deleted"
)

// Sandbox contains no Pod identity or internal Runtime endpoint. Its effective
// capabilities are the Profile/adapter/verified-runtime/authorization
// intersection. Absence means unsupported; SSE never implies Pty or Stdin.
type Sandbox struct {
	Scope         ResourceScope     `json:"scope"`
	ID            string            `json:"id"`
	Generation    int64             `json:"generation"`
	Revision      int64             `json:"revision"`
	ProfileRef    ProfileRef        `json:"profileRef"`
	Status        SandboxStatus     `json:"status"`
	Phase         *string           `json:"phase"`
	Reason        *string           `json:"reason"`
	Capabilities  []Capability      `json:"capabilities"`
	Isolation     IsolationLevel    `json:"isolation"`
	Workspace     *WorkspaceBinding `json:"workspace"`
	LeaseID       *string           `json:"leaseId"`
	CreatedAt     time.Time         `json:"createdAt"`
	ObservedAt    *time.Time        `json:"observedAt"`
	HardExpiresAt *time.Time        `json:"hardExpiresAt"`
	IdleExpiresAt *time.Time        `json:"idleExpiresAt"`
	DeletedAt     *time.Time        `json:"deletedAt"`
	Failure       *Error            `json:"failure"`
	Conditions    []Condition       `json:"conditions,omitempty"`
}

type LeaseStatus string

const (
	LeaseActive        LeaseStatus = "Active"
	LeaseReleasing     LeaseStatus = "Releasing"
	LeaseExpiredStatus LeaseStatus = "Expired"
	LeaseReleased      LeaseStatus = "Released"
	LeaseQuarantined   LeaseStatus = "Quarantined"
)

type ReleaseMode string

const (
	ReleaseRetain  ReleaseMode = "Retain"
	ReleaseDestroy ReleaseMode = "Destroy"
)

// Expired means admission has ended, not that old processes have stopped.
// Released requires confirmed cleanup. Retain never returns to a public pool.
type Lease struct {
	Scope          ResourceScope      `json:"scope"`
	ID             string             `json:"id"`
	Sandbox        SandboxRef         `json:"sandbox"`
	FencingToken   int64              `json:"fencingToken"`
	HolderRunID    string             `json:"holderRunId"`
	HolderRunEpoch int64              `json:"holderRunEpoch"`
	CallerID       string             `json:"callerId"`
	Permissions    []string           `json:"permissions"`
	Status         LeaseStatus        `json:"status"`
	CreatedAt      time.Time          `json:"createdAt"`
	ExpiresAt      time.Time          `json:"expiresAt"`
	HardExpiresAt  time.Time          `json:"hardExpiresAt"`
	ReleaseMode    *ReleaseMode       `json:"releaseMode"`
	ReleasedAt     *time.Time         `json:"releasedAt"`
	Termination    *TerminationResult `json:"termination"`
	Failure        *Error             `json:"failure"`
}

type WorkspaceStatus string

const (
	WorkspacePreparing   WorkspaceStatus = "Preparing"
	WorkspaceReady       WorkspaceStatus = "Ready"
	WorkspaceRestoring   WorkspaceStatus = "Restoring"
	WorkspaceQuarantined WorkspaceStatus = "Quarantined"
	WorkspaceFailed      WorkspaceStatus = "Failed"
)

type WorkspaceBinding struct {
	Scope         ResourceScope   `json:"scope"`
	Ref           WorkspaceRef    `json:"ref"`
	Mode          WorkspaceMode   `json:"mode"`
	SourceRef     string          `json:"sourceRef"`
	BaseCommit    string          `json:"baseCommit"`
	DataVersion   string          `json:"dataVersion"`
	StorageRef    *string         `json:"storageRef"`
	RecoveryLevel RecoveryLevel   `json:"recoveryLevel"`
	Status        WorkspaceStatus `json:"status"`
	Layout        WorkspaceLayout `json:"layout"`
	WriterLeaseID *string         `json:"writerLeaseId"`
	CheckpointID  *string         `json:"checkpointId"`
	SavePolicyRef PolicyRef       `json:"savePolicyRef"`
	Failure       *Error          `json:"failure"`
}

type CommandMode string

const (
	CommandArgv  CommandMode = "Argv"
	CommandShell CommandMode = "Shell"
)

// Command requires exactly one of nonempty Argv or Shell. No implicit shell
// interpolation applies to Argv. Shell uses the Profile's fixed interpreter.
type Command struct {
	Mode  CommandMode `json:"commandMode"`
	Argv  []string    `json:"argv"`
	Shell *string     `json:"shell"`
}

type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ExecutionStatus string

const (
	ExecutionPending       ExecutionStatus = "Pending"
	ExecutionStarting      ExecutionStatus = "Starting"
	ExecutionRunning       ExecutionStatus = "Running"
	ExecutionSucceeded     ExecutionStatus = "Succeeded"
	ExecutionFailed        ExecutionStatus = "Failed"
	ExecutionTimedOut      ExecutionStatus = "TimedOut"
	ExecutionCancelled     ExecutionStatus = "Cancelled"
	ExecutionUnknownStatus ExecutionStatus = "Unknown"
)

type OutputCompleteness string

const (
	OutputPending    OutputCompleteness = "Pending"
	OutputComplete   OutputCompleteness = "Complete"
	OutputTruncated  OutputCompleteness = "Truncated"
	OutputIncomplete OutputCompleteness = "Incomplete"
)

type TerminationGranularity string

const (
	TerminationProcessTree TerminationGranularity = "ProcessTree"
	TerminationSandbox     TerminationGranularity = "Sandbox"
)

type TerminationResult struct {
	Granularity TerminationGranularity `json:"granularity"`
	Confirmed   bool                   `json:"confirmed"`
	ConfirmedAt *time.Time             `json:"confirmedAt"`
	Reason      string                 `json:"reason"`
}

// Execution.Unknown is unresolved, not a successful or safely retryable result.
// Nonzero ExitCode is a command result, not a transport error. ExitCode remains
// null unless observed; cancellation by destroying a sandbox may have no code.
type Execution struct {
	Scope              ResourceScope      `json:"scope"`
	ID                 string             `json:"id"`
	RequestKey         string             `json:"requestKey"`
	Sandbox            SandboxRef         `json:"sandbox"`
	LeaseID            string             `json:"leaseId"`
	Workspace          WorkspaceRef       `json:"workspace"`
	MemberID           *string            `json:"memberId"`
	Status             ExecutionStatus    `json:"status"`
	Cwd                string             `json:"cwd"`
	Command            Command            `json:"command"`
	CreatedAt          time.Time          `json:"createdAt"`
	Deadline           time.Time          `json:"deadline"`
	StartedAt          *time.Time         `json:"startedAt"`
	CompletedAt        *time.Time         `json:"completedAt"`
	ExitCode           *int               `json:"exitCode"`
	ExitSignal         *string            `json:"exitSignal"`
	OutputCompleteness OutputCompleteness `json:"outputCompleteness"`
	OutputBytes        int64              `json:"outputBytes"`
	LastSequence       *int64             `json:"lastSequence"`
	Archives           []ArchiveRef       `json:"archives"`
	StdinClosed        bool               `json:"stdinClosed"`
	Termination        *TerminationResult `json:"termination"`
	Failure            *Error             `json:"failure"`
}

type OutputStream string

const (
	OutputStdout   OutputStream = "Stdout"
	OutputStderr   OutputStream = "Stderr"
	OutputCombined OutputStream = "Combined"
)

// ArchiveRef is a reference to existing storage, not an unauthenticated URL.
// Reading an archive requires independent tenant/resource authorization.
type ArchiveRef struct {
	ID            string     `json:"id"`
	StorageRef    string     `json:"storageRef"`
	FirstSequence int64      `json:"firstSequence"`
	LastSequence  int64      `json:"lastSequence"`
	SizeBytes     int64      `json:"sizeBytes"`
	SHA256        string     `json:"sha256"`
	ExpiresAt     *time.Time `json:"expiresAt"`
}

type OutputChunk struct {
	ExecutionID string       `json:"executionId"`
	Sequence    int64        `json:"sequence"`
	Stream      OutputStream `json:"stream"`
	Timestamp   time.Time    `json:"timestamp"`
	Data        []byte       `json:"data"`
}

// OutputGap identifies unavailable event sequences. A null last sequence means
// the end of the gap is not known. Gaps must also surface as CodeOutputGap.
type OutputGap struct {
	FirstMissingSequence      int64        `json:"firstMissingSequence"`
	LastMissingSequence       *int64       `json:"lastMissingSequence"`
	EarliestAvailableSequence *int64       `json:"earliestAvailableSequence"`
	Archives                  []ArchiveRef `json:"archives"`
}

type ExecutionOutput struct {
	ExecutionID       string             `json:"executionId"`
	Chunks            []OutputChunk      `json:"chunks"`
	NextAfterSequence *int64             `json:"nextAfterSequence"`
	HasMore           bool               `json:"hasMore"`
	Complete          bool               `json:"complete"`
	Completeness      OutputCompleteness `json:"completeness"`
	Gap               *OutputGap         `json:"gap"`
	Archives          []ArchiveRef       `json:"archives"`
}

type EventType string

const (
	EventSandboxChanged   EventType = "SandboxChanged"
	EventExecutionChanged EventType = "ExecutionChanged"
	EventOutput           EventType = "Output"
	EventOutputGap        EventType = "OutputGap"
)

// Event has exactly one payload matching Type. Sequence is scoped to the
// resource event log and is the decimal SSE id/Last-Event-ID. Output chunks use
// that same execution event sequence; state events may fall between chunks.
// A sequence is never a byte offset or a vendor cursor. New event types require
// compatible SDK support; an implementation must not silently advance past an
// undecodable event (CodeProtocolError).
type Event struct {
	Type       EventType    `json:"type"`
	ResourceID string       `json:"resourceId"`
	Sequence   int64        `json:"sequence"`
	Timestamp  time.Time    `json:"timestamp"`
	Sandbox    *Sandbox     `json:"sandbox"`
	Execution  *Execution   `json:"execution"`
	Output     *OutputChunk `json:"output"`
	Gap        *OutputGap   `json:"gap"`
}

// EventPage provides REST polling of the same ordered log as SSE.
// Closed means the resource log is final and all events have been drained.
type EventPage struct {
	Events            []Event    `json:"events"`
	NextAfterSequence *int64     `json:"nextAfterSequence"`
	HasMore           bool       `json:"hasMore"`
	Closed            bool       `json:"closed"`
	RetryAfterSeconds *int64     `json:"retryAfterSeconds"`
	Gap               *OutputGap `json:"gap"`
}

type OperationStatus string

const (
	OperationPending   OperationStatus = "Pending"
	OperationRunning   OperationStatus = "Running"
	OperationSucceeded OperationStatus = "Succeeded"
	OperationFailed    OperationStatus = "Failed"
	OperationUnknown   OperationStatus = "Unknown"
)

type OperationKind string

const (
	OperationRenewLease           OperationKind = "RenewLease"
	OperationWriteFile            OperationKind = "WriteFile"
	OperationWriteStdin           OperationKind = "WriteStdin"
	OperationCreateTerminal       OperationKind = "CreateTerminal"
	OperationCreateBrowser        OperationKind = "CreateBrowser"
	OperationCreatePreview        OperationKind = "CreatePreview"
	OperationConnectAccessSession OperationKind = "ConnectAccessSession"
	OperationWriteTerminal        OperationKind = "WriteTerminal"
	OperationResizeTerminal       OperationKind = "ResizeTerminal"
	OperationSignalTerminal       OperationKind = "SignalTerminal"
	OperationPublishProfile       OperationKind = "PublishProfile"
	OperationSetPool              OperationKind = "SetPool"
	OperationSetQuota             OperationKind = "SetQuota"
	OperationCancelAcquisition    OperationKind = "CancelAcquisition"
	OperationReleaseLease         OperationKind = "ReleaseLease"
	OperationCancelExecution      OperationKind = "CancelExecution"
	OperationCloseAccessSession   OperationKind = "CloseAccessSession"
	OperationCreateWorktree       OperationKind = "CreateWorktree"
	OperationRemoveWorktree       OperationKind = "RemoveWorktree"
	OperationCreateCheckpoint     OperationKind = "CreateCheckpoint"
	OperationRestoreCheckpoint    OperationKind = "RestoreCheckpoint"
	OperationDisableProfile       OperationKind = "DisableProfile"
	OperationDisablePool          OperationKind = "DisablePool"
	OperationRetryRequiredSave    OperationKind = "RetryRequiredSave"
	OperationDiscardBlockedSave   OperationKind = "DiscardBlockedSave"
)

// Operation tracks accepted actions, including queryable synchronous mutations
// after a response is lost. Internal credential/artifact actions are not public.
// Unknown requires reconciliation
// and is never permission to repeat effects. Succeeded has a non-null Result;
// Failed has a non-null Failure. Other states have neither. TargetID may be null
// until allocation; RequestKey can locate an operation after a lost response.
type Operation struct {
	Scope             ResourceScope    `json:"scope"`
	ID                string           `json:"id"`
	Kind              OperationKind    `json:"kind"`
	RequestKey        string           `json:"requestKey"`
	TargetID          *string          `json:"targetId"`
	Status            OperationStatus  `json:"status"`
	Phase             *string          `json:"phase"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	CompletedAt       *time.Time       `json:"completedAt"`
	RetryAfterSeconds *int64           `json:"retryAfterSeconds"`
	Result            *OperationResult `json:"result"`
	Failure           *Error           `json:"failure"`
}

// OperationResult has exactly one non-null field corresponding to Kind.
// Removal returns a Worktree whose status is Removed, not an empty success.
type OperationResult struct {
	Acquisition   *Acquisition        `json:"acquisition"`
	Lease         *Lease              `json:"lease"`
	Execution     *Execution          `json:"execution"`
	AccessSession *AccessSession      `json:"accessSession"`
	Worktree      *Worktree           `json:"worktree"`
	Checkpoint    *Checkpoint         `json:"checkpoint"`
	Restore       *RestoreResult      `json:"restore"`
	Profile       *ProfileRevision    `json:"profile"`
	Pool          *WarmPool           `json:"pool"`
	FileWrite     *FileWriteResult    `json:"fileWrite"`
	Input         *InputReceipt       `json:"input"`
	Connection    *AccessConnection   `json:"connection"`
	Quota         *Quota              `json:"quota"`
	SaveRetry     *RequiredSaveResult `json:"saveRetry"`
	Discard       *DiscardReceipt     `json:"discard"`
}

type RequiredSaveKind string

const (
	RequiredSaveCheckpoint RequiredSaveKind = "Checkpoint"
	RequiredSaveArtifact   RequiredSaveKind = "Artifact"
)

// RequiredSaveRequirement is a server-frozen obligation, even if no operation
// was admitted. ID is stable across retries. IntentDigest binds the canonical
// original save parameters and coverage, not a mutable worker request.
// Checkpoint uses non-null Coverage and empty ArtifactPaths; Artifact uses
// nonempty ArtifactPaths and null Coverage. Paths identify only this save's data.
type RequiredSaveRequirement struct {
	ID            string              `json:"id"`
	Kind          RequiredSaveKind    `json:"kind"`
	IntentDigest  string              `json:"intentDigest"`
	Coverage      *CheckpointCoverage `json:"coverage"`
	ArtifactPaths []string            `json:"artifactPaths"`
}

// BlockedSaveTarget is the entire confirmation scope. RequiredSaves is nonempty,
// sorted by unique ID, and includes all frozen obligations, including gaps and
// already satisfied members. Omissions/extras cannot waive protection. Revision
// is the durable confirmation snapshot revision, not a lease or worker claim.
// Changes to data, requirements, attempts or publication/disposition facts advance
// it atomically. DataVersion identifies the frozen source data, not an old checkpoint.
type BlockedSaveTarget struct {
	Scope              ResourceScope             `json:"scope"`
	Sandbox            SandboxRef                `json:"sandbox"`
	Workspace          WorkspaceRef              `json:"workspace"`
	DataVersion        string                    `json:"dataVersion"`
	ReleaseOperationID string                    `json:"releaseOperationId"`
	Revision           int64                     `json:"revision"`
	RequiredSaves      []RequiredSaveRequirement `json:"requiredSaves"`
}

// A null OperationID/Status pair is an unadmitted gap, never success. Every
// requirement has one progress entry; OperationID identifies its latest attempt.
// Failure is non-null only for Failed. Published results remain in the ledger.
type RequiredSaveProgress struct {
	RequirementID string           `json:"requirementId"`
	OperationID   *string          `json:"operationId"`
	Status        *OperationStatus `json:"status"`
	Failure       *Error           `json:"failure"`
}

type BlockedSave struct {
	Target BlockedSaveTarget      `json:"target"`
	Saves  []RequiredSaveProgress `json:"saves"`
}

// ArtifactPublication references an existing artifact service's published version;
// the sandbox neither allocates artifact versions nor owns their deletion.
type ArtifactPublication struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// RequiredSaveResult confirms publication for the exact frozen intent. Exactly
// one of Checkpoint (Ready) or Artifact is non-null according to requirement kind.
// It cannot be produced by accepting a discard or by publishing a different scope.
type RequiredSaveResult struct {
	Target        BlockedSaveTarget    `json:"target"`
	RequirementID string               `json:"requirementId"`
	Checkpoint    *Checkpoint          `json:"checkpoint"`
	Artifact      *ArtifactPublication `json:"artifact"`
	PublishedAt   time.Time            `json:"publishedAt"`
}

// DiscardReceipt records a decision to waive only the unsatisfied requirements
// of Target. It is neither save success nor deletion success. Published checkpoints
// and unrelated artifacts are untouched. Physical deletion still requires process
// isolation and in-flight publish reconciliation/fencing; resource charges remain
// until cleanup is proved. No lease, admission or execution can be revived.
type DiscardReceipt struct {
	OperationID          string            `json:"operationId"`
	Target               BlockedSaveTarget `json:"target"`
	WaivedRequirementIDs []string          `json:"waivedRequirementIds"`
	AuthorizationRef     string            `json:"authorizationRef"`
	RecordedAt           time.Time         `json:"recordedAt"`
}

// FileVersion is opaque. Conditional writes require FileConditionalWrite and
// an atomic compare-and-replace in the Runtime; shell writes can bypass it.
type FileVersion string

type FileInfo struct {
	Path       string       `json:"path"`
	SizeBytes  int64        `json:"sizeBytes"`
	Mode       uint32       `json:"mode"`
	ModifiedAt time.Time    `json:"modifiedAt"`
	Version    *FileVersion `json:"version"`
}

type FileReadResult struct {
	File       FileInfo `json:"file"`
	Offset     int64    `json:"offset"`
	Data       []byte   `json:"data"`
	NextOffset int64    `json:"nextOffset"`
	EOF        bool     `json:"eof"`
}

type FileWriteResult struct {
	File         FileInfo `json:"file"`
	BytesWritten int64    `json:"bytesWritten"`
}

type SearchKind string

const (
	SearchNames   SearchKind = "Names"
	SearchContent SearchKind = "Content"
)

type FileMatch struct {
	Path       string `json:"path"`
	Line       *int64 `json:"line"`
	ByteOffset *int64 `json:"byteOffset"`
	Data       []byte `json:"data"`
}

type FileSearchResult struct {
	Matches       []FileMatch `json:"matches"`
	Truncated     bool        `json:"truncated"`
	NextPageToken *string     `json:"nextPageToken"`
}

type AccessKind string

const (
	AccessTerminal AccessKind = "Terminal"
	AccessBrowser  AccessKind = "Browser"
	AccessPreview  AccessKind = "Preview"
)

type AccessStatus string

const (
	AccessOpen    AccessStatus = "Open"
	AccessClosing AccessStatus = "Closing"
	AccessClosed  AccessStatus = "Closed"
	AccessExpired AccessStatus = "Expired"
	AccessFailed  AccessStatus = "Failed"
)

type AccessRole string

const (
	AccessWriter   AccessRole = "Writer"
	AccessObserver AccessRole = "Observer"
)

type AccessProtocol string

const (
	AccessWebSocket AccessProtocol = "WebSocket"
	AccessHTTPS     AccessProtocol = "HTTPS"
)

// AccessSession creation does not grant possession-based access. Every connection
// is authenticated by the gateway. GatewayURL never identifies a private Pod.
type AccessSession struct {
	Scope              ResourceScope  `json:"scope"`
	ID                 string         `json:"id"`
	RequestKey         string         `json:"requestKey"`
	Kind               AccessKind     `json:"kind"`
	Sandbox            SandboxRef     `json:"sandbox"`
	LeaseID            string         `json:"leaseId"`
	Status             AccessStatus   `json:"status"`
	Protocol           AccessProtocol `json:"protocol"`
	GatewayURL         string         `json:"gatewayUrl"`
	CreatedAt          time.Time      `json:"createdAt"`
	ExpiresAt          time.Time      `json:"expiresAt"`
	ClosedAt           *time.Time     `json:"closedAt"`
	WriterConnectionID *string        `json:"writerConnectionId"`
	PreviewPort        *int           `json:"previewPort"`
	Failure            *Error         `json:"failure"`
}

// AccessConnection is a browser/preview gateway connection descriptor. It has
// no embedded credentials; the consumer must attach its current authentication.
// Disconnecting the network connection does not close the AccessSession.
type AccessConnection struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionId"`
	Role      AccessRole     `json:"role"`
	Protocol  AccessProtocol `json:"protocol"`
	URL       string         `json:"url"`
	ExpiresAt time.Time      `json:"expiresAt"`
}

type TerminalSize struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

type TerminalSignal string

const (
	TerminalInterrupt TerminalSignal = "INT"
	TerminalTerminate TerminalSignal = "TERM"
	TerminalHangup    TerminalSignal = "HUP"
	TerminalKill      TerminalSignal = "KILL"
)

type TerminalFrameType string

const (
	TerminalOutput TerminalFrameType = "Output"
	TerminalExit   TerminalFrameType = "Exit"
)

type TerminalExitResult struct {
	ExitCode    *int               `json:"exitCode"`
	Signal      *TerminalSignal    `json:"signal"`
	Termination *TerminationResult `json:"termination"`
}

// TerminalFrame sequences are connection-local and distinct from SSE sequences.
// Output is a merged PTY byte stream, not separate stdout/stderr. Exit frames
// have non-null Exit and empty Data; output frames have null Exit.
type TerminalFrame struct {
	Type      TerminalFrameType   `json:"type"`
	Sequence  int64               `json:"sequence"`
	Timestamp time.Time           `json:"timestamp"`
	Data      []byte              `json:"data"`
	Exit      *TerminalExitResult `json:"exit"`
}

// InputReceipt confirms gateway acceptance, not application consumption.
// Sequence is connection-local for PTY and execution-local for stdin.
type InputReceipt struct {
	RequestKey    string    `json:"requestKey"`
	Sequence      int64     `json:"sequence"`
	AcceptedAt    time.Time `json:"acceptedAt"`
	AcceptedBytes int64     `json:"acceptedBytes"`
	InputClosed   bool      `json:"inputClosed"`
}

type WorktreeStatus string

const (
	WorktreeCreating WorktreeStatus = "Creating"
	WorktreeReady    WorktreeStatus = "Ready"
	WorktreeRemoving WorktreeStatus = "Removing"
	WorktreeRemoved  WorktreeStatus = "Removed"
	WorktreeFailed   WorktreeStatus = "Failed"
)

type Worktree struct {
	Scope      ResourceScope  `json:"scope"`
	ID         string         `json:"id"`
	RequestKey string         `json:"requestKey"`
	Workspace  WorkspaceRef   `json:"workspace"`
	MemberID   string         `json:"memberId"`
	BaseCommit string         `json:"baseCommit"`
	Branch     string         `json:"branch"`
	Path       string         `json:"path"`
	Status     WorktreeStatus `json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
	RemovedAt  *time.Time     `json:"removedAt"`
	Failure    *Error         `json:"failure"`
}

type CheckpointConsistency string

const (
	CheckpointApplicationConsistent CheckpointConsistency = "ApplicationConsistent"
	CheckpointCrashConsistent       CheckpointConsistency = "CrashConsistent"
)

// CheckpointCoverage states actual saved file scope using explicit path coverage.
// The center does not interpret application state or claim process restore.
type CheckpointCoverage struct {
	TrackedChanges  bool     `json:"trackedChanges"`
	UntrackedFiles  bool     `json:"untrackedFiles"`
	BinaryFiles     bool     `json:"binaryFiles"`
	FilePermissions bool     `json:"filePermissions"`
	IncludedPaths   []string `json:"includedPaths"`
	ExcludedPaths   []string `json:"excludedPaths"`
}

type CheckpointStatus string

const (
	CheckpointCreating CheckpointStatus = "Creating"
	CheckpointReady    CheckpointStatus = "Ready"
	CheckpointFailed   CheckpointStatus = "Failed"
)

type Checkpoint struct {
	Scope          ResourceScope         `json:"scope"`
	ID             string                `json:"id"`
	RequestKey     string                `json:"requestKey"`
	Workspace      WorkspaceRef          `json:"workspace"`
	BaseCommit     string                `json:"baseCommit"`
	DataVersion    string                `json:"dataVersion"`
	Status         CheckpointStatus      `json:"status"`
	Consistency    CheckpointConsistency `json:"consistency"`
	Coverage       CheckpointCoverage    `json:"coverage"`
	ManifestRef    *string               `json:"manifestRef"`
	ManifestSHA256 *string               `json:"manifestSha256"`
	StorageRef     *string               `json:"storageRef"`
	SizeBytes      *int64                `json:"sizeBytes"`
	CreatedAt      time.Time             `json:"createdAt"`
	PublishedAt    *time.Time            `json:"publishedAt"`
	Failure        *Error                `json:"failure"`
}

type RestoreMode string

const (
	RestoreInPlace         RestoreMode = "InPlace"
	RestoreIndependentCopy RestoreMode = "IndependentCopy"
)

// RestoreResult describes data restoration only. InPlace advances the target
// workspace generation; IndependentCopy creates a separate workspace identity.
// Neither replays commands, restores processes, or confirms external effects.
type RestoreResult struct {
	CheckpointID string           `json:"checkpointId"`
	Mode         RestoreMode      `json:"mode"`
	Workspace    WorkspaceBinding `json:"workspace"`
	RestoredAt   time.Time        `json:"restoredAt"`
}

type PoolSpec struct {
	ProjectID             string         `json:"projectId"`
	ProfileRef            ProfileRef     `json:"profileRef"`
	PlacementPolicy       PolicyRef      `json:"placementPolicy"`
	StaticVolumeLayoutRef string         `json:"staticVolumeLayoutRef"`
	MinReady              int64          `json:"minReady"`
	MaxReady              int64          `json:"maxReady"`
	MaxProvisioning       int64          `json:"maxProvisioning"`
	MaxIdleSeconds        int64          `json:"maxIdleSeconds"`
	Budget                ResourceLimits `json:"budget"`
}

// WarmPool counts only clean inventory. Disabling stops replenishment and drains
// unclaimed instances; it never reclaims a sandbox already assigned to a lease.
type WarmPool struct {
	Scope             ResourceScope `json:"scope"`
	ID                string        `json:"id"`
	Revision          int64         `json:"revision"`
	Enabled           bool          `json:"enabled"`
	Spec              PoolSpec      `json:"spec"`
	ReadyCount        int64         `json:"readyCount"`
	ProvisioningCount int64         `json:"provisioningCount"`
	DeletingCount     int64         `json:"deletingCount"`
	ObservedAt        *time.Time    `json:"observedAt"`
	Conditions        []Condition   `json:"conditions,omitempty"`
}

// QuotaLimits zero values prohibit allocations; zero never means unlimited.
type QuotaLimits struct {
	MaxActiveLeases         int64 `json:"maxActiveLeases"`
	MaxPendingAcquisitions  int64 `json:"maxPendingAcquisitions"`
	MaxConcurrentExecutions int64 `json:"maxConcurrentExecutions"`
	MaxAccessSessions       int64 `json:"maxAccessSessions"`
	MaxCPUMillis            int64 `json:"maxCpuMillis"`
	MaxMemoryBytes          int64 `json:"maxMemoryBytes"`
	MaxEphemeralDiskBytes   int64 `json:"maxEphemeralDiskBytes"`
	MaxWorkspaceBytes       int64 `json:"maxWorkspaceBytes"`
	MaxArchivedOutputBytes  int64 `json:"maxArchivedOutputBytes"`
}

type QuotaUsage struct {
	ActiveLeases         int64 `json:"activeLeases"`
	PendingAcquisitions  int64 `json:"pendingAcquisitions"`
	ConcurrentExecutions int64 `json:"concurrentExecutions"`
	AccessSessions       int64 `json:"accessSessions"`
	CPUMillis            int64 `json:"cpuMillis"`
	MemoryBytes          int64 `json:"memoryBytes"`
	EphemeralDiskBytes   int64 `json:"ephemeralDiskBytes"`
	WorkspaceBytes       int64 `json:"workspaceBytes"`
	ArchivedOutputBytes  int64 `json:"archivedOutputBytes"`
}

type Quota struct {
	TenantID   string      `json:"tenantId"`
	Revision   int64       `json:"revision"`
	Limits     QuotaLimits `json:"limits"`
	Usage      QuotaUsage  `json:"usage"`
	ObservedAt *time.Time  `json:"observedAt"`
}

type ErrorCode string

const (
	CodeUnauthorized           ErrorCode = "Unauthorized"
	CodeForbidden              ErrorCode = "Forbidden"
	CodeInvalidArgument        ErrorCode = "InvalidArgument"
	CodeNotFound               ErrorCode = "NotFound"
	CodeProfileNotFound        ErrorCode = "ProfileNotFound"
	CodeProfileDisabled        ErrorCode = "ProfileDisabled"
	CodeCapabilityUnsupported  ErrorCode = "CapabilityUnsupported"
	CodeIdempotencyConflict    ErrorCode = "IdempotencyConflict"
	CodeLeaseBusy              ErrorCode = "LeaseBusy"
	CodeWorkspaceBusy          ErrorCode = "WorkspaceBusy"
	CodeCapacityUnavailable    ErrorCode = "CapacityUnavailable"
	CodeQuotaExceeded          ErrorCode = "QuotaExceeded"
	CodeSandboxPreparing       ErrorCode = "SandboxPreparing"
	CodeLeaseExpired           ErrorCode = "LeaseExpired"
	CodeStaleFence             ErrorCode = "StaleFence"
	CodeGenerationMismatch     ErrorCode = "GenerationMismatch"
	CodeRevisionConflict       ErrorCode = "RevisionConflict"
	CodeFileVersionConflict    ErrorCode = "FileVersionConflict"
	CodeProvisionFailed        ErrorCode = "ProvisionFailed"
	CodeWorkspacePrepareFailed ErrorCode = "WorkspacePrepareFailed"
	CodeExecutionUnknown       ErrorCode = "ExecutionUnknown"
	CodeOutcomeUnknown         ErrorCode = "OutcomeUnknown"
	CodeOutputGap              ErrorCode = "OutputGap"
	CodeSandboxLost            ErrorCode = "SandboxLost"
	CodeRequestExpired         ErrorCode = "RequestExpired"
	CodeInvalidState           ErrorCode = "InvalidState"
	CodeWriterBusy             ErrorCode = "WriterBusy"
	CodeInputUnknown           ErrorCode = "InputUnknown"
	CodeCheckpointIncompatible ErrorCode = "CheckpointIncompatible"
	CodeCounterExhausted       ErrorCode = "CounterExhausted"
	CodeRateLimited            ErrorCode = "RateLimited"
	CodeUnavailable            ErrorCode = "Unavailable"
	CodeDeadlineExceeded       ErrorCode = "DeadlineExceeded"
	CodeProtocolError          ErrorCode = "ProtocolError"
	CodeInternal               ErrorCode = "Internal"
)

type FieldViolation struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error is the public service error (use errors.As). Retryable is advice, not
// permission for the SDK to repeat a mutation. Unknown outcomes require query
// and reconciliation. Diagnostics never contain secrets or full environments.
// HTTPStatus is transport metadata, excluded from the JSON error body.
type Error struct {
	Code              ErrorCode        `json:"code"`
	Message           string           `json:"message"`
	TraceID           string           `json:"traceId"`
	ResourceID        *string          `json:"resourceId"`
	OperationID       *string          `json:"operationId"`
	RequestKey        *string          `json:"requestKey"`
	Phase             *string          `json:"phase"`
	Reason            string           `json:"reason"`
	Retryable         bool             `json:"retryable"`
	RetryAfterSeconds *int64           `json:"retryAfterSeconds"`
	DiagnosticRefs    []string         `json:"diagnosticRefs"`
	Violations        []FieldViolation `json:"violations"`
	OutputGap         *OutputGap       `json:"outputGap"`
	HTTPStatus        int              `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	if e.Code == "" {
		return e.Message
	}
	return string(e.Code) + ": " + e.Message
}

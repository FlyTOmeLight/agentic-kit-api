package contract

import (
	"context"
	"io"
	"time"

	sandboxv1 "example.com/sandbox-design-contracts/sdk/go/sandboxv1"
)

type WorkspaceKey struct {
	TenantID    string `json:"tenantId"`
	ProjectID   string `json:"projectId"`
	WorkspaceID string `json:"workspaceId"`
	Generation  int64  `json:"generation"`
}

type WorkspaceSource struct {
	Kind         string // Git, Checkpoint, or Empty.
	SourceRef    string
	BaseCommit   string
	CheckpointID string
}

// BaseLayerRef identifies the read-only layer an EphemeralCopy workspace is
// assembled from. Digest is the content identity the platform verified when
// materializing the layer; Restore and Prepare must refuse to activate a
// workspace whose materialized base does not match this digest.
type BaseLayerRef struct {
	Kind   string // RepositorySnapshot or Checkpoint.
	Digest string
	Engine string // Verified layering engine, e.g. "overlayfs".
}

type WorkspaceSpec struct {
	Key            WorkspaceKey
	Mode           string // EphemeralCopy or PersistentVolume.
	Source         WorkspaceSource
	BaseLayer      *BaseLayerRef // Required for EphemeralCopy; nil for PersistentVolume.
	StorageRef     string
	Root           string
	ReadonlyAssets []AssetRef
	RecoveryLevel  string // Ephemeral, Checkpointed, or Persistent.
}

type AssetRef struct {
	ID        string
	Version   string
	Digest    string
	MountPath string
}

type PrepareWorkspace struct {
	AcquisitionID string
	OperationID   string
	Target        RuntimeTarget
	Spec          WorkspaceSpec
	Deadline      time.Time
}

type WorkspaceReady struct {
	Key                WorkspaceKey `json:"key"`
	Root               string       `json:"root"`
	BaseCommit         string       `json:"baseCommit"`
	BaseLayerDigest    string       `json:"baseLayerDigest"`
	MaterializedDigest string       `json:"materializedDigest"`
	RecoveryLevel      string       `json:"recoveryLevel"`
	PreparedAt         time.Time    `json:"preparedAt"`
}

type WorktreeSpec struct {
	OperationID string
	WorktreeID  string
	Workspace   WorkspaceKey
	Target      RuntimeTarget
	Proof       AdmissionProof
	MemberID    string
	BaseCommit  string
	Branch      string
	Path        string
}

type WorktreeResult struct {
	WorktreeID string
	Path       string
	Branch     string
	HeadCommit string
}

type CheckpointCoverage struct {
	TrackedFiles   bool     `json:"trackedFiles"`
	UntrackedFiles bool     `json:"untrackedFiles"`
	BinaryFiles    bool     `json:"binaryFiles"`
	Permissions    bool     `json:"permissions"`
	Symlinks       bool     `json:"symlinks"`
	Includes       []string `json:"includes"`
	Excludes       []string `json:"excludes"`
}

type QuiescenceEvidence struct {
	Workspace        WorkspaceKey `json:"workspace"`
	AdmissionEpoch   int64        `json:"admissionEpoch"`
	ProcessTreeEmpty bool         `json:"processTreeEmpty"`
	EvidenceRef      string       `json:"evidenceRef"`
	VerifiedAt       time.Time    `json:"verifiedAt"`
}

// SaveRecoveryPermit authorizes only the frozen save, independently of LeaseProof.
// The service derives it from an admitted authorized retry; adapters validate its
// operation, exact source and current publication epoch before dispatch/commit.
// It cannot authorize exec, stdin, general file writes, restore or lease renewal.
// If the backend cannot fence publication, drain/reconcile all calls before cleanup.
type SaveRecoveryPermit struct {
	OperationID        string                 `json:"operationId"`
	ReleaseOperationID string                 `json:"releaseOperationId"`
	RequirementID      string                 `json:"requirementId"`
	Sandbox            sandboxv1.SandboxRef   `json:"sandbox"`
	Workspace          sandboxv1.WorkspaceRef `json:"workspace"`
	DataVersion        string                 `json:"dataVersion"`
	PublicationEpoch   int64                  `json:"publicationEpoch"`
	ExpiresAt          time.Time              `json:"expiresAt"`
}

// CheckpointSpec executes an already-admitted caller intent, including a frozen
// unadmitted requirement recovered by BeginSaveRetry. Exactly one of Proof and
// Recovery is non-null; recovery cannot use an expired AdmissionProof. Background
// reconciliation cannot invent new scheduled checkpoints or widen saved coverage.
type CheckpointSpec struct {
	OperationID  string
	CheckpointID string
	Workspace    WorkspaceKey
	Target       RuntimeTarget
	Proof        *AdmissionProof
	Recovery     *SaveRecoveryPermit
	Coverage     CheckpointCoverage
	Consistency  string // ApplicationConsistent or CrashConsistent.
	Quiescence   *QuiescenceEvidence
}

type CheckpointManifest struct {
	CheckpointID string             `json:"checkpointId"`
	Workspace    WorkspaceKey       `json:"workspace"`
	BaseCommit   string             `json:"baseCommit"`
	BaseLayer    *BaseLayerRef      `json:"baseLayer"`
	Coverage     CheckpointCoverage `json:"coverage"`
	Consistency  string             `json:"consistency"`
	ManifestRef  string             `json:"manifestRef"`
	Digest       string             `json:"digest"`
	Bytes        int64              `json:"bytes"`
	CreatedAt    time.Time          `json:"createdAt"`
}

type RestoreSpec struct {
	OperationID string
	Manifest    CheckpointManifest
	Destination PrepareWorkspace

	// Required for replacing data on a storage domain used by an old incarnation.
	OldWriterFence *FenceEvidence

	IndependentStorage bool
}

// WorkspaceDriver runs only under a recorded acquisition/operation reservation.
// Restore cannot reuse a blocked writable volume based on lease expiry alone.
type WorkspaceDriver interface {
	Prepare(context.Context, PrepareWorkspace) (WorkspaceReady, error)
	CreateWorktree(context.Context, WorktreeSpec) (WorktreeResult, error)
	RemoveWorktree(context.Context, WorktreeSpec, bool) error
	CreateCheckpoint(context.Context, CheckpointSpec) (CheckpointManifest, error)
	RestoreCheckpoint(context.Context, RestoreSpec) (WorkspaceReady, error)
}

type ArtifactSpec struct {
	TenantID    string              `json:"tenantId"`
	ProjectID   string              `json:"projectId"`
	RequestKey  string              `json:"requestKey"`
	ExecutionID string              `json:"executionId"`
	Digest      string              `json:"digest"`
	MediaType   string              `json:"mediaType"`
	SizeBytes   int64               `json:"sizeBytes"`
	Recovery    *SaveRecoveryPermit `json:"recovery"`
}

type ArtifactRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type PublishResult struct {
	Outcome DispatchOutcome
	Ref     *ArtifactRef
}

// ArtifactPublisher uses the existing enterprise artifact service. Unknown publish
// outcomes are reconciled by request key; the sandbox does not own artifact versions.
// A recovery publish uses its new admitted operation key and exact frozen input;
// Unknown retains that key until reconciled. Recovery permits never allow deletion
// of already published versions. Expired worker claims do not fence external writes.
type ArtifactPublisher interface {
	Publish(context.Context, ArtifactSpec, io.Reader) (PublishResult, error)
	Lookup(ctx context.Context, tenantID, projectID, requestKey string) (PublishResult, error)
}

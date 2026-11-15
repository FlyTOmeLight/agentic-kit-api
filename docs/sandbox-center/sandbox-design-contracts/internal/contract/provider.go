// Package contract defines server-side ports. It contains no implementation.
package contract

import (
	"context"
	"time"
)

// InstanceKey changes whenever the physical execution environment is replaced.
type InstanceKey struct {
	InstanceID string `json:"instanceId"`
	Generation int64  `json:"generation"`
}

type PhysicalRef struct {
	Key       InstanceKey `json:"key"`
	Provider  string      `json:"provider"`
	Cluster   string      `json:"cluster"`
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	UID       string      `json:"uid"`
}

type Resources struct {
	CPUMilli              int64 `json:"cpuMilli"`
	MemoryBytes           int64 `json:"memoryBytes"`
	EphemeralStorageBytes int64 `json:"ephemeralStorageBytes"`
	PIDs                  int64 `json:"pids"`
}

type MountKind string

const (
	MountEmptyDir MountKind = "EmptyDir"
	MountPVC      MountKind = "PVC"
	MountMemory   MountKind = "Memory"
)

// VolumeBinding is fixed before Pod creation. A running Pod cannot change PVC.
type VolumeBinding struct {
	Name      string    `json:"name"`
	Kind      MountKind `json:"kind"`
	ClaimName string    `json:"claimName"`
	SubPath   string    `json:"subPath"`
	MountPath string    `json:"mountPath"`
	ReadOnly  bool      `json:"readOnly"`
	SizeBytes int64     `json:"sizeBytes"`
}

type SecuritySpec struct {
	PolicyRevision           string   `json:"policyRevision"`
	RuntimeClass             string   `json:"runtimeClass"`
	RunAsUser                int64    `json:"runAsUser"`
	RunAsGroup               int64    `json:"runAsGroup"`
	ReadOnlyRootFilesystem   bool     `json:"readOnlyRootFilesystem"`
	AutomountServiceAccount  bool     `json:"automountServiceAccount"`
	AllowPrivilegeEscalation bool     `json:"allowPrivilegeEscalation"`
	SeccompProfile           string   `json:"seccompProfile"`
	DroppedCapabilities      []string `json:"droppedCapabilities"`
	NetworkPolicyRevision    string   `json:"networkPolicyRevision"`
	EgressPolicyRef          string   `json:"egressPolicyRef"`
}

type Toleration struct {
	Key               string `json:"key"`
	Operator          string `json:"operator"`
	Value             string `json:"value"`
	Effect            string `json:"effect"`
	TolerationSeconds *int64 `json:"tolerationSeconds"`
}

type RuntimeEndpointSpec struct {
	Name     string `json:"name"`
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
}

// InstanceSpec is resolved from an immutable approved Profile, never raw client input.
// HardExpiresAt ends user execution, not preservation of unsaved files. A Provider
// must not map it to automatic destruction of the sole protected data copy.
type InstanceSpec struct {
	Key                 InstanceKey           `json:"key"`
	ProfileID           string                `json:"profileId"`
	ProfileRevision     int64                 `json:"profileRevision"`
	PoolKey             string                `json:"poolKey"`
	SpecHash            string                `json:"specHash"`
	Cluster             string                `json:"cluster"`
	Namespace           string                `json:"namespace"`
	ImageDigest         string                `json:"imageDigest"`
	RuntimeAdapter      string                `json:"runtimeAdapter"`
	RuntimeVersion      string                `json:"runtimeVersion"`
	Command             []string              `json:"command"`
	Args                []string              `json:"args"`
	NonSecretEnv        map[string]string     `json:"nonSecretEnv"`
	Requests            Resources             `json:"requests"`
	Limits              Resources             `json:"limits"`
	Volumes             []VolumeBinding       `json:"volumes"`
	Security            SecuritySpec          `json:"security"`
	NodeSelector        map[string]string     `json:"nodeSelector"`
	Tolerations         []Toleration          `json:"tolerations"`
	RuntimeEndpoints    []RuntimeEndpointSpec `json:"runtimeEndpoints"`
	ReadonlyAssetDigest string                `json:"readonlyAssetDigest"`
	ProvisionDeadline   time.Time             `json:"provisionDeadline"`
	HardExpiresAt       time.Time             `json:"hardExpiresAt"`
}

type PhysicalPhase string

const (
	PhysicalPending PhysicalPhase = "Pending"
	PhysicalRunning PhysicalPhase = "Running"
	PhysicalFailed  PhysicalPhase = "Failed"
	PhysicalGone    PhysicalPhase = "Gone"
	PhysicalUnknown PhysicalPhase = "Unknown"
)

// Endpoint contains private routing information; it must never appear in public DTOs.
type Endpoint struct {
	URL        string `json:"url"`
	AuthRef    string `json:"authRef"`
	ServerName string `json:"serverName"`
}

type Observation struct {
	Ref            PhysicalRef   `json:"ref"`
	Phase          PhysicalPhase `json:"phase"`
	Endpoint       *Endpoint     `json:"endpoint"`
	NodeName       string        `json:"nodeName"`
	ObservedAt     time.Time     `json:"observedAt"`
	ReasonCode     string        `json:"reasonCode"`
	Message        string        `json:"message"`
	ProcessStopped bool          `json:"processStopped"`
}

type DeleteResult struct {
	APIObjectGone bool
	Fence         *FenceEvidence
}

type FenceEvidence struct {
	Ref         PhysicalRef `json:"ref"`
	Method      string      `json:"method"`
	EvidenceRef string      `json:"evidenceRef"`
	VerifiedAt  time.Time   `json:"verifiedAt"`

	// True only when old processes can no longer write the bound storage.
	WritesStopped bool `json:"writesStopped"`
}

// Ensure uses a deterministic name and verifies ownership/spec hash on AlreadyExists.
// Delete is UID-conditional. An absent API object is not proof of node/process fencing.
// Controllers must establish the required-save deletion guard before calling Delete;
// expiry, GC or a Failed operation never waive that guard. Provider APIs are private.
type Provider interface {
	Ensure(context.Context, InstanceSpec) (Observation, error)
	Observe(context.Context, PhysicalRef) (Observation, error)
	Delete(context.Context, PhysicalRef) (DeleteResult, error)
}

// PhysicalFencer is optional. No support means the same writable volume stays blocked.
type PhysicalFencer interface {
	Fence(context.Context, PhysicalRef) (FenceEvidence, error)
}

type InventoryPage struct {
	Items      []Observation
	NextCursor string
}

// Inventory lists only platform-owned resources, including resources orphaned by crashes.
type Inventory interface {
	ListOwned(ctx context.Context, clusterID, cursor string, limit int) (InventoryPage, error)
}

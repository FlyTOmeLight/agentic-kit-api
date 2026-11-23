// Package memory holds the pure domain values shared by the Memory business
// service, the context builder, and the public SDK. It does not import
// durable, HTTP, database or artifact types.
package memory

import "encoding/json"

// ScopeKind names the isolation level of a memory entry.
type ScopeKind string

const (
	ScopeOwnerPrivate  ScopeKind = "OwnerPrivate"
	ScopeActorPrivate  ScopeKind = "ActorPrivate"
	ScopeActorProject  ScopeKind = "ActorProject"
	ScopeProjectShared ScopeKind = "ProjectShared"
)

// Scope is the exact identity and isolation key of a Memory entry.
type Scope struct {
	Kind    ScopeKind `json:"kind"`
	Owner   string    `json:"owner"`
	Tenant  string    `json:"tenant,omitempty"`
	Project string    `json:"project,omitempty"`
	Actor   string    `json:"actor,omitempty"`
}

// Kind names the semantic type of a memory entry.
type Kind string

const (
	KindFact       Kind = "Fact"
	KindPreference Kind = "Preference"
	KindProcedure  Kind = "Procedure"
)

// OutputVisibility constrains where the entry content may appear.
type OutputVisibility string

const (
	OutputPrivate OutputVisibility = "Private"
	OutputProject OutputVisibility = "Project"
)

type OutputAudience string

const (
	AudiencePrivate OutputAudience = "Private"
	AudienceProject OutputAudience = "Project"
)

// SourceClass names how the content was confirmed.
type SourceClass string

const (
	SourceHumanConfirmed SourceClass = "HumanConfirmed"
)

// SourceEvidence references an authorized run/message/step/event.
type SourceEvidence struct {
	RunID     string `json:"runID,omitempty"`
	MessageID string `json:"messageID,omitempty"`
	StepID    string `json:"stepID,omitempty"`
	EventID   string `json:"eventID,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// Source records who confirmed this content and under what evidence.
type Source struct {
	Class    SourceClass      `json:"class"`
	Actor    string           `json:"actor"`
	Evidence []SourceEvidence `json:"evidence,omitempty"`
}

// Conditions constrains when the entry applies.
type Conditions struct {
	TaskTags  []string `json:"taskTags,omitempty"`
	ToolNames []string `json:"toolNames,omitempty"`
}

// Document is the complete typed payload of one memory version.
type Document struct {
	SchemaVersion    int              `json:"schemaVersion"`
	EntryID          string           `json:"entryID"`
	Scope            Scope            `json:"scope"`
	AgentID          string           `json:"agentID,omitempty"`
	Kind             Kind             `json:"kind"`
	SubjectKey       string           `json:"subjectKey,omitempty"`
	Summary          string           `json:"summary"`
	Body             json.RawMessage  `json:"body"`
	Conditions       Conditions       `json:"conditions,omitempty"`
	OutputVisibility OutputVisibility `json:"outputVisibility"`
	Source           Source           `json:"source"`
	ValidUntil       *int64           `json:"validUntil,omitempty"`
}

// VersionRef is a fixed reference to one archived version.
type VersionRef struct {
	StoreID    string `json:"storeID"`
	TenantID   string `json:"tenantID"`
	ProjectID  string `json:"projectID,omitempty"`
	UserID     string `json:"userID,omitempty"`
	ArtifactID string `json:"artifactID"`
	VersionID  string `json:"versionID"`
	Sequence   int64  `json:"sequence"`
	VersionURI string `json:"versionURI"`
}

// ArtifactBinding is the fixed storage ownership of one entry, resolved at
// creation time and reused by every later publish, retry and read.
type ArtifactBinding struct {
	StoreID    string `json:"storeID"`
	TenantID   string `json:"tenantID"`
	ProjectID  string `json:"projectID,omitempty"`
	UserID     string `json:"userID"`
	ArtifactID string `json:"artifactID"`
}

// VersionSummary identifies an archived version adopted by a memory entry.
type VersionSummary struct {
	VersionRef      VersionRef `json:"versionRef"`
	AppliedRevision int64      `json:"appliedRevision"`
	Actor           string     `json:"actor"`
	AppliedAt       int64      `json:"appliedAt"`
	Active          bool       `json:"active"`
}

// VersionPage contains adopted versions and the entry's current state.
type VersionPage struct {
	Items             []VersionSummary `json:"items"`
	NextAfterRevision int64            `json:"nextAfterRevision,omitempty"`
	ActiveVersionRef  *VersionRef      `json:"activeVersionRef,omitempty"`
	RetiredAt         *int64           `json:"retiredAt,omitempty"`
}

// Version contains an adopted version and its archived document.
type Version struct {
	VersionSummary
	Document         Document    `json:"document"`
	ActiveVersionRef *VersionRef `json:"activeVersionRef,omitempty"`
	RetiredAt        *int64      `json:"retiredAt,omitempty"`
}

// Entry is the local projection of the currently active memory content.
type Entry struct {
	ID               string           `json:"id"`
	Scope            Scope            `json:"scope"`
	AgentID          string           `json:"agentID,omitempty"`
	Kind             Kind             `json:"kind"`
	SubjectKey       string           `json:"subjectKey,omitempty"`
	Summary          string           `json:"summary"`
	Body             json.RawMessage  `json:"body"`
	Conditions       Conditions       `json:"conditions,omitempty"`
	OutputVisibility OutputVisibility `json:"outputVisibility"`
	Source           Source           `json:"source"`
	ValidUntil       *int64           `json:"validUntil,omitempty"`
	Binding          *ArtifactBinding `json:"binding,omitempty"`
	ActiveVersionRef *VersionRef      `json:"activeVersionRef,omitempty"`
	Revision         int64            `json:"revision"`
	RetiredAt        *int64           `json:"retiredAt,omitempty"`
}

// Selection records whether a run asked for memory recall.
type Selection string

const (
	SelectionOff    Selection = "Off"
	SelectionRecall Selection = "Recall"
)

// RecallFilter narrows one explicit recall request. Terms must all match
// the entry text; Tags must all appear in Conditions.TaskTags. MaxEntries
// and MaxBytes bound the frozen material; zero falls back to defaults,
// negative disables the corresponding bound.
type RecallFilter struct {
	Terms      []string `json:"terms,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	MaxEntries int      `json:"maxEntries,omitempty"`
	MaxBytes   int      `json:"maxBytes,omitempty"`
}

// OperationType names the intent of a memory write.
type OperationType string

const (
	OperationCreate  OperationType = "Create"
	OperationUpdate  OperationType = "Update"
	OperationRestore OperationType = "Restore"
	OperationRetire  OperationType = "Retire"
)

// OperationStatus is the terminal state of a write receipt.
type OperationStatus string

const (
	OperationPending  OperationStatus = "Pending"
	OperationApplied  OperationStatus = "Applied"
	OperationRejected OperationStatus = "Rejected"
)

// Operation is a durable receipt for one memory write intent.
type Operation struct {
	ID               string           `json:"id"`
	TenantID         string           `json:"tenantID"`
	OwnerID          string           `json:"ownerID"`
	RequestKey       string           `json:"requestKey"`
	IntentDigest     string           `json:"intentDigest"`
	Type             OperationType    `json:"type"`
	EntryID          string           `json:"entryID"`
	ExpectedRevision int64            `json:"expectedRevision"`
	ActorID          string           `json:"actorID"`
	Document         json.RawMessage  `json:"document,omitempty"`
	Binding          *ArtifactBinding `json:"binding,omitempty"`
	VersionRef       *VersionRef      `json:"versionRef,omitempty"`
	Status           OperationStatus  `json:"status"`
	AppliedRevision  *int64           `json:"appliedRevision,omitempty"`
	AppliedAt        *int64           `json:"appliedAt,omitempty"`
	ErrorReason      string           `json:"errorReason,omitempty"`
}

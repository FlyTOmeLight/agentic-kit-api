package contract

import (
	"context"
	"io"
	"time"
)

type Capability string

const (
	CapExec                 Capability = "Exec"
	CapAsyncExec            Capability = "AsyncExec"
	CapIncrementalOutput    Capability = "IncrementalOutput"
	CapCancelProcessTree    Capability = "CancelProcessTree"
	CapFileRead             Capability = "FileRead"
	CapFileWrite            Capability = "FileWrite"
	CapFileSearch           Capability = "FileSearch"
	CapFileConditionalWrite Capability = "FileConditionalWrite"
	CapStdin                Capability = "Stdin"
	CapPTY                  Capability = "Pty"
	CapBrowser              Capability = "Browser"
	CapPreview              Capability = "Preview"
	CapWorkspaceCheckpoint  Capability = "WorkspaceCheckpoint"
	CapProcessCheckpoint    Capability = "ProcessCheckpoint"
)

// RuntimeTarget pins every operation to one physical incarnation and boot.
type RuntimeTarget struct {
	Physical PhysicalRef
	Endpoint Endpoint
	BootID   string
}

type RuntimeInfo struct {
	ProtocolVersion        string       `json:"protocolVersion"`
	BootID                 string       `json:"bootId"`
	Capabilities           []Capability `json:"capabilities"`
	DeduplicatesStartID    bool         `json:"deduplicatesStartId"`
	CanLookupClientStartID bool         `json:"canLookupClientStartId"`
	EnforcesAdmissionEpoch bool         `json:"enforcesAdmissionEpoch"`

	// Proven local expiry enforcement even while the control plane is unreachable.
	// It stops user processes/input without deleting protected workspace data.
	EnforcesLeaseDeadline bool `json:"enforcesLeaseDeadline"`

	OutputRetention time.Duration `json:"outputRetentionNanos"`
}

type AdmissionProof struct {
	LeaseID        string    `json:"leaseId"`
	FencingToken   int64     `json:"fencingToken"`
	AdmissionEpoch int64     `json:"admissionEpoch"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type EnvironmentValue struct {
	Name      string  `json:"name"`
	Value     *string `json:"value"`
	SecretRef *string `json:"secretRef"`
}

type CommandSpec struct {
	ExecutionID    string             `json:"executionId"`
	Proof          AdmissionProof     `json:"proof"`
	Mode           string             `json:"mode"` // Argv or Shell; Shell must be explicit.
	Argv           []string           `json:"argv"`
	Script         string             `json:"script"`
	Shell          string             `json:"shell"`
	CWD            string             `json:"cwd"`
	Env            []EnvironmentValue `json:"env"`
	Timeout        time.Duration      `json:"timeoutNanos"`
	MaxOutputBytes int64              `json:"maxOutputBytes"`
	StdinEnabled   bool               `json:"stdinEnabled"`
}

type BackendExecutionRef struct {
	ExecutionID string
	BackendID   string
	BootID      string
}

type DispatchOutcome string

const (
	DispatchAccepted    DispatchOutcome = "Accepted"
	DispatchNotAccepted DispatchOutcome = "NotAccepted"
	DispatchUnknown     DispatchOutcome = "Unknown"
)

type StartResult struct {
	Outcome DispatchOutcome
	Ref     *BackendExecutionRef
}

type ExecutionObservation struct {
	Ref        BackendExecutionRef
	State      string // Pending, Running, Succeeded, Failed, Canceled, Lost, Unknown.
	ExitCode   *int
	StartedAt  *time.Time
	FinishedAt *time.Time
	ObservedAt time.Time
	ReasonCode string
}

type OutputChunk struct {
	Stream string // stdout or stderr
	Data   []byte
}

type OutputPage struct {
	Chunks       []OutputChunk
	NextCursor   string
	EOF          bool
	Truncated    bool
	Gap          bool
	DroppedBytes *int64
}

type CancelResult struct {
	Requested          bool
	ProcessTreeStopped bool
	ObservedAt         time.Time
}

type CancelSpec struct {
	OperationID string
	Signal      string
	GracePeriod time.Duration
	Deadline    time.Time
}

// A zero StartResult plus an error MUST be treated as Unknown unless the adapter
// explicitly reports NotAccepted. Context cancellation is not process cancellation.
type Runtime interface {
	Probe(context.Context, PhysicalRef, Endpoint) (RuntimeInfo, error)
	Start(context.Context, RuntimeTarget, CommandSpec) (StartResult, error)
	Inspect(context.Context, RuntimeTarget, BackendExecutionRef) (ExecutionObservation, error)
	Output(
		ctx context.Context,
		target RuntimeTarget,
		execution BackendExecutionRef,
		cursor string,
		maxBytes int64,
	) (OutputPage, error)
	Cancel(context.Context, RuntimeTarget, BackendExecutionRef, CancelSpec) (CancelResult, error)
	ReadFile(context.Context, RuntimeTarget, FileReadSpec) (FileContent, error)
	WriteFile(context.Context, RuntimeTarget, FileWriteSpec) (FileVersion, error)
	SearchFiles(context.Context, RuntimeTarget, FileSearchSpec) (FileSearchPage, error)
}

type FileReadSpec struct {
	Path     string
	Offset   int64
	MaxBytes int64
}

type FileContent struct {
	Info       RuntimeFileInfo
	Data       []byte
	Version    string
	NextOffset int64
	EOF        bool
}

type FileWriteSpec struct {
	OperationID     string
	Proof           AdmissionProof
	Path            string
	Data            []byte
	ExpectedVersion *string
	CreateOnly      bool
	Mode            uint32
}

type FileVersion struct {
	Info    RuntimeFileInfo
	Version string
	Bytes   int64
}

type RuntimeFileInfo struct {
	Path       string
	SizeBytes  int64
	Mode       uint32
	ModifiedAt time.Time
	Version    *string
}

type FileSearchSpec struct {
	Root          string
	Pattern       string
	Regex         bool
	Kind          string // Names or Content.
	CaseSensitive bool
	IncludeGlobs  []string
	ExcludeGlobs  []string
	Cursor        string
	MaxMatches    int64
	MaxBytes      int64
}

type FileMatch struct {
	Path       string
	Line       *int64
	ByteOffset *int64
	Data       []byte
}

type FileSearchPage struct {
	Matches    []FileMatch
	NextCursor string
	Truncated  bool
}

type LookupResult struct {
	Found         bool
	Authoritative bool
	Execution     *ExecutionObservation
}

type StartLookup interface {
	LookupStart(context.Context, RuntimeTarget, string) (LookupResult, error)
}

type AdmissionBarrier struct {
	NewEpoch         int64     `json:"newEpoch"`
	Closed           bool      `json:"closed"`
	InFlightStarts   int64     `json:"inFlightStarts"`
	ProcessTreeEmpty bool      `json:"processTreeEmpty"`
	ObservedAt       time.Time `json:"observedAt"`
}

// This port may be implemented by the Runtime or a protected supervisor. Worker
// Profiles require verified local deadline enforcement; a database timer is not
// sufficient. BindLease rejects expired grants and cannot revive an expired
// binding. An active renewal may only extend the same lease/fence/epoch, within
// the verified upstream deadline. Uncertain renewal is reconciled, not assumed.
// At expiry, reject new writes/input and stop existing user processes while
// preserving protected files and restricted save access. CloseAdmission rejects
// delayed mutations with an older epoch, including file writes and input, not
// just Start. Existing in-flight effects still require drain/fencing evidence.
type RuntimeAdmission interface {
	BindLease(context.Context, RuntimeTarget, AdmissionProof) error
	CloseAdmission(context.Context, RuntimeTarget, int64) (AdmissionBarrier, error)
}

type StdinWrite struct {
	OperationID string
	Proof       AdmissionProof
	Sequence    int64
	Data        []byte
	EOF         bool
}

type StdinResult struct {
	Outcome       DispatchOutcome
	AcceptedBytes int64
	InputClosed   bool
}

type StdinRuntime interface {
	WriteStdin(context.Context, RuntimeTarget, BackendExecutionRef, StdinWrite) (StdinResult, error)
}

type TerminalSpec struct {
	SessionID string
	Proof     AdmissionProof
	Argv      []string
	CWD       string
	Env       []EnvironmentValue
	Columns   uint16
	Rows      uint16
	ExpiresAt time.Time
}

type TerminalSize struct {
	Columns uint16
	Rows    uint16
}

type TerminalExit struct {
	ExitCode               *int
	Signal                 *string
	TerminationConfirmed   bool
	TerminationGranularity string
	ConfirmedAt            *time.Time
}

type TerminalFrame struct {
	Kind       string // Output or Exit.
	Data       []byte
	Exit       *TerminalExit
	ObservedAt time.Time
}

// Recv emits an Exit frame before clean remote EOF. An unexpected disconnect
// without exit evidence returns an error. Local Close releases only the stream.
type TerminalConnection interface {
	Recv(context.Context) (TerminalFrame, error)
	Write(context.Context, []byte) error
	Resize(context.Context, TerminalSize) error
	Signal(context.Context, string) error
	io.Closer
}

type PTYRuntime interface {
	OpenTerminal(context.Context, RuntimeTarget, TerminalSpec) (string, error)
	ConnectTerminal(context.Context, RuntimeTarget, string) (TerminalConnection, error)
	CloseTerminal(context.Context, RuntimeTarget, string) (CancelResult, error)
}

type AccessSpec struct {
	SessionID  string
	Proof      AdmissionProof
	Kind       string // Browser or Preview.
	Port       *int32
	Path       string
	InitialURL *string
	ExpiresAt  time.Time
}

type BackendAccess struct {
	BackendID string
	Endpoint  Endpoint
}

type BrowserRuntime interface {
	OpenBrowser(context.Context, RuntimeTarget, AccessSpec) (BackendAccess, error)
	CloseBrowser(context.Context, RuntimeTarget, string) error
}

type PreviewRuntime interface {
	OpenPreview(context.Context, RuntimeTarget, AccessSpec) (BackendAccess, error)
	ClosePreview(context.Context, RuntimeTarget, string) error
}

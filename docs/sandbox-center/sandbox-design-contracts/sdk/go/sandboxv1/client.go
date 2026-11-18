package sandboxv1

import (
	"context"
	"net/http"
	"time"
)

// Client is the complete public control-plane interface. Implementations must be
// safe for concurrent calls; mutable request values remain caller-owned and must
// not be modified or retained after the call. This package provides no
// constructor or HTTP implementation.
//
// Authentication supplies tenant/caller identity for EVERY call, including
// reads, events, archives, and browser/preview access. Resource IDs, project IDs,
// holder IDs and TenantScope never establish authority. Reads neither renew a
// lease nor require possession of write credentials. New instance writes require
// valid LeaseProof, current generation/fence, holder authorization and admission.
// BlockedSaveClient has separate save/disposition authorization, without a lease
// proof; it never renews a lease or reopens execution admission.
// Authorized exact-key replay may return an already-accepted result after lease
// expiry; it must not perform new effects. Administrative actions use explicit
// TenantScope and admin grants instead of a user lease. Pre-Ready acquisition
// cancellation is authorized against the acquisition owner, since no lease exists.
// Worker authentication must carry verified Run-attempt authority. Acquire binds
// holderRunEpoch; subsequent writes and renewals must match it and current Run
// ownership. A new epoch may read old results and request authorized cleanup,
// but cannot write/renew the old lease. There is no live lease-transfer API.
//
// Request context controls this call, not remote task lifetime. Timeouts after a
// mutation may have unknown outcomes. Do not automatically retry any mutation,
// including on 401/token refresh, redirects, connection loss, or Retry-After.
// An HTTP implementation must also prevent transport-level mutation replays.
// Reconcile via ID/request-key lookup; never repeat stdin or PTY frames on an
// uncertain acknowledgment. Explicit same-key calls obey server idempotency.
//
// All routes below are relative to BaseURL. Path IDs are escaped as one segment.
// Path-bound fields are removed from the JSON body; remaining fields use the
// lower-camel JSON names. GET fields are query parameters; structured query
// references use dotted keys (for example profileRef.id/profileRef.revision),
// arrays use repeated keys, and nil/empty optional filters are omitted. Request
// structs are also usable as complete JSON descriptions outside HTTP routing.
// DELETE uses the JSON action body (requestKey, proof, reason as applicable).
// Errors use the Error object as the response body. HTTP 202 means accepted;
// command success, cleanup and data publication must be checked independently.
//
// Acquisition and execution have their own async state resources. Other deferred actions
// return Operation (202) and are observed through OperationClient.
// Synchronous mutations still have an action record: GetOperation can inspect
// that record by Kind/TargetID/RequestKey after response loss, without replaying
// input or side effects. Its Result contains the same typed successful response.
type Client interface {
	ProfileClient
	AcquisitionClient
	SandboxClient
	LeaseClient
	ExecutionClient
	FileClient
	AccessClient
	WorktreeClient
	CheckpointClient
	BlockedSaveClient
	OperationClient
	AdministrationClient
}

// ClientOptions is local transport configuration, never an API wire payload.
// HTTPClient=nil means a future implementation supplies bounded defaults. The
// supplied client is caller-owned; streams must not inherit a short whole-body
// timeout. RequestTimeout bounds unary requests/stream handshakes, not the remote
// execution or a successfully established stream. Context deadlines take priority.
// BaseURL must use HTTPS except explicitly configured local development URLs.
// TokenSource must provide tokens for BaseURL's intended audience; redirects must
// never forward credentials to another origin. These fields imply no constructor
// or HTTP implementation exists in this package.
type ClientOptions struct {
	BaseURL        string        `json:"-"`
	HTTPClient     *http.Client  `json:"-"`
	TokenSource    TokenSource   `json:"-"`
	RequestTimeout time.Duration `json:"-"`
	UserAgent      string        `json:"-"`
}

// Token values must never be logged, encoded in URLs, or put into domain objects.
type Token struct {
	Value     string     `json:"-"`
	ExpiresAt *time.Time `json:"-"`
}

// TokenSource implementations must support concurrent calls and honor ctx.
type TokenSource interface {
	Token(ctx context.Context) (Token, error)
}

type ProfileClient interface {
	// GET /v1/sandbox-profiles
	ListProfiles(ctx context.Context, request ListProfilesRequest) (*Page[ProfileRevision], error)

	// GET /v1/sandbox-profiles/{ref.id}/revisions/{ref.revision}
	GetProfile(ctx context.Context, ref ProfileRef) (*ProfileRevision, error)
}

type AcquisitionClient interface {
	// POST /v1/sandbox-acquisitions; 200 Ready, otherwise 202 accepted.
	// Selection enforces fixed Profile isolation and controlled-task eligibility
	// for Standard; MinimumIsolation cannot downgrade it or trigger a fallback.
	Acquire(ctx context.Context, request AcquireRequest) (*Acquisition, error)

	// GET /v1/sandbox-acquisitions/{acquisitionId}
	// Lookup form: GET /v1/sandbox-acquisitions?requestKey=...
	// Lookup returns one Acquisition, not a collection.
	GetAcquisition(ctx context.Context, request GetAcquisitionRequest) (*Acquisition, error)

	// DELETE /v1/sandbox-acquisitions/{acquisitionId}; 202 Operation.
	CancelAcquisition(ctx context.Context, request CancelAcquisitionRequest) (*Operation, error)

	// No new route: polls GetAcquisition until Ready/Failed/Cancelled/TimedOut.
	// Ready returns nil error; remote failures return the snapshot and its Error.
	// Cancelled returns CodeInvalidState. Context errors leave allocation intact.
	WaitAcquisition(ctx context.Context, request WaitAcquisitionRequest) (*Acquisition, error)
}

type SandboxClient interface {
	// GET /v1/sandboxes/{sandboxId}
	GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)

	// GET /v1/sandboxes/{sandboxId}/events; Accept: text/event-stream.
	// cursor maps to Last-Event-ID (exclusive); nil means no header.
	SandboxEvents(ctx context.Context, sandboxID string, cursor EventCursor) (EventStream, error)

	// REST representation: GET /v1/sandboxes/{sandboxId}/events;
	// Accept: application/json. Optional REST polling alternative to SSE.
	ListSandboxEvents(ctx context.Context, sandboxID string, request ListEventsRequest) (*EventPage, error)
}

type LeaseClient interface {
	// GET /v1/sandbox-leases/{leaseId}
	GetLease(ctx context.Context, leaseID string) (*Lease, error)

	// POST /v1/sandbox-leases/{leaseId}/renew
	// Cannot revive an expired lease or extend past HardExpiresAt.
	RenewLease(ctx context.Context, request RenewLeaseRequest) (*Lease, error)

	// POST /v1/sandbox-leases/{leaseId}/release; 202 Operation.
	ReleaseLease(ctx context.Context, request ReleaseLeaseRequest) (*Operation, error)
}

type ExecutionClient interface {
	// POST /v1/sandboxes/{sandboxId}/executions; 202 Execution.
	StartExecution(ctx context.Context, request StartExecutionRequest) (*Execution, error)

	// GET /v1/executions/{executionId}
	// Lookup form: GET /v1/executions?leaseId=...&requestKey=...
	GetExecution(ctx context.Context, request GetExecutionRequest) (*Execution, error)

	// GET /v1/executions/{executionId}/events; Accept: text/event-stream.
	ExecutionEvents(ctx context.Context, executionID string, cursor EventCursor) (EventStream, error)

	// REST representation: GET /v1/executions/{executionId}/events;
	// Accept: application/json. It has the same ordering/gap rules as SSE.
	ListExecutionEvents(ctx context.Context, executionID string, request ListEventsRequest) (*EventPage, error)

	// GET /v1/executions/{executionId}/output
	// afterSequence/maxBytes/archiveId are queries; archives remain authorized.
	ReadExecutionOutput(ctx context.Context, request ReadExecutionOutputRequest) (*ExecutionOutput, error)

	// POST /v1/executions/{executionId}/stdin
	WriteStdin(ctx context.Context, request WriteStdinRequest) (*InputReceipt, error)

	// POST /v1/executions/{executionId}/cancel; 202 Operation.
	CancelExecution(ctx context.Context, request CancelExecutionRequest) (*Operation, error)
}

type FileClient interface {
	// POST /v1/sandboxes/{sandbox.id}/files/read (read-only).
	ReadFile(ctx context.Context, request ReadFileRequest) (*FileReadResult, error)

	// POST /v1/sandboxes/{sandboxId}/files/write
	WriteFile(ctx context.Context, request WriteFileRequest) (*FileWriteResult, error)

	// POST /v1/sandboxes/{sandbox.id}/files/search (read-only).
	SearchFiles(ctx context.Context, request SearchFilesRequest) (*FileSearchResult, error)
}

type AccessClient interface {
	// POST /v1/sandboxes/{sandboxId}/terminals; 201 AccessSession.
	// Requires Pty; success means the session is open, not just queued.
	CreateTerminal(ctx context.Context, request CreateTerminalRequest) (*AccessSession, error)

	// POST /v1/sandboxes/{sandboxId}/browsers; 201 AccessSession.
	CreateBrowser(ctx context.Context, request CreateBrowserRequest) (*AccessSession, error)

	// POST /v1/sandboxes/{sandboxId}/previews; 201 AccessSession.
	CreatePreview(ctx context.Context, request CreatePreviewRequest) (*AccessSession, error)

	// GET /v1/access-sessions/{sessionId}
	GetAccessSession(ctx context.Context, sessionID string) (*AccessSession, error)

	// POST /v1/access-sessions/{sessionId}/terminal-connections creates
	// an authenticated grant, followed by GET /v1/access-sessions/{sessionId}/
	// terminal-connections/{connectionId}/stream (WebSocket upgrade).
	// Only Terminal sessions are valid. Handshake failure does not replay the
	// POST; the grant expires and GetAccessSession can inspect writer ownership.
	ConnectTerminal(ctx context.Context, request ConnectAccessRequest) (TerminalConnection, error)

	// POST /v1/access-sessions/{sessionId}/connections
	// Browser/Preview only; returns a gateway descriptor for an external browser
	// or protocol consumer. It does not launch a browser on the caller's machine.
	// Gateway validates session kind and authorization on each connection/request.
	ConnectAccess(ctx context.Context, request ConnectAccessRequest) (*AccessConnection, error)

	// DELETE /v1/access-sessions/{sessionId}; 202 Operation.
	// Closes all associated connections and confirms session cleanup.
	CloseAccessSession(ctx context.Context, request CloseAccessSessionRequest) (*Operation, error)
}

type WorktreeClient interface {
	// POST /v1/workspaces/{workspace.id}/worktrees; 202 Operation.
	CreateWorktree(ctx context.Context, request CreateWorktreeRequest) (*Operation, error)

	// GET /v1/workspaces/{workspaceId}/worktrees/{worktreeId}
	GetWorktree(ctx context.Context, workspaceID, worktreeID string) (*Worktree, error)

	// GET /v1/workspaces/{workspace.id}/worktrees
	ListWorktrees(ctx context.Context, request ListWorktreesRequest) (*Page[Worktree], error)

	// DELETE /v1/workspaces/{workspace.id}/worktrees/{worktreeId};
	// 202 Operation. No branch deletion or implicit dirty-data discard.
	RemoveWorktree(ctx context.Context, request RemoveWorktreeRequest) (*Operation, error)
}

type CheckpointClient interface {
	// POST /v1/workspaces/{workspace.id}/checkpoints; 202 Operation.
	CreateCheckpoint(ctx context.Context, request CreateCheckpointRequest) (*Operation, error)

	// GET /v1/workspaces/{workspaceId}/checkpoints/{checkpointId}
	GetCheckpoint(ctx context.Context, workspaceID, checkpointID string) (*Checkpoint, error)

	// GET /v1/workspaces/{workspaceId}/checkpoints
	ListCheckpoints(ctx context.Context, request ListCheckpointsRequest) (*Page[Checkpoint], error)

	// POST /v1/workspaces/{target.id}/restores; 202 Operation.
	// checkpointId identifies the authorized source in the body.
	RestoreCheckpoint(ctx context.Context, request RestoreCheckpointRequest) (*Operation, error)
}

// BlockedSaveClient supports recovery and an owner's explicit business-UI
// confirmation after required-save failure. It grants no execution capability.
type BlockedSaveClient interface {
	// GET /v1/sandboxes/{sandbox.id}/blocked-save?generation=...
	// Returns a consistent current snapshot, including unadmitted save gaps.
	// GenerationMismatch rejects an old incarnation; InvalidState means no block.
	GetBlockedSave(ctx context.Context, sandbox SandboxRef) (*BlockedSave, error)

	// POST /v1/sandboxes/{target.sandbox.id}/blocked-save/retry; 202 Operation.
	// An authorized worker may retry one frozen requirement, without owner consent.
	// Completion means confirmed publication, not merely retry admission.
	RetryRequiredSave(ctx context.Context, request RetryRequiredSaveRequest) (*Operation, error)

	// POST /v1/sandboxes/{target.sandbox.id}/blocked-save/discard; 200 DiscardReceipt.
	// Requires trusted proof of owner confirmation or scoped cross-owner authority.
	// The same receipt is queryable as OperationResult.Discard after response loss.
	DiscardBlockedSave(ctx context.Context, request DiscardBlockedSaveRequest) (*DiscardReceipt, error)
}

type OperationClient interface {
	// GET /v1/sandbox-operations/{id}
	// Lookup form: GET /v1/sandbox-operations?kind=...&targetId=...&
	// requestKey=... (and profileRevision for DisableProfile).
	GetOperation(ctx context.Context, lookup OperationLookup) (*Operation, error)
}

// AdministrationClient requires explicit authorization over scope.TenantID for
// every method, including reads. No wildcard tenant or global unaudited admin
// default exists. Ordinary clients cannot override tenant identity in a body.
type AdministrationClient interface {
	// POST /v1/admin/tenants/{tenantId}/sandbox-profiles/{profileId}/revisions
	// 201 immutable revision; publication validates the Profile combination.
	// Default isolation is Hardened; Standard requires platform-verified
	// controlled-task scope, enforced again when selecting the Profile.
	PublishProfile(ctx context.Context, scope TenantScope, request PublishProfileRequest) (*ProfileRevision, error)

	// POST /v1/admin/tenants/{tenantId}/sandbox-profiles/{profileRef.id}/
	// revisions/{profileRef.revision}/disable; 202 Operation.
	DisableProfile(ctx context.Context, scope TenantScope, request DisableProfileRequest) (*Operation, error)

	// GET /v1/admin/tenants/{tenantId}/sandbox-pools/{poolId}
	GetPool(ctx context.Context, scope TenantScope, poolID string) (*WarmPool, error)

	// GET /v1/admin/tenants/{tenantId}/sandbox-pools
	ListPools(ctx context.Context, scope TenantScope, request ListPoolsRequest) (*Page[WarmPool], error)

	// PUT /v1/admin/tenants/{tenantId}/sandbox-pools/{poolId}
	// Returns desired config; non-null ObservedAt does not imply convergence
	// to the latest revision. Conditions supply optional diagnostic evidence.
	PutPool(ctx context.Context, scope TenantScope, request PutPoolRequest) (*WarmPool, error)

	// POST /v1/admin/tenants/{tenantId}/sandbox-pools/{poolId}/disable;
	// 202 Operation, completed only after unclaimed inventory is drained.
	DisablePool(ctx context.Context, scope TenantScope, request DisablePoolRequest) (*Operation, error)

	// GET /v1/admin/tenants/{tenantId}/sandbox-quota
	GetQuota(ctx context.Context, scope TenantScope) (*Quota, error)

	// PUT /v1/admin/tenants/{tenantId}/sandbox-quota
	PutQuota(ctx context.Context, scope TenantScope, request PutQuotaRequest) (*Quota, error)

	// GET /v1/admin/tenants/{tenantId}/sandbox-operations/{id}
	// Lookup: GET /v1/admin/tenants/{tenantId}/sandbox-operations with
	// the same lookup query as GetOperation. Required for cross-tenant admins.
	GetTenantOperation(ctx context.Context, scope TenantScope, lookup OperationLookup) (*Operation, error)
}

// EventStream is a read-only ordered SSE stream, never a PTY connection.
// Exactly one goroutine calls Recv at a time. Close may run concurrently, is
// idempotent, releases local resources and unblocks Recv. It does not cancel
// execution, release the lease, or close an access session. After local Close,
// Recv returns io.EOF. The stream owns its response body and must close it.
//
// The opening context governs stream lifetime; Recv's context may additionally
// bound that wait. Cancellation returns the applicable context error and ends
// this stream. io.EOF without a final resource event is merely disconnection,
// not proof of command success. A clean final log drains then returns io.EOF.
// Service errors return *Error; transport errors remain errors. Output gaps
// return the gap Event together with *Error{Code: CodeOutputGap}, then close.
// The client must process a non-null event even when err is non-nil.
//
// No automatic reconnect is promised. Callers may explicitly reconnect using
// the last successfully processed Sequence; discard duplicate sequences and
// report gaps. SSE retry/Retry-After hints are advisory bounded delays, not
// instructions to retry writes. Heartbeats are not domain Events. REST event
// polling provides the same log/cursor/gap/closed semantics and is an optional
// client consumption mode, independent of Runtime protocol capabilities.
type EventStream interface {
	Recv(ctx context.Context) (*Event, error)
	Close() error
}

// TerminalConnection is a bidirectional PTY connection. Only one Recv goroutine
// and one sending goroutine may be active; the sender serializes SendInput,
// Resize and Signal together. Receive and send may run concurrently. The server
// permits at most one Writer across ALL connections to the terminal; Observer
// connections return Forbidden on every send method. A Go mutex alone cannot
// enforce server-side ownership. Lease/fence and writer authorization are checked
// throughout the connection and on every write, not only at handshake.
//
// Connection returns immutable connection metadata. Close is local, idempotent,
// concurrency-safe and unblocks pending operations; it relinquishes the writer
// connection but does not request remote terminal termination. The session may
// survive disconnect until the Profile retention/lease deadline. Explicit remote
// shutdown uses CloseAccessSession. The opening context bounds the connection;
// per-call contexts bound that operation, and cancellation terminates this
// connection when frame delivery is uncertain. Failed sends return InputUnknown
// when acceptance cannot be proved, never silently replay input/resize/signal.
//
// Recv returns io.EOF after an Exit frame is drained or local Close. Unexpected
// disconnection before Exit is an error, not successful process termination.
// Query GetAccessSession after disconnect. There is no automatic reconnection,
// PTY replay, or conversion of terminal frames into SSE output events.
type TerminalConnection interface {
	Connection() AccessConnection
	Recv(ctx context.Context) (*TerminalFrame, error)
	SendInput(ctx context.Context, request TerminalInputRequest) (*InputReceipt, error)
	Resize(ctx context.Context, request TerminalResizeRequest) (*InputReceipt, error)
	Signal(ctx context.Context, request TerminalSignalRequest) (*InputReceipt, error)
	Close() error
}

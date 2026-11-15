package contract

import (
	"context"
	"time"

	sandboxv1 "example.com/sandbox-design-contracts/sdk/go/sandboxv1"
)

type Principal struct {
	TenantID  string `json:"tenantId"`
	ProjectID string `json:"projectId"`

	// Stable authenticated application identity across worker/epoch replacements;
	// required for original-caller request-key lookup without reusing old tokens.
	CallerID string `json:"callerId"`

	SubjectID string   `json:"subjectId"`
	Roles     []string `json:"roles"`

	// Set only from a verified worker identity or attempt-scoped delegation.
	// Plain Run/Epoch headers and possession of LeaseProof are not evidence.
	RunAttempt *RunAttempt `json:"runAttempt,omitempty"`
}

// RunAttempt identifies an upstream Agent worker's ownership, not a sandbox
// controller claim, instance generation, fencing token or admission epoch.
type RunAttempt struct {
	RunID    string `json:"runId"`
	WorkerID string `json:"workerId"`
	Epoch    int64  `json:"epoch"`
}

// RunAuthorization is server-produced after checking the authoritative Run
// owner, Running state, worker ID, epoch and lease deadline. It is not a token
// decoded from a request body or a reusable cache of successful authorization.
type RunAuthorization struct {
	Principal  Principal
	Attempt    RunAttempt
	Action     string
	Resource   ResourceScope
	CheckedAt  time.Time
	ValidUntil time.Time // Never later than the upstream execution lease.
}

type AuthInput struct {
	BearerToken  string
	PeerIdentity string
}

type ResourceScope struct {
	TenantID  string
	ProjectID string
	Kind      string
	ID        string
	Revision  int64 // Zero for unversioned targets; positive for ProfileRevision.
}

type PolicyDecision struct {
	Allowed        bool
	PolicyRevision string
	ReasonCode     string
	ValidUntil     time.Time
}

type Authorizer interface {
	Authenticate(context.Context, AuthInput) (Principal, error)
	Authorize(context.Context, Principal, string, ResourceScope) (PolicyDecision, error)

	// Requires Principal.RunAttempt and verifies its exact current ownership with
	// the trusted Agent platform. Caller-supplied epoch alone is insufficient.
	// Unavailable/expired authority fails closed for new effects and renewals.
	// Read reconciliation and separately authorized cleanup/save recovery use
	// resource authorization instead, so terminal Runs can still be cleaned up.
	AuthorizeRun(context.Context, Principal, string, ResourceScope) (RunAuthorization, error)

	// Verifies the business ownership/confirmation source, never body assertions.
	// Retry permits a scoped recovery worker; Discard requires explicit owner
	// confirmation (direct or verified delegation) or a scoped cross-owner admin
	// decision. Admins use the SAME exact target and confirmation checks, not force.
	// A generic execution/worker grant is insufficient. Return a durable auditable
	// EvidenceRef binding principal, owner, kind, target, confirmation and validity.
	// Failure to verify any binding fails closed; no new approval system is implied.
	AuthorizeSaveRecovery(
		context.Context,
		Principal,
		SaveRecoveryAuthorizationRequest,
	) (SaveRecoveryAuthorization, error)
}

// Kind is RetryRequiredSave or DiscardBlockedSave. RequirementID is set only
// for Retry; ConfirmationRef is set only for Discard. Target.Scope is checked
// against authenticated authority, never used to override caller tenant identity.
type SaveRecoveryAuthorizationRequest struct {
	Kind            sandboxv1.OperationKind     `json:"kind"`
	Target          sandboxv1.BlockedSaveTarget `json:"target"`
	RequirementID   *string                     `json:"requirementId"`
	ConfirmationRef *string                     `json:"confirmationRef"`
}

// SaveRecoveryAuthorization is server-produced, not decoded from an HTTP body.
// EvidenceRef resolves a trusted, immutable authorization record. OwnerSubjectID
// comes from business ownership, not holder/caller IDs. Discard also requires
// ConfirmedAt and a verified confirmation bound to this decision's exact target.
// The transaction validates the proof's issuer/integrity, principal, scope,
// action and validity; a nonempty string alone is not authorization evidence.
type SaveRecoveryAuthorization struct {
	Request        SaveRecoveryAuthorizationRequest `json:"request"`
	Principal      Principal                        `json:"principal"`
	OwnerSubjectID string                           `json:"ownerSubjectId"`
	EvidenceRef    string                           `json:"evidenceRef"`
	PolicyRevision string                           `json:"policyRevision"`
	ConfirmedAt    *time.Time                       `json:"confirmedAt"`
	ValidUntil     time.Time                        `json:"validUntil"`
}

type ProfileRef struct {
	ID       string
	Revision int64
}

type ProfileSelection struct {
	Principal            Principal
	Profile              ProfileRef
	RequiredCapabilities []Capability
	MinimumIsolation     string
	Workspace            WorkspaceSpec
}

// Policy is the immutable published revision, including all lifecycle bounds.
// Derived durations below must agree with it. Resolve/Reserve reject request
// policy assertions that differ; later operations use the bound revision.
type ResolvedProfile struct {
	Ref                  ProfileRef
	Policy               sandboxv1.ProfileSpec
	Instance             InstanceSpec
	Capabilities         []Capability
	PreparationSteps     []PreparationStep
	LeaseDuration        time.Duration
	MaxLifetime          time.Duration
	IdleRetention        time.Duration
	CredentialPolicyRef  string
	AccessPolicyRevision string
	ConfigDigest         string
}

type PreparationStep struct {
	Name    string
	Argv    []string
	CWD     string
	Timeout time.Duration
}

type ProfileResolver interface {
	Resolve(context.Context, ProfileSelection) (ResolvedProfile, error)
}

type CredentialRequest struct {
	RequestKey    string      `json:"requestKey"`
	Principal     Principal   `json:"principal"`
	AcquisitionID string      `json:"acquisitionId"`
	LeaseID       string      `json:"leaseId"`
	Target        PhysicalRef `json:"target"`
	PolicyRef     string      `json:"policyRef"`
	Audience      string      `json:"audience"`
	Scopes        []string    `json:"scopes"`
	ExpiresAt     time.Time   `json:"expiresAt"`
}

// CredentialGrant exposes a broker reference only. Secret values are never rows/events.
type CredentialGrant struct {
	GrantID   string    `json:"grantId"`
	SecretRef string    `json:"secretRef"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type CredentialBroker interface {
	Issue(context.Context, CredentialRequest) (CredentialGrant, error)
	Revoke(context.Context, string) error
}

type NetworkBinding struct {
	OperationID    string
	Target         PhysicalRef
	TenantID       string
	PolicyRevision string
	ProxyIdentity  string
}

type NetworkEnforcer interface {
	Bind(context.Context, NetworkBinding) error
	Revoke(context.Context, NetworkBinding) error
}

type AccessTicketRequest struct {
	Principal Principal
	SessionID string
	Proof     AdmissionProof
	Audience  string
	ExpiresAt time.Time
}

type AccessTicket struct {
	Token     string
	ExpiresAt time.Time
}

// Ticket issuance also verifies the current session/lease. Tokens are single-use,
// short-lived, and audience-bound; connection routes do not expose a Pod endpoint.
type AccessTickets interface {
	Issue(context.Context, AccessTicketRequest) (AccessTicket, error)
	Consume(context.Context, string, string) (AccessTicketRequest, error)
}

type PolicyConfig struct {
	DefaultLeaseDuration time.Duration
	RenewInterval        time.Duration
	MaxLifetime          time.Duration
	IdleRetention        time.Duration
	MaxFileReadBytes     int64
	MaxFileWriteBytes    int64
	MaxOutputPageBytes   int64
	MaxSearchMatches     int
	EventRetention       time.Duration
	IdempotencyRetention time.Duration
	AccessTicketLifetime time.Duration
}

type ReconcileConfig struct {
	ScanInterval      time.Duration
	ClaimTTL          time.Duration
	CallTimeout       time.Duration
	BatchSize         int
	MaxConcurrent     int
	RetryMin          time.Duration
	RetryMax          time.Duration
	OrphanGracePeriod time.Duration
}

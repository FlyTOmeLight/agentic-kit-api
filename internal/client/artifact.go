// Package client holds the outbound adapters for trusted peer services.
// Only this package references the artifact SDK; domain values, durable
// state and the public API never expose third-party types.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	artifactv1 "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const (
	memoryArtifactType  = "agent-memory"
	memoryVersionScheme = artifactv1.VersionSchemeInteger
)

// ArtifactConfig configures the Memory archive adapter.
type ArtifactConfig struct {
	// Endpoint is the base URL of the artifact service. Empty disables the
	// adapter; memory writes stay local without a remote archive.
	Endpoint string

	// StoreID identifies this artifact deployment inside stored bindings and
	// version references. It defaults to "artifact".
	StoreID string

	// TenantID is the storage tenant resolved for OwnerPrivate entries whose
	// scope carries no business tenant. Entries of other scopes use their
	// verified business tenant.
	TenantID string

	BearerToken string

	Timeout time.Duration

	// MaxPayloadBytes bounds the immutable document payload; it must satisfy
	// both the memory limit and the deployed artifact MaxPayloadBytes.
	MaxPayloadBytes int64
}

// MemoryArchive implements the memory archive port over artifact-go.
type MemoryArchive struct {
	sdk *artifactv1.Client
	cfg ArtifactConfig
}

// NewMemoryArchive builds the adapter. The caller owns Close.
func NewMemoryArchive(cfg ArtifactConfig) (*MemoryArchive, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("artifact endpoint is required")
	}
	if cfg.StoreID == "" {
		cfg.StoreID = "artifact"
	}
	if cfg.MaxPayloadBytes <= 0 {
		return nil, errors.New("artifact MaxPayloadBytes is required")
	}
	sdkClient, err := artifactv1.NewClient(artifactv1.ClientConfig{
		Endpoint: cfg.Endpoint,
		Timeout:  cfg.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &MemoryArchive{sdk: sdkClient, cfg: cfg}, nil
}

// Close releases the underlying transport.
func (s *MemoryArchive) Close() error { return s.sdk.Close() }

// ResolveBinding fixes the storage ownership of a new entry. OwnerPrivate
// entries without a business tenant use the configured mapping; a missing
// mapping rejects creation instead of falling back to a shared scope.
func (s *MemoryArchive) ResolveBinding(scope memory.Scope, actor string) (*memory.ArtifactBinding, error) {
	binding := &memory.ArtifactBinding{StoreID: s.cfg.StoreID}
	switch scope.Kind {
	case memory.ScopeOwnerPrivate:
		binding.TenantID = scope.Tenant
		binding.ProjectID = scope.Project
		if binding.TenantID == "" {
			if scope.Project != "" {
				return nil, fmt.Errorf(
					"%w: OwnerPrivate with a project requires a tenant",
					memory.ErrInvalid,
				)
			}
			binding.TenantID = s.cfg.TenantID
		}
		binding.UserID = scope.Owner
	case memory.ScopeActorPrivate:
		if scope.Project != "" {
			return nil, fmt.Errorf("%w: ActorPrivate cannot carry a project", memory.ErrInvalid)
		}
		binding.TenantID = scope.Tenant
		binding.UserID = actor
	case memory.ScopeActorProject:
		binding.TenantID = scope.Tenant
		binding.ProjectID = scope.Project
		binding.UserID = actor
		if binding.ProjectID == "" {
			return nil, fmt.Errorf("%w: ActorProject requires a project", memory.ErrInvalid)
		}
	case memory.ScopeProjectShared:
		binding.TenantID = scope.Tenant
		binding.ProjectID = scope.Project
		binding.UserID = actor
		if binding.ProjectID == "" {
			return nil, fmt.Errorf("%w: ProjectShared requires a project", memory.ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: unsupported scope kind %q", memory.ErrInvalid, scope.Kind)
	}
	if binding.TenantID == "" || binding.UserID == "" {
		return nil, fmt.Errorf("%w: no storage ownership mapping for %s", memory.ErrInvalid, scope.Kind)
	}
	return binding, nil
}

// EnsureCollection creates or confirms the entry collection with the stable
// request key derived from the entry. The same key with the same
// parameters returns the original artifact; a different key with the same
// name never adopts an existing collection.
func (s *MemoryArchive) EnsureCollection(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	entryID, requestKey string,
) (string, error) {
	if err := validateBinding(binding, s.cfg.StoreID); err != nil {
		return "", err
	}
	if entryID == "" || requestKey == "" {
		return "", fmt.Errorf("%w: entry ID and collection request key are required", memory.ErrInvalid)
	}
	resp, err := s.sdk.CreateArtifact(
		artifactv1.WithRequestIdentity(ctx, s.identityFor(binding, entryID)),
		&artifactv1.CreateArtifactRequest{
			RequestKey:    requestKey,
			Type:          memoryArtifactType,
			Name:          "memory-" + entryID,
			VersionScheme: memoryVersionScheme,
		},
	)
	if err != nil {
		return "", mapArtifactError(err, binding)
	}
	if resp == nil || resp.ID == "" {
		return "", fmt.Errorf("%w: artifact service returned no collection identity", memory.ErrUnavailable)
	}
	if binding.ArtifactID != "" && resp.ID != binding.ArtifactID {
		return "", fmt.Errorf("%w: collection key resolved to a different artifact", memory.ErrConflict)
	}
	return resp.ID, nil
}

// PublishVersion archives one immutable document as a protected version.
// The operation ID is the stable publish key; retries must reuse it.
func (s *MemoryArchive) PublishVersion(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	entryID, requestKey string,
	doc *memory.Document,
) (*memory.VersionRef, error) {
	if err := validateBinding(binding, s.cfg.StoreID); err != nil {
		return nil, err
	}
	if binding.ArtifactID == "" {
		return nil, fmt.Errorf("%w: artifact ID is required to publish a version", memory.ErrInvalid)
	}
	if entryID == "" || requestKey == "" {
		return nil, fmt.Errorf("%w: entry ID and publish request key are required", memory.ErrInvalid)
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: memory document is required", memory.ErrInvalid)
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: encode memory document: %v", memory.ErrInvalid, err)
	}
	if int64(len(payload)) > s.cfg.MaxPayloadBytes {
		return nil, fmt.Errorf(
			"%w: document payload exceeds %d bytes",
			memory.ErrInvalid,
			s.cfg.MaxPayloadBytes,
		)
	}
	resp, err := s.sdk.CreateVersion(
		artifactv1.WithRequestIdentity(ctx, s.identityFor(binding, entryID)),
		&artifactv1.CreateVersionRequest{
			ArtifactPath: artifactv1.ArtifactPath{ArtifactID: binding.ArtifactID},
			RequestKey:   requestKey,
			Payload:      payload,
			Protected:    true,
		},
	)
	if err != nil {
		return nil, mapArtifactError(err, binding)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: artifact service returned no version identity", memory.ErrUnavailable)
	}
	if resp.ArtifactID != binding.ArtifactID {
		return nil, fmt.Errorf("%w: publish key resolved to a different artifact", memory.ErrConflict)
	}
	if resp.ID == "" || resp.VersionURI == "" || resp.Sequence <= 0 {
		return nil, fmt.Errorf(
			"%w: artifact service returned an incomplete version identity",
			memory.ErrUnavailable,
		)
	}
	if resp.State != artifactv1.VersionReady || !resp.Protected || !equalPayload(payload, resp.Payload) {
		return nil, fmt.Errorf(
			"%w: artifact service returned an unready, unprotected or mismatched document",
			memory.ErrUnavailable,
		)
	}
	return &memory.VersionRef{
		StoreID:    binding.StoreID,
		TenantID:   binding.TenantID,
		ProjectID:  binding.ProjectID,
		UserID:     binding.UserID,
		ArtifactID: resp.ArtifactID,
		VersionID:  resp.ID,
		Sequence:   resp.Sequence,
		VersionURI: resp.VersionURI,
	}, nil
}

func equalPayload(expected, actual []byte) bool {
	var expectedValue, actualValue any
	expectedDecoder := json.NewDecoder(bytes.NewReader(expected))
	expectedDecoder.UseNumber()
	actualDecoder := json.NewDecoder(bytes.NewReader(actual))
	actualDecoder.UseNumber()
	if expectedDecoder.Decode(&expectedValue) != nil || actualDecoder.Decode(&actualValue) != nil {
		return false
	}
	return reflect.DeepEqual(expectedValue, actualValue)
}

func validateBinding(binding *memory.ArtifactBinding, storeID string) error {
	if binding == nil {
		return fmt.Errorf("%w: artifact binding is required", memory.ErrInvalid)
	}
	if binding.StoreID == "" || binding.TenantID == "" || binding.UserID == "" {
		return fmt.Errorf("%w: artifact binding must include store, tenant and user", memory.ErrInvalid)
	}
	if binding.StoreID != storeID {
		return fmt.Errorf("%w: artifact binding belongs to a different store", memory.ErrUnavailable)
	}
	return nil
}

const artifactType = "agent"

// Identity is the caller's verified request identity.
type Identity struct {
	TenantID         string
	ProjectID        *string
	PropagatedUserID string
	RequestID        string
}

// VersionRef is the fixed identity of a published version.
type VersionRef struct {
	ID  string
	URI string
}

// ArtifactStore stores published Agent configuration as immutable JSON.
type ArtifactStore interface {
	// EnsureCollection idempotently creates the Agent's collection.
	EnsureCollection(ctx context.Context, identity Identity, agentID string) (string, error)

	// PublishVersion publishes with a stable request key; replays return the
	// original version.
	PublishVersion(
		ctx context.Context,
		identity Identity,
		artifactID, requestKey string,
		payload []byte,
	) (VersionRef, error)

	// GetPayload reads a ready version's body by fixed identity.
	GetPayload(ctx context.Context, identity Identity, artifactID, versionID string) ([]byte, error)

	// Close releases the underlying transport; called by the assembly layer.
	Close() error
}

type artifactClient struct {
	sdk           *artifactv1.Client
	serviceAPIKey string
}

// NewArtifact constructs the adapter. The caller closes it after requests stop.
func NewArtifact(endpoint, serviceAPIKey string, timeout time.Duration) (ArtifactStore, error) {
	if strings.TrimSpace(serviceAPIKey) == "" {
		return nil, errors.New("artifact service API key is required")
	}
	sdk, err := artifactv1.NewClient(artifactv1.ClientConfig{Endpoint: endpoint, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return &artifactClient{sdk: sdk, serviceAPIKey: serviceAPIKey}, nil
}

func (c *artifactClient) Close() error { return c.sdk.Close() }

func (c *artifactClient) EnsureCollection(
	ctx context.Context,
	identity Identity,
	agentID string,
) (string, error) {
	if err := validateIdentity(identity, agentID); err != nil {
		return "", err
	}
	created, err := c.sdk.CreateArtifact(c.withIdentity(ctx, identity), &artifactv1.CreateArtifactRequest{
		RequestKey:    collectionKey(agentID),
		Type:          artifactType,
		Name:          agentID,
		VersionScheme: artifactv1.VersionSchemeInteger,
	})
	if err != nil {
		return "", mapError(ctx, err)
	}
	return created.ID, nil
}

func (c *artifactClient) PublishVersion(
	ctx context.Context,
	identity Identity,
	artifactID, requestKey string,
	payload []byte,
) (VersionRef, error) {
	if err := validateIdentity(identity, artifactID); err != nil {
		return VersionRef{}, err
	}
	if requestKey == "" {
		return VersionRef{}, agent.ErrInvalid
	}
	// Published Agent versions back reproducible runs, so retention must not
	// evict them.
	published, err := c.sdk.CreateVersion(c.withIdentity(ctx, identity), &artifactv1.CreateVersionRequest{
		ArtifactPath: artifactv1.ArtifactPath{ArtifactID: artifactID},
		RequestKey:   requestKey,
		Payload:      payload,
		Protected:    true,
	})
	if err != nil {
		return VersionRef{}, mapError(ctx, err)
	}
	return VersionRef{ID: published.ID, URI: published.VersionURI}, nil
}

func (c *artifactClient) GetPayload(
	ctx context.Context,
	identity Identity,
	artifactID, versionID string,
) ([]byte, error) {
	if err := validateIdentity(identity, artifactID); err != nil {
		return nil, err
	}
	if versionID == "" {
		return nil, agent.ErrInvalid
	}
	version, err := c.sdk.GetVersion(c.withIdentity(ctx, identity), &artifactv1.GetVersionRequest{
		VersionPath: artifactv1.VersionPath{
			ArtifactPath: artifactv1.ArtifactPath{ArtifactID: artifactID},
			VersionID:    versionID,
		},
	})
	if err != nil {
		return nil, mapError(ctx, err)
	}
	return version.Payload, nil
}

func validateIdentity(identity Identity, agentID string) error {
	if agentID == "" {
		return agent.ErrInvalid
	}
	if strings.TrimSpace(identity.TenantID) == "" || identity.TenantID != strings.TrimSpace(identity.TenantID) {
		return fmt.Errorf("%w: verified tenant is required", agent.ErrUnavailable)
	}
	if strings.TrimSpace(identity.PropagatedUserID) == "" ||
		identity.PropagatedUserID != strings.TrimSpace(identity.PropagatedUserID) {
		return fmt.Errorf("%w: verified user is required", agent.ErrUnavailable)
	}
	return nil
}

func (s *MemoryArchive) identityFor(binding *memory.ArtifactBinding, requestID string) artifactv1.RequestIdentity {
	if binding == nil {
		return artifactv1.RequestIdentity{RequestID: requestID, BearerToken: s.cfg.BearerToken}
	}
	identity := artifactv1.RequestIdentity{
		TenantID:    binding.TenantID,
		RequestID:   requestID,
		BearerToken: s.cfg.BearerToken,
	}
	if binding.ProjectID != "" {
		project := binding.ProjectID
		identity.ProjectID = &project
	}
	if binding.UserID != "" {
		user := binding.UserID
		identity.ResourceUserID = &user
	}
	return identity
}

// mapArtifactError preserves retry semantics: transport failures, unknown
// outcomes and dependency errors stay ErrUnavailable so the operation keeps
// Pending; conflicts and validation errors map to stable terminal errors.
func mapArtifactError(err error, binding *memory.ArtifactBinding) error {
	var deleted *artifactv1.VersionDeletedError
	if errors.As(err, &deleted) {
		if binding == nil {
			return fmt.Errorf("%w: version tombstone has no binding", memory.ErrUnavailable)
		}
		if deleted.Version.ArtifactID != binding.ArtifactID {
			return fmt.Errorf("%w: version tombstone resolved to a different artifact", memory.ErrConflict)
		}
		return &memory.VersionUnavailableError{
			Ref: memory.VersionRef{
				StoreID:    binding.StoreID,
				TenantID:   binding.TenantID,
				ProjectID:  binding.ProjectID,
				UserID:     binding.UserID,
				ArtifactID: binding.ArtifactID,
				VersionID:  deleted.Version.ID,
				Sequence:   deleted.Version.Sequence,
				VersionURI: deleted.Version.VersionURI,
			},
			Version: deleted.Version.Version,
			Cause:   err,
		}
	}
	var apiErr *serrors.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %v", memory.ErrUnavailable, err)
	}
	if apiErr.Status == http.StatusConflict {
		switch apiErr.Code {
		case artifactv1.CodeRequestInProgress, "RequestAttemptExpired":
			return fmt.Errorf("%w: %s", memory.ErrUnavailable, apiErr.Code)
		}
		return fmt.Errorf("%w: %s", memory.ErrConflict, apiErr.Code)
	}
	return fmt.Errorf("%w: %s", memory.ErrUnavailable, apiErr.Code)
}

func (c *artifactClient) withIdentity(ctx context.Context, identity Identity) context.Context {
	return artifactv1.WithRequestIdentity(ctx, artifactv1.RequestIdentity{
		BearerToken:      c.serviceAPIKey,
		TenantID:         identity.TenantID,
		ProjectID:        identity.ProjectID,
		PropagatedUserID: identity.PropagatedUserID,
		ResourceUserID:   &identity.PropagatedUserID,
		RequestID:        identity.RequestID,
	})
}

// collectionKey is stable across retries because it derives from persisted
// identity only.
func collectionKey(agentID string) string {
	return digest(agentID + ":collection")
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mapError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var serviceError *serrors.Error
	if !errors.As(err, &serviceError) {
		return fmt.Errorf("%w: %w", agent.ErrUnavailable, err)
	}
	switch serviceError.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", agent.ErrInvalid, err)
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%w: %w", agent.ErrNotFound, err)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", agent.ErrConflict, err)
	default:
		// Authentication, scope and dependency failures are deployment
		// conditions rather than caller errors.
		return fmt.Errorf("%w: %w", agent.ErrUnavailable, err)
	}
}

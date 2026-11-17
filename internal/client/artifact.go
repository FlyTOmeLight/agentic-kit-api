// Package client holds the outbound adapters for trusted peer services.
// Only this package references the artifact SDK; domain values, durable
// state and the public API never expose third-party types.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	artifactv1 "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	serrors "github.com/FlyTOmeLight/server-go/errors"

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

// ArtifactStore implements the memory archive port over artifact-go.
type ArtifactStore struct {
	sdk *artifactv1.Client
	cfg ArtifactConfig
}

// NewArtifactStore builds the adapter. The caller owns Close.
func NewArtifactStore(cfg ArtifactConfig) (*ArtifactStore, error) {
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
	return &ArtifactStore{sdk: sdkClient, cfg: cfg}, nil
}

// Close releases the underlying transport.
func (s *ArtifactStore) Close() error { return s.sdk.Close() }

// ResolveBinding fixes the storage ownership of a new entry. OwnerPrivate
// entries without a business tenant use the configured mapping; a missing
// mapping rejects creation instead of falling back to a shared scope.
func (s *ArtifactStore) ResolveBinding(scope memory.Scope, actor string) (*memory.ArtifactBinding, error) {
	binding := &memory.ArtifactBinding{StoreID: s.cfg.StoreID}
	switch scope.Kind {
	case memory.ScopeOwnerPrivate:
		if scope.Project != "" {
			return nil, fmt.Errorf("%w: OwnerPrivate cannot carry a project", memory.ErrInvalid)
		}
		binding.TenantID = scope.Tenant
		if binding.TenantID == "" {
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
func (s *ArtifactStore) EnsureCollection(
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
func (s *ArtifactStore) PublishVersion(
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

func (s *ArtifactStore) identityFor(binding *memory.ArtifactBinding, requestID string) artifactv1.RequestIdentity {
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

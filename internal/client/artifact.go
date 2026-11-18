// Package client adapts external services to the small interfaces used by the
// application. SDK types stay inside this package.
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

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
	var serviceError *frameworkerrors.Error
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

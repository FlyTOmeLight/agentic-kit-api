package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	artifactv1 "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// ReadVersion reads one archived document by its fixed version reference.
// The reference supplies the complete storage identity; the URI is only
// compared with the response and is never resolved or fetched directly.
func (s *ArtifactStore) ReadVersion(ctx context.Context, ref memory.VersionRef) (*memory.Document, error) {
	binding := &memory.ArtifactBinding{
		StoreID:    ref.StoreID,
		TenantID:   ref.TenantID,
		ProjectID:  ref.ProjectID,
		UserID:     ref.UserID,
		ArtifactID: ref.ArtifactID,
	}
	if err := validateBinding(binding, s.cfg.StoreID); err != nil {
		return nil, err
	}
	if ref.ArtifactID == "" || ref.VersionID == "" || ref.Sequence <= 0 || ref.VersionURI == "" {
		return nil, fmt.Errorf("%w: fixed version reference is incomplete", memory.ErrInvalid)
	}
	resp, err := s.sdk.GetVersion(
		artifactv1.WithRequestIdentity(ctx, s.identityFor(binding, ref.VersionID)),
		&artifactv1.GetVersionRequest{
			VersionPath: artifactv1.VersionPath{
				ArtifactPath: artifactv1.ArtifactPath{ArtifactID: ref.ArtifactID},
				VersionID:    ref.VersionID,
			},
		},
	)
	if err != nil {
		return nil, mapReadArtifactError(err, ref)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: artifact service returned no version", memory.ErrUnavailable)
	}
	if resp.ID != ref.VersionID || resp.ArtifactID != ref.ArtifactID ||
		resp.Sequence != ref.Sequence || resp.VersionURI != ref.VersionURI {
		return nil, fmt.Errorf(
			"%w: artifact service returned a different version identity",
			memory.ErrUnavailable,
		)
	}
	if resp.State != artifactv1.VersionReady {
		return nil, &memory.VersionUnavailableError{Ref: ref}
	}
	payload := bytes.TrimSpace(resp.Payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return nil, &memory.VersionUnavailableError{Ref: ref}
	}
	if int64(len(payload)) > s.cfg.MaxPayloadBytes {
		return nil, fmt.Errorf(
			"%w: version payload exceeds %d bytes",
			memory.ErrUnavailable,
			s.cfg.MaxPayloadBytes,
		)
	}
	var doc memory.Document
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("%w: decode version payload: %v", memory.ErrUnavailable, err)
	}
	return &doc, nil
}

// mapReadArtifactError distinguishes a confirmed missing or deleted version
// from an indeterminate dependency failure. A tombstone only confirms the
// requested reference when its complete identity matches.
func mapReadArtifactError(err error, ref memory.VersionRef) error {
	var deleted *artifactv1.VersionDeletedError
	if errors.As(err, &deleted) {
		identity := deleted.Version
		if identity.ID != ref.VersionID || identity.ArtifactID != ref.ArtifactID ||
			identity.Sequence != ref.Sequence || identity.VersionURI != ref.VersionURI {
			return fmt.Errorf(
				"%w: version tombstone resolved to a different version",
				memory.ErrUnavailable,
			)
		}
		return &memory.VersionUnavailableError{Ref: ref, Version: identity.Version, Cause: err}
	}
	var apiErr *serrors.Error
	if errors.As(err, &apiErr) &&
		(apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusGone) {
		return &memory.VersionUnavailableError{Ref: ref, Cause: err}
	}
	return fmt.Errorf("%w: %v", memory.ErrUnavailable, err)
}

package execution

import (
	"context"
	"fmt"
	"mime"
	"path"
	"slices"
	"strings"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func hasArtifactAttachments(attachments []execpkg.Attachment) bool {
	return slices.ContainsFunc(attachments, func(a execpkg.Attachment) bool {
		return a.ArtifactURI != "" || strings.HasPrefix(a.URI, "artifact://")
	})
}

func matchesArtifactIdentity(identity client.Identity, owner string, scope access.Scope) bool {
	project := ""
	if identity.ProjectID != nil {
		project = *identity.ProjectID
	}
	return identity.PropagatedUserID == owner && identity.TenantID == scope.TenantID && project == scope.ProjectID
}

func normalizeResolvedMediaType(raw string) (string, bool) {
	parsed, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	parsed = strings.ToLower(strings.TrimSpace(parsed))
	if parsed == "" || !strings.Contains(parsed, "/") || strings.Contains(parsed, "*") {
		return "", false
	}
	return parsed, true
}

func (s *Service) resolveAttachments(
	ctx context.Context,
	identity client.Identity,
	input []execpkg.Attachment,
) ([]execpkg.Attachment, error) {
	attachments := slices.Clone(input)
	for i := range attachments {
		a := &attachments[i]
		if a.ArtifactURI == "" {
			continue
		}
		if s.artifact == nil {
			return nil, agent.ErrUnavailable
		}
		resolved, err := s.artifact.ResolveAttachment(ctx, identity, a.ArtifactURI)
		if err != nil {
			return nil, err
		}
		mediaType, ok := normalizeResolvedMediaType(resolved.MediaType)
		if resolved.FileURI == "" || resolved.FileID == "" || !ok ||
			(a.MediaType != "" && a.MediaType != mediaType) {
			return nil, fmt.Errorf("%w: attachment metadata does not match resolved file", agent.ErrInvalid)
		}
		if a.URI != "" && !strings.HasPrefix(a.URI, "artifact://") {
			return nil, fmt.Errorf(
				"%w: attachment URI cannot be bound to an artifact file",
				agent.ErrInvalid,
			)
		}
		if (a.SizeBytes != 0 && a.SizeBytes != resolved.SizeBytes) ||
			(a.ID != "" && a.ID != resolved.FileID) {
			return nil, fmt.Errorf(
				"%w: attachment metadata does not match resolved file",
				agent.ErrInvalid,
			)
		}
		a.URI = resolved.FileURI
		a.ArtifactURI = resolved.FileURI
		a.SizeBytes = resolved.SizeBytes
		a.ID = resolved.FileID
		a.MediaType = mediaType
		if a.Name == "" {
			name := ""
			if trimmed := strings.TrimSpace(resolved.Path); trimmed != "" {
				if base := path.Base(trimmed); base != "." && base != "/" {
					name = base
				}
			}
			if name == "" {
				name = resolved.FileID
			}
			a.Name = name
		}
	}
	if err := execpkg.ValidateAttachments(attachments); err != nil {
		return nil, err
	}
	return attachments, nil
}

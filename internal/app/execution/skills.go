package execution

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/sync/errgroup"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const (
	skillEntryPath          = "SKILL.md"
	maxSkillFilesPerPackage = 64
	maxTotalRunSkills       = 32
	maxSkillFileBytes       = 128 << 10
	maxConcurrentSkillFetch = 4
)

func (s *Service) authorizeSkillAccess(
	ctx context.Context,
	identity client.Identity,
	owner string,
	alreadyAuthorized bool,
) (context.Context, error) {
	if alreadyAuthorized {
		return ctx, nil
	}
	scope, verified := access.FromContext(ctx)
	if !verified || scope.TenantID == "" || owner == "" || !matchesArtifactIdentity(identity, owner, scope) {
		return ctx, memory.ErrForbidden
	}
	return s.authorizeProjectRecall(ctx, owner, scope)
}

func (s *Service) resolveSkills(
	ctx context.Context,
	identity client.Identity,
	refs []agent.SkillRef,
) ([]execpkg.SkillSnapshot, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if s.artifact == nil {
		return nil, agent.ErrUnavailable
	}
	resolved := make([]execpkg.SkillSnapshot, len(refs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrentSkillFetch)
	for i, ref := range refs {
		group.Go(func() error {
			pkg, err := s.artifact.ResolveSkill(groupCtx, identity, ref.ArtifactURI, ref.Shared)
			if err != nil {
				return err
			}
			snapshot, err := buildSkillSnapshot(ref, pkg)
			if err != nil {
				return err
			}
			resolved[i] = snapshot
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return resolved, nil
}

func buildSkillSnapshot(ref agent.SkillRef, resolved client.ResolvedSkill) (execpkg.SkillSnapshot, error) {
	if resolved.VersionURI == "" || len(resolved.Files) == 0 || len(resolved.Files) > maxSkillFilesPerPackage {
		return execpkg.SkillSnapshot{}, fmt.Errorf(
			"%w: skill %q has invalid version manifest",
			agent.ErrInvalid,
			ref.Name,
		)
	}
	seenPaths := make(map[string]bool, len(resolved.Files))
	files := make([]execpkg.SkillFileSnapshot, 0, len(resolved.Files))
	hasEntry := false
	for _, f := range resolved.Files {
		if !validSkillRelativePath(f.Path) || seenPaths[f.Path] || f.FileID == "" || f.FileURI == "" ||
			f.SizeBytes < 0 {
			return execpkg.SkillSnapshot{}, fmt.Errorf(
				"%w: skill %q has invalid file entry %q",
				agent.ErrInvalid,
				ref.Name,
				f.Path,
			)
		}
		seenPaths[f.Path] = true
		mediaType := ""
		if f.MediaType != "" {
			if normalized, ok := normalizeResolvedMediaType(f.MediaType); ok {
				mediaType = normalized
			}
		}
		if f.Path == skillEntryPath {
			if f.SizeBytes <= 0 || f.SizeBytes > maxSkillFileBytes {
				return execpkg.SkillSnapshot{}, fmt.Errorf(
					"%w: skill %q %s must be between 1 and %d bytes",
					agent.ErrInvalid,
					ref.Name,
					skillEntryPath,
					maxSkillFileBytes,
				)
			}
			hasEntry = true
		}
		files = append(files, execpkg.SkillFileSnapshot{
			Path:      f.Path,
			FileID:    f.FileID,
			FileURI:   f.FileURI,
			MediaType: mediaType,
			SizeBytes: f.SizeBytes,
		})
	}
	if !hasEntry {
		return execpkg.SkillSnapshot{}, fmt.Errorf(
			"%w: skill %q is missing %s",
			agent.ErrInvalid,
			ref.Name,
			skillEntryPath,
		)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	description := strings.TrimSpace(ref.Description)
	if description == "" {
		description = strings.TrimSpace(resolved.Description)
	}
	if description == "" {
		description = ref.Name
	}
	if len(description) > 1024 || strings.ContainsFunc(description, unicode.IsControl) {
		return execpkg.SkillSnapshot{}, fmt.Errorf(
			"%w: skill %q description is invalid",
			agent.ErrInvalid,
			ref.Name,
		)
	}
	return execpkg.SkillSnapshot{
		Name:        ref.Name,
		Description: description,
		ArtifactURI: ref.ArtifactURI,
		VersionURI:  resolved.VersionURI,
		Shared:      ref.Shared,
		EntryPath:   skillEntryPath,
		Files:       files,
	}, nil
}

func validSkillRelativePath(raw string) bool {
	if raw == "" || len(raw) > 512 || raw != strings.TrimSpace(raw) ||
		strings.ContainsFunc(raw, unicode.IsControl) || strings.Contains(raw, "\\") ||
		strings.HasPrefix(raw, "/") || raw == "." || raw == ".." || strings.HasPrefix(raw, "../") {
		return false
	}
	return path.Clean(raw) == raw
}

// ReadSkillFile verifies the worker's active lease on runID and reads one file
// exclusively from that run's frozen skill manifest.
func (s *Service) ReadSkillFile(
	ctx context.Context,
	runID, workerID string,
	epoch int64,
	skillName, filePath string,
) (*execpkg.SkillFileContent, error) {
	if s.artifact == nil {
		return nil, agent.ErrUnavailable
	}
	auth, err := s.store.AuthorizeSkillRead(ctx, runID, workerID, epoch, skillName, filePath)
	if err != nil {
		return nil, err
	}
	if auth.File.SizeBytes > maxSkillFileBytes {
		return nil, fmt.Errorf("%w: skill file exceeds %d bytes", agent.ErrInvalid, maxSkillFileBytes)
	}
	identity := client.Identity{
		TenantID:         auth.TenantID,
		PropagatedUserID: auth.OwnerID,
	}
	if auth.ProjectID != "" {
		project := auth.ProjectID
		identity.ProjectID = &project
	}
	data, err := s.artifact.ReadSkillFile(ctx, identity, auth.Shared, auth.File.FileID, maxSkillFileBytes)
	if err != nil {
		return nil, err
	}
	return &execpkg.SkillFileContent{
		Skill:      auth.Skill,
		Path:       auth.File.Path,
		VersionURI: auth.VersionURI,
		MediaType:  auth.File.MediaType,
		SizeBytes:  int64(len(data)),
		Content:    string(data),
	}, nil
}

package durable

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// CheckExecutionLease uses the database clock and rejects canceled, expired, or old attempts.
func (s *Store) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	auth, err := s.AuthorizeExecution(ctx, owner, id, "", epoch)
	if err != nil {
		return false, err
	}
	return auth.Allowed, nil
}

// AuthorizeExecution validates a run claim against the database clock and
// returns the authoritative validity bound min(lease_until, deadline). When
// workerID is non-empty the claim holder must also match that worker.
func (s *Store) AuthorizeExecution(
	ctx context.Context,
	owner, id, workerID string,
	epoch int64,
) (*RunAuthorization, error) {
	var row struct {
		table.Run
		CheckedAt time.Time `gorm:"column:checked_at"`
		ExpiresAt time.Time `gorm:"column:expires_at"`
	}
	q := table.TenantQuery(ctx, s.db).
		Model(&table.Run{}).
		Select("durable_run.*, clock_timestamp() AS checked_at, LEAST(lease_until, deadline) AS expires_at").
		Where(
			"owner_id = ? AND id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()",
			owner, id, epoch, Running,
		)
	if workerID != "" {
		q = q.Where("worker_id = ?", workerID)
	}
	if err := q.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &RunAuthorization{Allowed: false}, nil
		}
		return nil, err
	}
	return &RunAuthorization{
		Allowed:   true,
		RunID:     row.ID,
		WorkerID:  row.WorkerID,
		Epoch:     row.Epoch,
		Scope:     row.Spec.Scope,
		CheckedAt: row.CheckedAt,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// AuthorizeSkillRead verifies the active worker lease on runID and resolves the
// requested file exclusively from the run's frozen skill manifest. Identity comes
// from the authoritative durable_run row, never from caller-supplied headers.
func (s *Store) AuthorizeSkillRead(
	ctx context.Context,
	runID, workerID string,
	epoch int64,
	skillName, filePath string,
) (*AuthorizedSkillRead, error) {
	if runID == "" || workerID == "" || epoch < 1 || strings.TrimSpace(skillName) == "" {
		return nil, ErrInvalid
	}
	var row table.Run
	err := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Select("tenant_id", "project_id", "owner_id", "spec").
		Where(
			"id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()",
			runID, workerID, epoch, Running,
		).
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLeaseLost
		}
		return nil, err
	}
	for _, skill := range row.Spec.Skills {
		if skill.Name != skillName {
			continue
		}
		target := strings.TrimSpace(filePath)
		if target == "" {
			target = skill.EntryPath
			if target == "" {
				target = "SKILL.md"
			}
		}
		if strings.HasPrefix(target, "/") || strings.Contains(target, "\\") ||
			target == "." || target == ".." || strings.HasPrefix(target, "../") ||
			path.Clean(target) != target {
			return nil, ErrInvalid
		}
		for _, f := range skill.Files {
			if f.Path == target {
				return &AuthorizedSkillRead{
					TenantID:   row.TenantID,
					ProjectID:  row.ProjectID,
					OwnerID:    row.OwnerID,
					Skill:      skill.Name,
					VersionURI: skill.VersionURI,
					Shared:     skill.Shared,
					File:       f,
				}, nil
			}
		}
		return nil, ErrNotFound
	}
	return nil, ErrNotFound
}

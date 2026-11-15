package durable

import (
	"context"
	"errors"
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
	q := s.db.WithContext(ctx).
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

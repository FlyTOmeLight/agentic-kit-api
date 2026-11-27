package durable

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

const claimableRun = `(
	(status = 'Queued' AND available_at <= clock_timestamp())
	OR (status = 'Running' AND lease_until <= clock_timestamp())
	OR (status IN ('WaitingClarification', 'NeedsReview', 'WaitingExternal') AND deadline <= clock_timestamp())
)`

func (s *Store) Claim(ctx context.Context, worker string, lease time.Duration) (*Run, error) {
	if worker == "" || lease <= 0 {
		return nil, ErrConflict
	}
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var candidate table.Run
	err = tx.Where(claimableRun).Order("available_at,created_at").
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	reason, err := claimFailureReason(tx, candidate)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		if candidate.ParentRunID != "" {
			if err = tx.Rollback().Error; err != nil {
				return nil, err
			}
			err = s.failChildClaim(ctx, candidate)
		} else if err = failClaim(ctx, tx, candidate, reason); err == nil {
			err = tx.Commit().Error
		}
		if err != nil {
			return nil, err
		}
		return nil, ErrNoWork
	}
	var claimed table.Run
	err = tx.Model(&claimed).Where("id = ?", candidate.ID).Clauses(clause.Returning{}).Updates(map[string]any{
		"status":    Running,
		"worker_id": worker,
		"epoch":     gorm.Expr("epoch + 1"),
		"attempt":   gorm.Expr("attempt + 1"),
		"lease_until": gorm.Expr(
			"clock_timestamp()+?*interval '1 millisecond'",
			lease.Milliseconds(),
		),
		"updated_at": gorm.Expr("clock_timestamp()"),
	}).Error
	if err != nil {
		return nil, err
	}
	r := runFromRow(claimed)
	if err = emit(ctx, tx, r.ID, "RunStarted", map[string]any{"epoch": r.Epoch, "attempt": r.Attempt}); err != nil {
		return nil, err
	}
	return r, tx.Commit().Error
}

func claimFailureReason(tx *gorm.DB, candidate table.Run) (string, error) {
	var expired bool
	if err := tx.Model(&table.Run{}).Select("deadline <= clock_timestamp()").
		Where("id = ?", candidate.ID).Scan(&expired).Error; err != nil {
		return "", err
	}
	if expired {
		return "execution deadline exceeded", nil
	}
	if candidate.Attempt >= candidate.MaxAttempts {
		return "execution attempts exhausted", nil
	}
	return "", nil
}

func (s *Store) failChildClaim(ctx context.Context, candidate table.Run) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockParentRun(ctx, tx, candidate); err != nil {
			return err
		}
		var child table.Run
		err := tx.Where("tenant_id = ? AND id = ?", candidate.TenantID, candidate.ID).
			Where(claimableRun).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&child).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		reason, err := claimFailureReason(tx, child)
		if err != nil || reason == "" {
			return err
		}
		return failClaim(ctx, tx, child, reason)
	})
}

func failClaim(ctx context.Context, tx *gorm.DB, run table.Run, reason string) error {
	if err := tx.Model(&table.Run{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status":               Failed,
		"epoch":                gorm.Expr("epoch + 1"),
		"lease_until":          nil,
		"worker_id":            "",
		"error":                reason,
		"waiting_step":         "",
		"waiting_kind":         "",
		"waiting_external_ref": "",
	}).Error; err != nil {
		return err
	}
	if err := emit(ctx, tx, run.ID, "RunFailed", map[string]any{"error": reason}); err != nil {
		return err
	}
	run.Status = Failed
	run.Error = reason
	return finishTerminalRun(ctx, tx, &run)
}

func (s *Store) Renew(ctx context.Context, l Lease, d time.Duration) error {
	tag := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).
		Updates(map[string]any{"lease_until": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", d.Milliseconds()), "updated_at": gorm.Expr("clock_timestamp()")})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

// SaveState persists the executor's opaque state envelope under the caller's
// lease without emitting a boundary event. Workers use it to durably mark an
// in-flight model or summary dispatch before the provider call, so a crashed
// worker can be distinguished from one that simply has not reported usage yet.
func (s *Store) SaveState(ctx context.Context, l Lease, state json.RawMessage) error {
	if !json.Valid(state) {
		return ErrInvalid
	}
	tag := table.TenantQuery(ctx, s.db).
		Model(&table.Run{}).
		Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).
		Updates(map[string]any{"state": string(state), "updated_at": gorm.Expr("clock_timestamp()")})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

func lockLease(ctx context.Context, tx *gorm.DB, l Lease) (*Run, error) {
	r, err := readRun(
		tx.Model(&table.Run{}).
			Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseLost
	}
	return r, err
}

func (s *Store) TakePendingSteer(
	ctx context.Context,
	l Lease,
	checkpoint func(string) json.RawMessage,
) (string, error) {
	if checkpoint == nil {
		return "", ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := lockLease(ctx, tx, l); err != nil {
		return "", err
	}
	var steer string
	if err := tx.Model(&table.Run{}).
		Where("id = ?", l.RunID).
		Select("pending_steer").
		Scan(&steer).
		Error; err != nil {
		return "", err
	}
	if steer == "" {
		return "", tx.Commit().Error
	}
	state := checkpoint(steer)
	if !json.Valid(state) {
		return "", ErrInvalid
	}
	if err := tx.Model(&table.Run{}).
		Where("id = ?", l.RunID).
		Updates(map[string]any{"pending_steer": "", "state": string(state)}).
		Error; err != nil {
		return "", err
	}
	return steer, tx.Commit().Error
}

package durable

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

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
	r, err := readRun(
		tx.Model(&table.Run{}).
			Where("(status='Queued' AND available_at<=clock_timestamp()) OR (status='Running' AND lease_until<=clock_timestamp()) OR (status IN ('WaitingClarification','NeedsReview') AND deadline<=clock_timestamp())").
			Order("available_at,created_at").
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}),
	)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var expired bool
	if err = tx.Model(&table.Run{}).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", r.ID).
		Scan(&expired).
		Error; err != nil {
		return nil, err
	}
	if expired || r.Attempt >= r.MaxAttempts {
		reason := "execution deadline exceeded"
		if !expired {
			reason = "execution attempts exhausted"
		}
		err = tx.Model(&table.Run{}).
			Where("id = ?", r.ID).
			Updates(map[string]any{
				"status":       Failed,
				"epoch":        gorm.Expr("epoch + 1"),
				"lease_until":  nil,
				"error":        reason,
				"waiting_step": "",
				"waiting_kind": "",
			}).
			Error
		if err != nil {
			return nil, err
		}
		if err = emit(ctx, tx, r.ID, "RunFailed", map[string]any{"error": reason}); err != nil {
			return nil, err
		}
		if err = tx.Commit().Error; err != nil {
			return nil, err
		}
		return nil, ErrNoWork
	}
	var claimed table.Run
	err = tx.Model(&claimed).Where("id = ?", r.ID).Clauses(clause.Returning{}).Updates(map[string]any{
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
	r = runFromRow(claimed)
	if err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, r.ID, "RunStarted", map[string]any{"epoch": r.Epoch, "attempt": r.Attempt}); err != nil {
		return nil, err
	}
	return r, tx.Commit().Error
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

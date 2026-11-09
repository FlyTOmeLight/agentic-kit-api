package durable

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func (s *Store) Retry(ctx context.Context, l Lease, state json.RawMessage, reason string, delay time.Duration) error {
	if !json.Valid(state) {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	status := Queued
	if r.Attempt >= r.MaxAttempts {
		status = Failed
	}
	err = tx.Model(&table.Run{}).
		Where("id = ?", l.RunID).
		Updates(map[string]any{
			"state":        string(state),
			"status":       status,
			"error":        reason,
			"lease_until":  nil,
			"available_at": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", delay.Milliseconds()),
			"waiting_step": "",
			"waiting_kind": "",
		}).
		Error
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "RunRetry", map[string]any{"status": status, "detail": reason}); err != nil {
		return err
	}
	return tx.Commit().Error
}

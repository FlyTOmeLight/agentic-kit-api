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

func (s *Store) Retry(ctx context.Context, l Lease, state json.RawMessage, reason string, delay time.Duration) error {
	if !json.Valid(state) {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	var binding table.Run
	if err := table.TenantQuery(ctx, tx).
		Select("tenant_id", "parent_run_id", "attempt", "max_attempts").
		Where("id = ? AND worker_id = ? AND epoch = ?", l.RunID, l.WorkerID, l.Epoch).
		Take(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLeaseLost
		}
		return err
	}
	if binding.ParentRunID != "" && binding.Attempt >= binding.MaxAttempts {
		if err := lockParentRun(ctx, tx, binding); err != nil {
			return err
		}
	}
	r, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return err
	}
	status := Queued
	if r.Attempt >= r.MaxAttempts {
		status = Failed
	}
	var stored table.Run
	err = table.TenantQuery(ctx, tx.Model(&stored)).
		Where("id = ?", l.RunID).
		Clauses(clause.Returning{}).
		Updates(map[string]any{
			"state":       string(state),
			"status":      status,
			"error":       reason,
			"lease_until": nil,
			"worker_id":   "",
			"available_at": gorm.Expr(
				"clock_timestamp()+?*interval '1 millisecond'",
				delay.Milliseconds(),
			),
			"waiting_step":         "",
			"waiting_kind":         "",
			"waiting_external_ref": "",
		}).
		Error
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "RunRetry", map[string]any{"status": status, "detail": reason}); err != nil {
		return err
	}
	if status.Terminal() {
		if err = finishTerminalRun(ctx, tx, &stored); err != nil {
			return err
		}
	}
	return tx.Commit().Error
}

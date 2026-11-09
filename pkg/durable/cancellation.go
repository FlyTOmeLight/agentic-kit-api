package durable

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func (s *Store) Cancel(ctx context.Context, owner, id string) error {
	return s.db.WithContext(ctx).
		Transaction(func(tx *gorm.DB) error { _, err := cancelRun(ctx, tx, owner, id); return err })
}

func cancelRun(ctx context.Context, tx *gorm.DB, owner, id string) (bool, error) {
	r, err := readRun(
		tx.Model(&table.Run{}).
			Where("id = ? AND owner_id = ?", id, owner).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return false, err
	}
	if r.Status.Terminal() {
		return false, nil
	}
	if err = tx.Model(&table.Run{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":       Canceled,
			"epoch":        gorm.Expr("epoch + 1"),
			"lease_until":  nil,
			"waiting_step": "",
			"waiting_kind": "",
		}).
		Error; err != nil {
		return false, err
	}
	return true, emit(ctx, tx, id, "RunCanceled", map[string]any{"status": Canceled})
}

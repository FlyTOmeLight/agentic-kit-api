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
	var binding table.Run
	if err := table.TenantQuery(ctx, tx).Select("tenant_id", "parent_run_id").
		Where("id = ? AND owner_id = ?", id, owner).Take(&binding).Error; err != nil {
		return false, recordError(err)
	}
	if err := lockParentRun(ctx, tx, binding); err != nil {
		return false, err
	}
	var stored table.Run
	err := table.TenantQuery(ctx, tx).Where("id = ? AND owner_id = ?", id, owner).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&stored).Error
	if err != nil {
		return false, recordError(err)
	}
	if stored.Status.Terminal() {
		return false, nil
	}
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":               Canceled,
			"epoch":                gorm.Expr("epoch + 1"),
			"lease_until":          nil,
			"worker_id":            "",
			"error":                "canceled",
			"waiting_step":         "",
			"waiting_kind":         "",
			"waiting_external_ref": "",
		}).
		Error; err != nil {
		return false, err
	}
	if err = emit(ctx, tx, id, "RunCanceled", map[string]any{"status": Canceled}); err != nil {
		return false, err
	}
	stored.Status = Canceled
	stored.Error = "canceled"
	if err = finishTerminalRun(ctx, tx, &stored); err != nil {
		return false, err
	}
	return true, nil
}

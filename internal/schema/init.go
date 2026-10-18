// Package schema initializes the target table models on an empty schema. It
// does not migrate, backfill or probe older structures.
package schema

import (
	"context"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// Init creates or completes the target models. It is safe to run repeatedly on
// the same schema; concurrent initializers are serialized by an advisory lock.
func Init(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7412101)").Error; err != nil {
			return err
		}
		return tx.AutoMigrate(
			&table.Agent{},
			&table.AgentVersion{},
			&table.Session{},
			&table.Run{},
			&table.Step{},
			&table.Event{},
			&table.Message{},
		)
	})
}

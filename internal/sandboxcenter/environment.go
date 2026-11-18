// environment.go closes durable run-environment intents once the sandbox they
// are bound to has converged. The table belongs to pkg/durable, but the
// durable store may not be imported here (it depends on this package for
// tests), so the reconciler writes the same status transitions directly. The
// status vocabulary must stay in sync with pkg/durable/environment.go.
package sandboxcenter

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

const (
	envStatusPreparing = "Preparing"
	envStatusActive    = "Active"
	envStatusReleased  = "Released"
	envStatusLost      = "Lost"
)

// CloseEnvironmentsForSandbox finalizes every unfinished environment record
// bound to one sandbox ID after provider evidence confirmed its destruction.
// Destroyed sandboxes released their workspace; lost ones never activated.
// It is intentionally not lease-checked: the run claim that owned the
// environment may already be gone.
func (s *Store) CloseEnvironmentsForSandbox(ctx context.Context, sandboxID string, destroyed bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sandbox Sandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sandboxID).First(&sandbox).Error; err != nil {
			return err
		}
		status := envStatusLost
		checkpoint := ""
		switch sandbox.Status {
		case SandboxStatusDestroyed:
			if !destroyed {
				return ErrConflict
			}
			status = envStatusReleased
			if sandbox.RequiredSave {
				if sandbox.StoppedAt == nil || sandbox.PodUID == "" || sandbox.WorkspaceUID == "" ||
					validateCheckpointID(sandbox.SavedCheckpoint) != nil {
					return ErrSaveBlocked
				}
				checkpoint = sandbox.SavedCheckpoint
			}
		case SandboxStatusLost:
			if destroyed {
				return ErrConflict
			}
		default:
			return ErrConflict
		}
		return tx.Model(&table.RunEnvironment{}).
			Where("sandbox_id = ? AND (status IN ? OR (status = ? AND saved_checkpoint = '' AND ? <> ''))",
				sandboxID, []string{envStatusPreparing, envStatusActive}, status, checkpoint).
			Updates(map[string]any{
				"status":           status,
				"saved_checkpoint": checkpoint,
				"released_at":      gorm.Expr("COALESCE(released_at, clock_timestamp())"),
				"updated_at":       gorm.Expr("clock_timestamp()"),
			}).Error
	})
}

package durable

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func (s *Store) Unblock(ctx context.Context, owner, id string, epoch int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if epoch < 1 || reason == "" || len(reason) > 4096 {
		return ErrInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var binding table.Run
		if err := table.TenantQuery(ctx, tx).
			Where("id = ? AND owner_id = ?", id, owner).Take(&binding).Error; err != nil {
			return recordError(err)
		}
		var goal table.Goal
		if binding.GoalID != "" {
			if err := table.TenantQuery(ctx, tx).
				Where("tenant_id = ? AND id = ? AND owner_id = ?", binding.TenantID, binding.GoalID, owner).
				Clauses(clause.Locking{Strength: "UPDATE"}).Take(&goal).Error; err != nil {
				return recordError(err)
			}
		}
		run, err := readRun(table.TenantQuery(ctx, tx).
			Where("id = ? AND owner_id = ?", id, owner).
			Clauses(clause.Locking{Strength: "UPDATE"}))
		if err != nil {
			return err
		}
		if run.Status != Blocked || run.Epoch != epoch || run.GoalID != binding.GoalID ||
			run.SessionID != binding.SessionID {
			return ErrConflict
		}
		var session table.Session
		if err = table.TenantQuery(ctx, tx).
			Where("tenant_id = ? AND id = ? AND owner_id = ?", binding.TenantID, run.SessionID, owner).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConflict
			}
			return recordError(err)
		}
		if err = ensureNoActiveRun(table.TenantQuery(ctx, tx).Where("tenant_id = ?", binding.TenantID), owner, run.SessionID); err != nil {
			return err
		}
		var now time.Time
		if err = tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return err
		}
		if !run.Deadline.After(now) {
			return ErrConflict
		}
		if run.GoalID != "" {
			if (goal.Status != GoalBlocked && goal.Status != GoalActive) || usageOverBudget(&goal) ||
				goal.StallCount >= 3 ||
				(goal.Budget.MaxWallClockSeconds > 0 &&
					!goal.CreatedAt.Add(time.Duration(goal.Budget.MaxWallClockSeconds)*time.Second).After(now)) ||
				(run.ParentRunID == "" && goal.ActiveRunID != run.ID) {
				return ErrConflict
			}
		}
		nextState, marker, err := execution.ReleaseInflightModel(run.State)
		if err != nil {
			return err
		}
		if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", id).
			Updates(map[string]any{
				"state": string(nextState), "status": Queued, "attempt": 0,
				"epoch": gorm.Expr("epoch + 1"), "available_at": gorm.Expr("clock_timestamp()"),
				"updated_at": gorm.Expr("clock_timestamp()"), "error": "",
				"worker_id": "", "lease_until": nil,
				"waiting_step": "", "waiting_kind": "", "waiting_external_ref": "",
			}).Error; err != nil {
			return recordError(err)
		}
		resumedGoal := false
		if run.GoalID != "" && goal.Status == GoalBlocked && goal.ActiveRunID == run.ID {
			var otherBlocked int64
			if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
				Where("tenant_id = ? AND owner_id = ? AND goal_id = ? AND status = ? AND id <> ?",
					binding.TenantID, owner, run.GoalID, Blocked, id).
				Count(&otherBlocked).Error; err != nil {
				return err
			}
			if otherBlocked == 0 {
				if err = table.TenantQuery(ctx, tx.Model(&table.Goal{})).Where("id = ?", run.GoalID).
					Updates(map[string]any{"status": GoalActive, "updated_at": gorm.Expr("clock_timestamp()")}).Error; err != nil {
					return err
				}
				resumedGoal = true
			}
		}
		if err = emit(ctx, tx, id, "RunUnblocked", map[string]any{
			"status": Queued, "actorID": owner, "reason": reason,
			"blockedEpoch": epoch, "epoch": epoch + 1,
			"inflightModel": marker, "previousError": run.Error,
			"goalID": run.GoalID, "goalResumed": resumedGoal,
		}); err != nil {
			return err
		}
		return nil
	})
}

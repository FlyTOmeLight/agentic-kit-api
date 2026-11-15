package durable

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// ErrUsageBudgetExceeded reports that the goal budget was crossed before the
// next model call. The executor converts it into a terminal run failure so no
// new model or tool work is dispatched.
var ErrUsageBudgetExceeded = errors.New("goal usage budget exceeded")

// usageOverBudget reports whether the goal usage crosses a configured budget.
// Unknown usage is zero and therefore never trips a budget by itself.
func usageOverBudget(goal *table.Goal) bool {
	return (goal.Budget.MaxTokens > 0 && goal.TokensUsed >= goal.Budget.MaxTokens) ||
		(goal.Budget.MaxCostUSD > 0 && goal.CostUSD >= goal.Budget.MaxCostUSD)
}

// chargeGoalUsage adds delta to the goal row the run belongs to. The goal row
// lock serializes concurrent charges from the parent and child runs.
// Returns true when the goal was blocked by this charge.
func chargeGoalUsage(ctx context.Context, tx *gorm.DB, run *Run, delta int64) (bool, error) {
	if run.GoalID == "" || delta <= 0 {
		return false, nil
	}
	var goalRow table.Goal
	if err := tx.Model(&table.Goal{}).
		Where("owner_id = ? AND id = ?", run.OwnerID, run.GoalID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&goalRow).
		Error; err != nil {
		return false, recordError(err)
	}
	if goalRow.Status != GoalActive {
		return false, nil
	}
	goalRow.TokensUsed += delta
	updates := map[string]any{
		"tokens_used": goalRow.TokensUsed,
		"updated_at":  gorm.Expr("clock_timestamp()"),
	}
	blocked := usageOverBudget(&goalRow)
	if blocked {
		updates["status"] = GoalBlocked
	}
	if err := tx.Model(&table.Goal{}).Where("id = ?", goalRow.ID).Updates(updates).Error; err != nil {
		return false, err
	}
	return blocked, nil
}

// CheckUsageBudget stops the next model call when the goal budget is already
// crossed. A run without a goal has no shared budget and passes.
func (s *Store) CheckUsageBudget(ctx context.Context, l Lease) error {
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	if run.GoalID == "" {
		return tx.Commit().Error
	}
	var goalRow table.Goal
	if err := tx.Where("owner_id = ? AND id = ?", run.OwnerID, run.GoalID).
		Take(&goalRow).
		Error; err != nil {
		return recordError(err)
	}
	if goalRow.Status == GoalActive && usageOverBudget(&goalRow) {
		return ErrUsageBudgetExceeded
	}
	return tx.Commit().Error
}

// RecordUsage commits one model call's usage exactly once per (run, turn,
// call). The usage row's primary key makes checkpoint replays no-ops, and the
// state column carries the checkpoint with the recorded turn so a crash after
// the model call cannot charge the same turn again.
//
// Unknown usage records as zero tokens so operators can see the call happened
// without inventing a cost; zero never trips a budget.
func (s *Store) RecordUsage(
	ctx context.Context,
	l Lease,
	turn int64,
	call string,
	tokens int64,
	state json.RawMessage,
) error {
	if turn < 0 || call == "" || tokens < 0 {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	record := table.Usage{
		RunID:  l.RunID,
		Turn:   turn,
		Call:   call,
		Tokens: tokens,
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Same turn and call already recorded: a checkpoint replay.
		return tx.Commit().Error
	}
	row := tx.Model(&table.Run{}).Where("id = ?", l.RunID)
	updates := map[string]any{
		"state":      string(state),
		"updated_at": gorm.Expr("clock_timestamp()"),
	}
	if tokens > 0 {
		updates["tokens_used"] = gorm.Expr("tokens_used + ?", tokens)
	}
	if err := row.Updates(updates).Error; err != nil {
		return err
	}
	blocked, err := chargeGoalUsage(ctx, tx, run, tokens)
	if err != nil {
		return err
	}
	if err := emit(ctx, tx, l.RunID, "UsageRecorded", map[string]any{
		"turn":          turn,
		"call":          call,
		"tokens":        tokens,
		"tokensUsed":    run.TokensUsed + tokens,
		"budgetBlocked": blocked,
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

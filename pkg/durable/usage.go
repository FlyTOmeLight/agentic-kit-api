package durable

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// ErrUsageBudgetExceeded reports that the goal budget was crossed before the
// next model call. The executor converts it into a terminal run failure so no
// new model or tool work is dispatched.
var ErrUsageBudgetExceeded = errors.New("goal usage budget exceeded")

// usageOverBudget reports whether the goal usage has reached a configured
// budget.
func usageOverBudget(goal *table.Goal) bool {
	return (goal.Budget.MaxTokens > 0 && goal.TokensUsed >= goal.Budget.MaxTokens) ||
		(goal.Budget.MaxCostUSD > 0 && goal.CostUSD >= goal.Budget.MaxCostUSD)
}

// chargeGoalUsage adds delta to the goal row the run belongs to. Usage is
// accounted for regardless of goal status so in-flight sibling calls are not
// lost after one call crosses the budget. The goal row lock serializes
// concurrent charges from parent and child runs.
//
// The goal moves to Blocked only on the Active-to-Blocked transition. Returns
// true only for that transition so callers do not report a later in-flight
// charge as the cause of the block.
func chargeGoalUsage(ctx context.Context, tx *gorm.DB, run *Run, tokens int64, costUSD float64) (bool, error) {
	if run.GoalID == "" || (tokens == 0 && costUSD == 0) {
		return false, nil
	}
	var goalRow table.Goal
	if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
		Where("owner_id = ? AND id = ?", run.OwnerID, run.GoalID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&goalRow).
		Error; err != nil {
		return false, recordError(err)
	}
	goalRow.TokensUsed += tokens
	goalRow.CostUSD += costUSD
	updates := map[string]any{
		"tokens_used": goalRow.TokensUsed,
		"cost_usd":    goalRow.CostUSD,
		"updated_at":  gorm.Expr("clock_timestamp()"),
	}
	blocked := goalRow.Status == GoalActive && usageOverBudget(&goalRow)
	if blocked {
		updates["status"] = GoalBlocked
	}
	if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).Where("id = ?", goalRow.ID).Updates(updates).Error; err != nil {
		return false, err
	}
	return blocked, nil
}

// CheckUsageBudget stops the next model call when the goal budget is already
// crossed, including after an earlier charge moved the goal to Blocked. A run
// without a goal has no shared budget and passes.
func (s *Store) CheckUsageBudget(ctx context.Context, l Lease) error {
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return err
	}
	if run.GoalID == "" {
		return tx.Commit().Error
	}
	var goalRow table.Goal
	if err := table.TenantQuery(ctx, tx).Where("owner_id = ? AND id = ?", run.OwnerID, run.GoalID).
		Take(&goalRow).
		Error; err != nil {
		return recordError(err)
	}
	if usageOverBudget(&goalRow) {
		return ErrUsageBudgetExceeded
	}
	return tx.Commit().Error
}

// RecordUsage commits one model call's usage exactly once per (run, turn,
// call). The usage row's primary key makes checkpoint replays no-ops, and the
// state column carries the checkpoint with the recorded turn so a crash after
// the model call cannot charge the same turn again.
//
// tokens is the known provider-reported total for this call. Callers must not
// use zero to represent unknown usage.
func (s *Store) RecordUsage(
	ctx context.Context,
	l Lease,
	turn int64,
	call string,
	tokens int64,
	state json.RawMessage,
) error {
	return s.RecordUsageWithCost(ctx, l, turn, call, tokens, 0, state)
}

func (s *Store) RecordUsageWithCost(
	ctx context.Context,
	l Lease,
	turn int64,
	call string,
	tokens int64,
	costUSD float64,
	state json.RawMessage,
) error {
	if turn < 0 || call == "" || tokens < 0 || costUSD < 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	var binding table.Run
	if err := table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Select("owner_id", "goal_id").
		Where("id = ? AND worker_id = ? AND epoch = ?", l.RunID, l.WorkerID, l.Epoch).
		Take(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLeaseLost
		}
		return err
	}
	if binding.GoalID != "" {
		var goal table.Goal
		if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
			Where("owner_id = ? AND id = ?", binding.OwnerID, binding.GoalID).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&goal).Error; err != nil {
			return recordError(err)
		}
	}
	run, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return err
	}
	if run.OwnerID != binding.OwnerID || run.GoalID != binding.GoalID {
		return ErrConflict
	}
	record := table.Usage{
		RunID:   l.RunID,
		Turn:    turn,
		Call:    call,
		Tokens:  tokens,
		CostUSD: costUSD,
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Same turn and call already recorded: a checkpoint replay.
		return tx.Commit().Error
	}
	row := table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", l.RunID)
	updates := map[string]any{
		"state":      string(state),
		"updated_at": gorm.Expr("clock_timestamp()"),
	}
	if tokens > 0 {
		updates["tokens_used"] = gorm.Expr("tokens_used + ?", tokens)
	}
	if costUSD > 0 {
		updates["cost_usd"] = gorm.Expr("cost_usd + ?", costUSD)
	}
	if err := row.Updates(updates).Error; err != nil {
		return err
	}
	blocked, err := chargeGoalUsage(ctx, tx, run, tokens, costUSD)
	if err != nil {
		return err
	}
	if err := emit(ctx, tx, l.RunID, "UsageRecorded", map[string]any{
		"turn":          turn,
		"call":          call,
		"tokens":        tokens,
		"tokensUsed":    run.TokensUsed + tokens,
		"costUSD":       costUSD,
		"costUSDUsed":   run.CostUSD + costUSD,
		"budgetBlocked": blocked,
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

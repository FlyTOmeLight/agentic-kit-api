package durable

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

const childExternalRefPrefix = "child:"

// SpawnChildRun creates a child run owned by the calling run's lease holder.
// The child runs in its own session with a spec inherited from the parent;
// the parent step records the child reference so a terminal child can wake
// the parent deterministically.
func (s *Store) SpawnChildRun(ctx context.Context, l Lease, stepID, query string, ttl time.Duration) (*Run, error) {
	if stepID == "" || query == "" || ttl <= 0 {
		return nil, ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, err
	}
	defer tx.Rollback()
	parent, err := lockLease(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	var step table.Step
	err = tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ? AND status = 'Started' AND external_ref = ''", l.RunID, stepID).
		Take(&step).
		Error
	if err != nil {
		return nil, recordError(err)
	}
	parentSnapshot := parent.Spec
	childSession := table.Session{
		ID:      NewID(),
		OwnerID: parent.OwnerID,
		AgentID: parentSnapshot.AgentID,
		Title:   "child of " + parent.ID,
	}
	if err = tx.Create(&childSession).Error; err != nil {
		return nil, err
	}
	childSnapshot := table.ExecutionSnapshot(parentSnapshot)
	childSnapshot.Query = query
	childSnapshot.History = nil
	data, err := json.Marshal(childSnapshot)
	if err != nil {
		return nil, err
	}
	childID := NewID()
	if err = tx.Model(&table.Run{}).Create(map[string]any{
		"id":             childID,
		"owner_id":       parent.OwnerID,
		"session_id":     childSession.ID,
		"request_key":    l.RunID + ":" + stepID,
		"request_hash":   Hash([]byte(query)),
		"spec":           string(data),
		"status":         Queued,
		"deadline":       gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", ttl.Milliseconds()),
		"max_attempts":   parent.MaxAttempts,
		"goal_id":        parent.GoalID,
		"parent_run_id":  parent.ID,
		"parent_step_id": stepID,
	}).Error; err != nil {
		return nil, recordError(err)
	}
	if err = tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ?", l.RunID, stepID).
		Update("external_ref", childExternalRefPrefix+childID).
		Error; err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, l.RunID, "ChildRunSpawned", map[string]any{
		"stepID":  stepID,
		"childID": childID,
	}); err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, childID, "RunQueued", map[string]any{"status": Queued}); err != nil {
		return nil, err
	}
	child, err := readRun(tx.Model(&table.Run{}).Where("id = ?", childID))
	if err != nil {
		return nil, err
	}
	child.EventSeq = 1
	return child, tx.Commit().Error
}

// handleChildTerminal runs inside the transaction that moved a child run to a
// terminal status. It charges the goal budget, then either wakes the waiting
// parent with the child outcome or records a ChildOrphaned event when the
// parent can no longer accept the result.
func handleChildTerminal(ctx context.Context, tx *gorm.DB, child *table.Run) error {
	if child.ParentRunID == "" {
		return nil
	}
	if child.GoalID != "" {
		if err := chargeGoalBudget(ctx, tx, child); err != nil {
			return err
		}
	}
	result := child.Output
	if child.Status != Completed {
		reason := child.Error
		if reason == "" {
			reason = string(child.Status)
		}
		result = "subtask " + child.ID + " " + string(child.Status) + ": " + reason
	}
	var step struct{ Result, Status string }
	err := tx.Model(&table.Step{}).
		Select("result", "status").
		Where("run_id = ? AND step_id = ?", child.ParentRunID, child.ParentStepID).
		Take(&step).
		Error
	if err == nil && step.Status == "Completed" {
		if step.Result == result {
			return nil
		}
		return emit(ctx, tx, child.ID, "ChildOrphaned", map[string]any{
			"parentRunID":  child.ParentRunID,
			"parentStepID": child.ParentStepID,
			"reason":       "parent step already completed with a different result",
		})
	}
	parent, err := readRun(
		tx.Model(&table.Run{}).
			Where("id = ?", child.ParentRunID).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return err
	}
	if parent.Status.Terminal() ||
		parent.Status != WaitingExternal ||
		parent.WaitingStep != child.ParentStepID {
		return emit(ctx, tx, child.ID, "ChildOrphaned", map[string]any{
			"parentRunID":  child.ParentRunID,
			"parentStepID": child.ParentStepID,
			"parentStatus": parent.Status,
		})
	}
	var expired bool
	if err = tx.Model(&table.Run{}).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", parent.ID).
		Scan(&expired).
		Error; err != nil {
		return err
	}
	if expired {
		return emit(ctx, tx, child.ID, "ChildOrphaned", map[string]any{
			"parentRunID":  child.ParentRunID,
			"parentStepID": child.ParentStepID,
			"reason":       "parent deadline exceeded",
		})
	}
	if err = tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ?", parent.ID, child.ParentStepID).
		Updates(map[string]any{"status": "Completed", "result": result}).
		Error; err != nil {
		return err
	}
	if err = tx.Model(&table.Run{}).
		Where("id = ?", parent.ID).
		Updates(map[string]any{
			"status":               Queued,
			"attempt":              0,
			"available_at":         gorm.Expr("clock_timestamp()"),
			"error":                "",
			"waiting_step":         "",
			"waiting_kind":         "",
			"waiting_external_ref": "",
		}).
		Error; err != nil {
		return err
	}
	if err = appendMessage(
		tx,
		parent,
		"Tool",
		Message{Role: "tool", Content: result, ToolCallID: child.ParentStepID},
	); err != nil {
		return err
	}
	if err = emit(ctx, tx, child.ID, "ChildRunCompleted", map[string]any{
		"parentRunID":  child.ParentRunID,
		"parentStepID": child.ParentStepID,
		"status":       child.Status,
	}); err != nil {
		return err
	}
	return emit(ctx, tx, parent.ID, "RunQueued", map[string]any{"status": Queued})
}

// cascadeCancelChildren cancels every active child run of a parent that just
// reached a terminal state. Each canceled child is routed through
// handleChildTerminal so bookkeeping (budget, events) stays in one place.
func cascadeCancelChildren(ctx context.Context, tx *gorm.DB, parentID string) error {
	var children []table.Run
	if err := tx.Model(&table.Run{}).
		Where("parent_run_id = ? AND status IN ?", parentID, []Status{
			Queued, Running, WaitingClarification, NeedsReview, WaitingExternal, Blocked,
		}).
		Find(&children).Error; err != nil {
		return err
	}
	for _, child := range children {
		if err := tx.Model(&table.Run{}).
			Where("id = ?", child.ID).
			Updates(map[string]any{
				"status":               Canceled,
				"epoch":                gorm.Expr("epoch + 1"),
				"lease_until":          nil,
				"worker_id":            "",
				"error":                "parent run terminated",
				"waiting_step":         "",
				"waiting_kind":         "",
				"waiting_external_ref": "",
			}).
			Error; err != nil {
			return err
		}
		if err := emit(ctx, tx, child.ID, "RunCanceled", map[string]any{
			"status": Canceled,
			"reason": "parent run terminated",
		}); err != nil {
			return err
		}
		child.Status = Canceled
		if err := handleChildTerminal(ctx, tx, &child); err != nil {
			return err
		}
	}
	return nil
}

// chargeGoalBudget folds a terminal child run's usage into its goal. The goal
// row is locked; crossing a configured budget blocks the goal so subtasks
// cannot bypass the budget by splitting work.
func chargeGoalBudget(ctx context.Context, tx *gorm.DB, child *table.Run) error {
	var goalRow table.Goal
	if err := tx.Model(&table.Goal{}).
		Where("owner_id = ? AND id = ?", child.OwnerID, child.GoalID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&goalRow).
		Error; err != nil {
		return recordError(err)
	}
	if goalRow.Status != GoalActive {
		return nil
	}
	goalRow.TokensUsed += child.TokensUsed
	goalRow.CostUSD += child.CostUSD
	updates := map[string]any{
		"tokens_used": goalRow.TokensUsed,
		"cost_usd":    goalRow.CostUSD,
		"updated_at":  gorm.Expr("clock_timestamp()"),
	}
	if (goalRow.Budget.MaxTokens > 0 && goalRow.TokensUsed > goalRow.Budget.MaxTokens) ||
		(goalRow.Budget.MaxCostUSD > 0 && goalRow.CostUSD > goalRow.Budget.MaxCostUSD) {
		updates["status"] = GoalBlocked
	}
	return tx.Model(&table.Goal{}).Where("id = ?", goalRow.ID).Updates(updates).Error
}

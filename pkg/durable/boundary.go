package durable

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// CommitBoundary commits one legal execution boundary together with its
// message, waiting metadata and event. The boundary kind decides the status,
// message kind and event name; callers cannot choose them independently.
func (s *Store) CommitBoundary(ctx context.Context, l Lease, b Boundary) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if b.Kind == BoundaryBlock {
		return s.commitBlockBoundary(ctx, l, b)
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return err
	}
	if b.WaitingStep != "" {
		var step table.Step
		err = tx.Model(&step).
			Select("status").
			Where("run_id = ? AND step_id = ?", l.RunID, b.WaitingStep).
			Take(&step).
			Error
		if err != nil || step.Status != "Started" {
			return ErrConflict
		}
	}
	status, kind, messageKind := boundaryOutcome(b.Kind)
	updates := map[string]any{
		"state":                string(b.State),
		"status":               status,
		"output":               b.Output,
		"error":                b.Detail,
		"waiting_step":         b.WaitingStep,
		"waiting_kind":         b.Kind.Interaction(),
		"waiting_external_ref": b.ExternalRef,
	}
	if status != Running {
		updates["lease_until"] = nil
		updates["worker_id"] = ""
	}
	result := table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", l.RunID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	if b.Kind == BoundaryComplete {
		err = table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", l.RunID).Where(`EXISTS (
			SELECT 1 FROM durable_goal AS goal
			JOIN durable_run_environment AS env ON env.run_id = durable_run.id
			WHERE goal.id = durable_run.goal_id AND goal.owner_id = durable_run.owner_id
			  AND goal.tenant_id = durable_run.tenant_id
			  AND goal.status = 'Active' AND goal.active_run_id = durable_run.id
			  AND goal.evaluator->>'kind' = 'command'
			  AND btrim(COALESCE(goal.evaluator->>'config', '')) <> ''
			  AND env.status = 'Active' AND env.run_epoch = durable_run.epoch
			  AND NOT EXISTS (SELECT 1 FROM durable_evaluation AS evaluation
			                  WHERE evaluation.goal_id = goal.id AND evaluation.run_id = durable_run.id)
		)`).Updates(map[string]any{
			"evaluation_deadline":  gorm.Expr("LEAST(deadline, clock_timestamp() + interval '10 minutes')"),
			"evaluation_worker_id": l.WorkerID,
		}).Error
		if err != nil {
			return err
		}
	}
	if messageKind != "" {
		if err = appendMessage(
			tx,
			run,
			messageKind,
			Message{Role: "assistant", Content: b.Output},
		); err != nil {
			return err
		}
	}
	if b.Kind == BoundaryComplete && run.CompactedSummary != "" && run.FirstKeptSeq > 0 {
		through := run.FirstKeptSeq - 1
		if err = table.TenantQuery(ctx, tx.Model(&table.Session{})).
			Where("id = ? AND owner_id = ? AND compacted_through_seq <= ?", run.SessionID, run.OwnerID, through).
			Updates(map[string]any{
				"compacted_summary":     run.CompactedSummary,
				"compacted_through_seq": through,
			}).Error; err != nil {
			return err
		}
	}
	payload := map[string]any{
		"status":      status,
		"output":      b.Output,
		"detail":      b.Detail,
		"waitingStep": b.WaitingStep,
		"waitingKind": b.Kind.Interaction(),
	}
	if b.Context != nil {
		payload["context"] = b.Context
	}
	if err = emit(ctx, tx, l.RunID, kind, payload); err != nil {
		return err
	}
	if status.Terminal() {
		row := table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", l.RunID)
		var stored table.Run
		if err = row.Take(&stored).Error; err != nil {
			return err
		}
		if err = handleChildTerminal(ctx, tx, &stored); err != nil {
			return err
		}
		if err = cascadeCancelChildren(ctx, tx, l.RunID); err != nil {
			return err
		}
	}
	return tx.Commit().Error
}

// commitBlockBoundary parks a run the worker cannot continue and releases its
// lease in one transaction with the goal state. A blocked run is not
// terminal: no child wake-up or cancel cascade runs, and a blocked goal waits
// for a new iteration instead of converging. The goal lock is taken before
// the run lock, matching RecordUsageWithCost, so concurrent usage accounting
// and blocking cannot deadlock.
func (s *Store) commitBlockBoundary(ctx context.Context, l Lease, b Boundary) error {
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
	if _, err := lockLease(ctx, table.TenantQuery(ctx, tx), l); err != nil {
		return err
	}
	result := table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", l.RunID).Updates(map[string]any{
		"state":                string(b.State),
		"status":               Blocked,
		"error":                b.Detail,
		"output":               "",
		"waiting_step":         "",
		"waiting_kind":         "",
		"waiting_external_ref": "",
		"lease_until":          nil,
		"worker_id":            "",
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	if binding.GoalID != "" {
		if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
			Where("owner_id = ? AND id = ? AND status = ?", binding.OwnerID, binding.GoalID, GoalActive).
			Updates(map[string]any{
				"status":     GoalBlocked,
				"updated_at": gorm.Expr("clock_timestamp()"),
			}).Error; err != nil {
			return err
		}
	}
	payload := map[string]any{
		"status":      Blocked,
		"output":      "",
		"detail":      b.Detail,
		"waitingStep": "",
		"waitingKind": "",
	}
	if b.Context != nil {
		payload["context"] = b.Context
	}
	if err := emit(ctx, tx, l.RunID, "RunBlocked", payload); err != nil {
		return err
	}
	return tx.Commit().Error
}

func boundaryOutcome(kind BoundaryKind) (Status, string, string) {
	switch kind {
	case BoundaryModelStarted:
		return Running, "ModelStarted", ""
	case BoundaryComplete:
		return Completed, "RunCompleted", "Final"
	case BoundaryFail:
		return Failed, "RunFailed", ""
	case BoundaryAwaitClarification:
		return WaitingClarification, "ClarificationRequested", ""
	case BoundaryAwaitReview:
		return NeedsReview, "RunNeedsReview", ""
	case BoundaryAwaitExternal:
		return WaitingExternal, "ExternalWaitRequested", ""
	default:
		return "", "", ""
	}
}

// RecordDecision commits the assistant's tool calls before any tool can execute.
func (s *Store) RecordDecision(ctx context.Context, l Lease, state json.RawMessage, message Message) error {
	if message.Role != "assistant" || len(message.ToolCalls) == 0 || !json.Valid(state) {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return err
	}
	result := table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", l.RunID).
		Updates(map[string]any{
			"state":        string(state),
			"output":       message.Content,
			"error":        "",
			"waiting_step": "",
			"waiting_kind": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	if err = appendMessage(tx, run, "Assistant", message); err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "ModelCompleted", map[string]any{
		"status":      Running,
		"output":      message.Content,
		"detail":      "",
		"waitingStep": "",
		"waitingKind": "",
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

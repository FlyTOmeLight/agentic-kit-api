package durable

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// CommitBoundary commits one legal execution boundary together with its
// message, waiting metadata and event. The boundary kind decides the status,
// message kind and event name; callers cannot choose them independently.
func (s *Store) CommitBoundary(ctx context.Context, l Lease, b Boundary) error {
	if err := b.Validate(); err != nil {
		return err
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
	if err = tx.Model(&table.Run{}).Where("id = ?", l.RunID).Updates(updates).Error; err != nil {
		return err
	}
	if b.Kind == BoundaryComplete {
		err = tx.Model(&table.Run{}).Where("id = ?", l.RunID).Where(`EXISTS (
			SELECT 1 FROM durable_goal AS goal
			JOIN durable_run_environment AS env ON env.run_id = durable_run.id
			WHERE goal.id = durable_run.goal_id AND goal.owner_id = durable_run.owner_id
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
	if err = emit(ctx, tx, l.RunID, kind, map[string]any{
		"status":      status,
		"output":      b.Output,
		"detail":      b.Detail,
		"waitingStep": b.WaitingStep,
		"waitingKind": b.Kind.Interaction(),
	}); err != nil {
		return err
	}
	if status.Terminal() {
		row := tx.Model(&table.Run{}).Where("id = ?", l.RunID)
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
	run, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	err = tx.Model(&table.Run{}).
		Where("id = ?", l.RunID).
		Updates(map[string]any{
			"state":        string(state),
			"output":       message.Content,
			"error":        "",
			"waiting_step": "",
			"waiting_kind": "",
		}).
		Error
	if err != nil {
		return err
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

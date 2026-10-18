package durable

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// SupplyResult resumes a paused step. A review result must come from a
// human-confirmed external outcome. The waiting metadata recorded at the pause
// is validated here and cleared only after the answer is committed.
func (s *Store) SupplyResult(ctx context.Context, owner, id, step, result string, kind InteractionKind) error {
	expected, messageKind := Status(""), ""
	switch kind {
	case InteractionClarification:
		expected, messageKind = WaitingClarification, "ClarificationAnswer"
	case InteractionReview:
		expected, messageKind = NeedsReview, "ReviewResult"
	default:
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readRun(
		tx.Model(&table.Run{}).
			Where("id = ? AND owner_id = ?", id, owner).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return err
	}
	var saved struct{ Result, Status string }
	err = tx.Model(&table.Step{}).
		Select("result", "status").
		Where("run_id = ? AND step_id = ?", id, step).
		Take(&saved).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if saved.Status == "Completed" {
		if saved.Result == result {
			return nil
		}
		return ErrConflict
	}
	if saved.Status != "Started" || r.Status != expected || r.WaitingStep != step || r.WaitingKind != kind {
		return ErrConflict
	}
	var expired bool
	if err = tx.Model(&table.Run{}).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", id).
		Scan(&expired).
		Error; err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if err = tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ?", id, step).
		Updates(map[string]any{"status": "Completed", "result": result}).
		Error; err != nil {
		return err
	}
	if err = tx.Model(&table.Run{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":       Queued,
			"attempt":      0,
			"available_at": gorm.Expr("clock_timestamp()"),
			"error":        "",
			"waiting_step": "",
			"waiting_kind": "",
		}).
		Error; err != nil {
		return err
	}
	if err = appendMessage(
		tx,
		r,
		messageKind,
		Message{Role: "tool", Content: result, ToolCallID: step},
	); err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunResumed", map[string]any{"stepID": step, "kind": kind}); err != nil {
		return err
	}
	return tx.Commit().Error
}

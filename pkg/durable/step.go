package durable

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// BeginStep records intent BEFORE invoking a remote tool. A repeated Unsafe
// step whose outcome is unknown returns StepUncertain so the executor pauses
// for review instead of repeating the call.
func (s *Store) BeginStep(ctx context.Context, l Lease, intent StepIntent) (StepDecision, error) {
	if err := intent.Validate(); err != nil {
		return StepDecision{}, err
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return StepDecision{}, err
	}
	defer tx.Rollback()
	if _, err := lockLease(ctx, tx, l); err != nil {
		return StepDecision{}, err
	}
	var step table.Step
	err := tx.Model(&table.Step{}).Where("run_id = ? AND step_id = ?", l.RunID, intent.ID).Take(&step).Error
	if err == nil {
		if step.InputHash != Hash([]byte(intent.Input)) || intent.Policy != step.Policy {
			return StepDecision{}, ErrConflict
		}
		if step.Status == "Completed" {
			return StepDecision{Outcome: StepReplay, Result: step.Result}, nil
		}
		if intent.Policy == Unsafe {
			return StepDecision{Outcome: StepUncertain}, nil
		}
		return StepDecision{Outcome: StepExecute}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StepDecision{}, err
	}
	err = tx.Create(
		&table.Step{
			RunID:     l.RunID,
			StepID:    intent.ID,
			InputHash: Hash([]byte(intent.Input)),
			Policy:    intent.Policy,
			Status:    "Started",
		},
	).Error
	if err != nil {
		return StepDecision{}, err
	}
	if err = emit(
		ctx,
		tx,
		l.RunID,
		"ToolStarted",
		map[string]any{"stepID": intent.ID, "policy": intent.Policy},
	); err != nil {
		return StepDecision{}, err
	}
	return StepDecision{Outcome: StepExecute}, tx.Commit().Error
}

func (s *Store) FinishStep(ctx context.Context, l Lease, id, result string, state json.RawMessage) error {
	if !json.Valid(state) {
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
	tag := tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ? AND status = ?", l.RunID, id, "Started").
		Updates(map[string]any{"status": "Completed", "result": result})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	if err = tx.Model(&table.Run{}).Where("id = ?", l.RunID).Update("state", string(state)).Error; err != nil {
		return err
	}
	if err = appendMessage(tx, run, "Tool", Message{Role: "tool", Content: result, ToolCallID: id}); err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "ToolCompleted", map[string]any{"stepID": id, "result": result}); err != nil {
		return err
	}
	return tx.Commit().Error
}

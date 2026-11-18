package durable

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// maxEvaluationOutput bounds the acceptance log kept with a receipt. The full
// output belongs in the sandbox operation record, not the control plane.
const maxEvaluationOutput = 16 << 10

// RecordEvaluation stores the worker's acceptance result for one finished run.
// Replaying the same result is idempotent; a different result for the same run
// is a conflict, because acceptance must be reproducible.
func (s *Store) RecordEvaluation(
	ctx context.Context,
	owner, goalID, runID string,
	status EvaluationStatus,
	output string,
) (*Evaluation, error) {
	return s.recordEvaluation(ctx, owner, goalID, runID, status, output, nil)
}

func (s *Store) RecordClaimedEvaluation(
	ctx context.Context,
	run *Run,
	status EvaluationStatus,
	output string,
) (*Evaluation, error) {
	if run == nil || run.EvaluationEpoch <= 0 || run.EvaluationClaimWorkerID == "" {
		return nil, ErrInvalid
	}
	return s.recordEvaluation(ctx, run.OwnerID, run.GoalID, run.ID, status, output, run)
}

func (s *Store) recordEvaluation(
	ctx context.Context,
	owner, goalID, runID string,
	status EvaluationStatus,
	output string,
	claim *Run,
) (*Evaluation, error) {
	if owner == "" || goalID == "" || runID == "" {
		return nil, ErrInvalid
	}
	switch status {
	case EvaluationPassed, EvaluationFailed, EvaluationError:
	default:
		return nil, ErrInvalid
	}
	if len(output) > maxEvaluationOutput {
		output = output[:maxEvaluationOutput]
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var goal table.Goal
	if err := table.TenantQuery(ctx, tx).
		Where("owner_id = ? AND id = ?", owner, goalID).
		Take(&goal).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var run table.Run
	if err := table.TenantQuery(ctx, tx).Where("owner_id = ? AND id = ?", owner, runID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if run.GoalID != goalID {
		return nil, ErrConflict
	}
	if claim != nil {
		if _, err := lockEvaluation(tx, claim); err != nil {
			return nil, err
		}
	}
	var existing table.Evaluation
	err := tx.Where("goal_id = ? AND run_id = ?", goalID, runID).Take(&existing).Error
	if err == nil {
		if existing.Status != status || existing.Output != output {
			return nil, ErrConflict
		}
		return evaluationFromRow(existing), tx.Commit().Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if claim == nil && run.EvaluationEpoch > 0 {
		return nil, ErrLeaseLost
	}
	row := table.Evaluation{
		ID:      NewID(),
		OwnerID: owner,
		GoalID:  goalID,
		RunID:   runID,
		Status:  status,
		Output:  output,
	}
	if err = tx.Create(&row).Error; err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, runID, "Evaluation"+string(status), map[string]any{
		"goalID": goalID,
		"status": status,
		"output": output,
	}); err != nil {
		return nil, err
	}
	return evaluationFromRow(row), tx.Commit().Error
}

// GetEvaluation returns the receipt for one run, if the worker has recorded it.
func (s *Store) GetEvaluation(ctx context.Context, owner, goalID, runID string) (*Evaluation, error) {
	var goal table.Goal
	if err := table.TenantQuery(ctx, s.db).
		Where("owner_id = ? AND id = ?", owner, goalID).Take(&goal).Error; err != nil {
		return nil, recordError(err)
	}
	var run table.Run
	if err := table.TenantQuery(ctx, s.db).
		Where("owner_id = ? AND id = ? AND goal_id = ?", owner, runID, goalID).
		Take(&run).Error; err != nil {
		return nil, recordError(err)
	}
	var row table.Evaluation
	err := s.db.WithContext(ctx).
		Where("owner_id = ? AND goal_id = ? AND run_id = ?", owner, goalID, runID).
		Take(&row).
		Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return evaluationFromRow(row), nil
}

func evaluationFromRow(r table.Evaluation) *Evaluation {
	return &Evaluation{
		ID:        r.ID,
		OwnerID:   r.OwnerID,
		GoalID:    r.GoalID,
		RunID:     r.RunID,
		Status:    r.Status,
		Output:    r.Output,
		CreatedAt: r.CreatedAt,
	}
}

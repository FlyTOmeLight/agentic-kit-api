package durable

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

const evaluationDeadlineSQL = `LEAST(durable_run.deadline, durable_run.evaluation_deadline,
	(SELECT CASE WHEN COALESCE((goal.budget->>'maxWallClockSeconds')::bigint, 0) > 0
	 THEN goal.created_at + (goal.budget->>'maxWallClockSeconds')::double precision * interval '1 second'
	 ELSE durable_run.evaluation_deadline END
	 FROM durable_goal AS goal WHERE goal.id = durable_run.goal_id AND goal.owner_id = durable_run.owner_id))`

func evaluationWork(query *gorm.DB) *gorm.DB {
	return query.Model(&table.Run{}).
		Where("durable_run.status = ? AND durable_run.evaluation_deadline IS NOT NULL", Completed).
		Where(evaluationDeadlineSQL + " > clock_timestamp()").
		Where(`EXISTS (
			SELECT 1 FROM durable_goal AS goal
			WHERE goal.id = durable_run.goal_id AND goal.owner_id = durable_run.owner_id
			  AND goal.active_run_id = durable_run.id
			  AND goal.evaluator->>'kind' = 'command'
			  AND btrim(COALESCE(goal.evaluator->>'config', '')) <> ''
			  AND (
				(goal.status = 'Active' AND NOT EXISTS (
					SELECT 1 FROM durable_evaluation AS receipt
					WHERE receipt.goal_id = goal.id AND receipt.run_id = durable_run.id))
				OR EXISTS (
					SELECT 1 FROM durable_evaluation AS receipt
					WHERE receipt.goal_id = goal.id AND receipt.run_id = durable_run.id
					  AND receipt.status IN ('Passed', 'Failed')
					  AND (durable_run.stall_mark = '' OR EXISTS (
						SELECT 1 FROM durable_run_environment AS env
						WHERE env.run_id = durable_run.id AND env.run_epoch = durable_run.epoch
						  AND env.status = 'Active')))
			  )
		)`)
}

func (s *Store) ClaimEvaluation(ctx context.Context, worker string, lease time.Duration, runID string) (*Run, error) {
	if worker == "" || lease <= 0 {
		return nil, ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	query := evaluationWork(tx).
		Where("evaluation_lease_until IS NULL OR evaluation_lease_until <= clock_timestamp()").
		Order("evaluation_deadline, created_at, id").
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	if runID != "" {
		query = query.Where("durable_run.id = ?", runID)
	}
	run, err := readRun(query)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var row table.Run
	err = tx.Model(&row).Where("id = ?", run.ID).
		Clauses(clause.Returning{}).
		Updates(map[string]any{
			"evaluation_claim_worker_id": worker,
			"evaluation_epoch":           gorm.Expr("evaluation_epoch + 1"),
			"evaluation_lease_until": gorm.Expr(
				"LEAST("+evaluationDeadlineSQL+", clock_timestamp() + ? * interval '1 millisecond')",
				lease.Milliseconds()),
			"updated_at": gorm.Expr("clock_timestamp()"),
		}).Error
	if err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, run.ID, "EvaluationClaimed", map[string]any{
		"workerID": worker, "epoch": row.EvaluationEpoch,
	}); err != nil {
		return nil, err
	}
	return runFromRow(row), tx.Commit().Error
}

func lockEvaluation(tx *gorm.DB, claim *Run) (*Run, error) {
	run, err := readRun(evaluationWork(tx).
		Where("durable_run.id = ? AND evaluation_claim_worker_id = ? AND evaluation_epoch = ? AND evaluation_lease_until > clock_timestamp()",
			claim.ID, claim.EvaluationClaimWorkerID, claim.EvaluationEpoch).
		Clauses(clause.Locking{Strength: "UPDATE"}))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseLost
	}
	return run, err
}

func (s *Store) RenewEvaluation(ctx context.Context, claim *Run, lease time.Duration) error {
	if claim == nil || lease <= 0 || claim.EvaluationEpoch <= 0 || claim.EvaluationClaimWorkerID == "" {
		return ErrInvalid
	}
	tag := evaluationWork(s.db.WithContext(ctx)).
		Where("durable_run.id = ? AND evaluation_claim_worker_id = ? AND evaluation_epoch = ? AND evaluation_lease_until > clock_timestamp()",
			claim.ID, claim.EvaluationClaimWorkerID, claim.EvaluationEpoch).
		Updates(map[string]any{
			"evaluation_lease_until": gorm.Expr(
				"LEAST("+evaluationDeadlineSQL+", clock_timestamp() + ? * interval '1 millisecond')",
				lease.Milliseconds()),
			"updated_at": gorm.Expr("clock_timestamp()"),
		})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

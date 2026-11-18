// authorization.go validates run execution claims against the durable_run
// table using the PostgreSQL clock. Every sandbox acquire, lease renewal and
// command dispatch bound to a run claim must pass this check so a worker that
// lost its claim cannot keep mutating the workspace on a cached sandbox lease.
package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// RunAuthorization is the database-clock verdict for one run claim.
type RunAuthorization struct {
	RunID     string
	OwnerID   string
	WorkerID  string
	Epoch     int64
	CheckedAt time.Time
	ExpiresAt time.Time
}

// AuthorizeRun checks whether (runID, workerID, epoch) currently holds a
// live Running claim in durable_run.
func (s *Store) AuthorizeRun(
	ctx context.Context,
	ownerID, runID, workerID string,
	epoch int64,
) (*RunAuthorization, error) {
	return authorizeRun(ctx, s.db, ownerID, runID, workerID, epoch)
}

func authorizeRun(
	ctx context.Context,
	db *gorm.DB,
	ownerID, runID, workerID string,
	epoch int64,
) (*RunAuthorization, error) {
	if runID == "" || workerID == "" || epoch <= 0 {
		return nil, fmt.Errorf("%w: runId, workerId and positive epoch required", ErrLeaseClosed)
	}
	var row struct {
		ID        string    `gorm:"column:id"`
		OwnerID   string    `gorm:"column:owner_id"`
		WorkerID  string    `gorm:"column:worker_id"`
		Epoch     int64     `gorm:"column:epoch"`
		Status    string    `gorm:"column:status"`
		Unexpired bool      `gorm:"column:unexpired"`
		CheckedAt time.Time `gorm:"column:checked_at"`
		ExpiresAt time.Time `gorm:"column:expires_at"`
	}
	err := db.WithContext(ctx).Raw(
		`SELECT id, owner_id, worker_id, epoch, status,
		        (lease_until IS NOT NULL AND lease_until > clock_timestamp() AND deadline > clock_timestamp()) AS unexpired,
		        clock_timestamp() AS checked_at,
		        LEAST(lease_until, deadline) AS expires_at
		 FROM durable_run
		 WHERE id = ?`,
		runID,
	).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, fmt.Errorf("%w: run %s not found", ErrLeaseClosed, runID)
	}
	if ownerID != "" && row.OwnerID != ownerID {
		return nil, fmt.Errorf("%w: run %s owner mismatch", ErrLeaseClosed, runID)
	}
	if row.WorkerID != workerID {
		return nil, fmt.Errorf("%w: run %s is held by another worker", ErrLeaseClosed, runID)
	}
	if row.Epoch != epoch {
		return nil, fmt.Errorf(
			"%w: run %s epoch mismatch (have %d, want %d)",
			ErrLeaseClosed,
			runID,
			epoch,
			row.Epoch,
		)
	}
	if row.Status != "Running" || !row.Unexpired {
		return nil, fmt.Errorf("%w: run %s claim is not active", ErrLeaseClosed, runID)
	}
	return &RunAuthorization{
		RunID:     row.ID,
		OwnerID:   row.OwnerID,
		WorkerID:  row.WorkerID,
		Epoch:     row.Epoch,
		CheckedAt: row.CheckedAt,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

type EvaluationClaim struct {
	WorkerID string
	Epoch    int64
}

func authorizeOperation(
	ctx context.Context,
	db *gorm.DB,
	leaseID, requestKey, kind, inputHash string,
	claims ...EvaluationClaim,
) (*Lease, error) {
	var lease Lease
	err := db.WithContext(ctx).Where(
		"id = ? AND status = ? AND hard_expires_at > clock_timestamp()", leaseID, LeaseStatusActive,
	).Take(&lease).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrLeaseClosed
	}
	if err != nil {
		return nil, err
	}
	if requestKey != "eval-"+lease.SandboxID {
		_, err = authorizeRun(ctx, db, "", lease.HolderRunID, lease.WorkerID, lease.HolderRunEpoch)
		return &lease, err
	}
	if kind != "command" || lease.HolderRunEpoch <= 0 || lease.WorkerID == "" {
		return nil, ErrLeaseClosed
	}
	if len(claims) != 1 || claims[0].WorkerID == "" || claims[0].Epoch <= 0 {
		return nil, ErrLeaseClosed
	}
	var run table.Run
	err = db.WithContext(ctx).Model(&table.Run{}).
		Select("id").
		Where("id = ? AND status = ? AND evaluation_claim_worker_id = ? AND evaluation_epoch = ? AND evaluation_lease_until > clock_timestamp()",
			lease.HolderRunID, execution.Completed, claims[0].WorkerID, claims[0].Epoch).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrLeaseClosed
	}
	if err != nil {
		return nil, err
	}
	goal, _, err := authorizeEvaluation(ctx, db, &lease)
	if err != nil {
		return nil, err
	}
	command := strings.TrimSpace(goal.Evaluator.Config)
	if payloadHashOf(command) != inputHash {
		return nil, ErrLeaseClosed
	}
	return &lease, nil
}

func authorizeEvaluation(ctx context.Context, db *gorm.DB, lease *Lease) (*table.Goal, *RunAuthorization, error) {
	if lease.HolderRunID == "" || lease.HolderRunEpoch <= 0 || lease.WorkerID == "" ||
		lease.Status != LeaseStatusActive {
		return nil, nil, ErrLeaseClosed
	}
	var row struct {
		table.Goal `gorm:"embedded"`
		ExpiresAt  time.Time
		CheckedAt  time.Time
	}
	err := db.WithContext(ctx).Model(&table.Goal{}).
		Select(`durable_goal.*, clock_timestamp() AS checked_at,
			LEAST(durable_run.deadline, durable_run.evaluation_deadline,
				CASE WHEN COALESCE((durable_goal.budget->>'maxWallClockSeconds')::bigint, 0) > 0
				THEN durable_goal.created_at +
					(durable_goal.budget->>'maxWallClockSeconds')::double precision * interval '1 second'
				ELSE durable_run.evaluation_deadline END) AS expires_at`).
		Joins("JOIN durable_run ON durable_run.goal_id = durable_goal.id AND durable_run.owner_id = durable_goal.owner_id").
		Joins("JOIN durable_run_environment AS env ON env.run_id = durable_run.id").
		Joins("JOIN sandbox ON sandbox.id = env.sandbox_id AND sandbox.owner_id = durable_run.owner_id").
		Where("durable_run.id = ? AND durable_run.epoch = ?", lease.HolderRunID, lease.HolderRunEpoch).
		Where("durable_run.status = ? AND durable_run.deadline > clock_timestamp() AND durable_run.evaluation_deadline > clock_timestamp() AND durable_run.evaluation_worker_id = ?", execution.Completed, lease.WorkerID).
		Where("durable_goal.status = ? AND durable_goal.active_run_id = durable_run.id", execution.GoalActive).
		Where("env.status = ? AND env.run_epoch = ? AND env.sandbox_id = ? AND env.lease_id = ? AND env.generation = ?",
			envStatusActive, lease.HolderRunEpoch, lease.SandboxID, lease.ID, lease.SandboxGeneration).
		Where("sandbox.status = ? AND sandbox.lease_id = ? AND sandbox.generation = ?", SandboxStatusActive, lease.ID, lease.SandboxGeneration).
		Where("? > clock_timestamp()", lease.HardExpiresAt).
		Where("NOT EXISTS (SELECT 1 FROM durable_evaluation WHERE goal_id = durable_goal.id AND run_id = durable_run.id)").
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrLeaseClosed
	}
	if err != nil {
		return nil, nil, err
	}
	if !row.ExpiresAt.After(row.CheckedAt) ||
		row.Evaluator.Kind != execution.EvaluatorCommand || strings.TrimSpace(row.Evaluator.Config) == "" {
		return nil, nil, ErrLeaseClosed
	}
	return &row.Goal, &RunAuthorization{
		RunID: lease.HolderRunID, OwnerID: row.OwnerID, WorkerID: lease.WorkerID,
		Epoch: lease.HolderRunEpoch, CheckedAt: row.CheckedAt, ExpiresAt: row.ExpiresAt,
	}, nil
}

// clampToRunAuth bounds a requested lease deadline by the authoritative run
// execution or evaluation validity window.
func clampToRunAuth(requested time.Time, auth *RunAuthorization) time.Time {
	if auth != nil && !auth.ExpiresAt.IsZero() && requested.After(auth.ExpiresAt) {
		return auth.ExpiresAt.UTC()
	}
	return requested
}

package durable

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type CodingEvaluationEnvironment interface {
	Advance(
		context.Context,
		string,
		sdk.EvaluationScope,
		sdk.CodingEvaluationScenario,
		string,
	) (*sdk.CodingEvaluationEvidence, error)
	Filesystem(string) *sdk.FilesystemBinding
}

// Reconciliation persists preparation before submitting a Run. External operations
// use the immutable Trial ID; retries cannot allocate a second environment or Run.
func (s *Store) ReconcileCodingEvaluations(ctx context.Context, environments CodingEvaluationEnvironment) error {
	var rows []table.Evaluation
	if err := s.db.WithContext(ctx).
		Where("suite->'scenario'->>'kind' = 'coding' AND status <> 'Completed' AND EXISTS (SELECT 1 FROM evaluation_trial t WHERE t.experiment_id=evaluation_experiment.id AND NOT (t.coding->>'stopped')::boolean)").
		Order("updated_at,id").
		Limit(100).
		Find(&rows).
		Error; err != nil {
		return err
	}
	for _, row := range rows {
		var trials []table.EvaluationTrial
		if err := s.db.WithContext(ctx).Where("experiment_id = ?", row.ID).Find(&trials).Error; err != nil {
			return err
		}
		for _, trial := range trials {
			if trial.Coding != nil && trial.Coding.Stopped {
				continue
			}
			phase := "prepare"
			var run *Run
			if trial.RunID != nil {
				var err error
				run, err = s.Get(ctx, row.OwnerID, *trial.RunID)
				if err != nil {
					return err
				}
				if run.Status == Completed {
					phase = "verify"
				} else if run.Status.Terminal() {
					phase = "stop"
				} else {
					phase = "wait"
				}
			}
			expired := time.Now().
				After(row.CreatedAt.Add(time.Duration(row.Suite.Scenario.TimeoutSeconds) * time.Second))
			if row.Status == "Canceled" || expired {
				phase = "stop"
				if run != nil && !run.Status.Terminal() {
					if err := s.Cancel(ctx, row.OwnerID, run.ID); err != nil {
						return err
					}
				}
			}
			if phase == "wait" {
				continue
			}
			evidence, err := environments.Advance(
				ctx,
				trial.ID,
				sdk.EvaluationScope{
					TenantID:  row.TenantID,
					ProjectID: row.ProjectID,
					ActorID:   row.ActorID,
				},
				*row.Suite.Scenario.Coding,
				phase,
			)
			if err != nil {
				return err
			}
			if evidence == nil {
				return errors.New("coding environment returned no evidence")
			}
			if phase == "stop" && row.Status != "Canceled" {
				evidence.Status = "Failed"
				if expired {
					evidence.Error = "Coding evaluation deadline exceeded"
				} else if run != nil {
					evidence.Error = run.Error
				}
			}
			err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var current table.Evaluation
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("id = ?", row.ID).
					Take(&current).
					Error; err != nil {
					return err
				}
				var t table.EvaluationTrial
				if err := tx.Where("id = ?", trial.ID).Take(&t).Error; err != nil {
					return err
				}
				if t.Coding != nil && t.Coding.Stopped {
					return nil
				}
				raw, err := json.Marshal(evidence)
				if err != nil {
					return err
				}
				changes := map[string]any{"coding": string(raw)}
				if t.RunID == nil && evidence.Status == "Ready" && current.Status != "Canceled" {
					input := ""
					for _, c := range row.Suite.Dataset.Cases {
						if c.ID == t.CaseID {
							input = c.Input
						}
					}
					query, err := json.Marshal(
						map[string]any{
							"requirements": input,
							"workspace": map[string]any{
								"path":         "/workspace/repo",
								"head":         row.Suite.Scenario.Coding.BaseHead,
								"allowedPaths": row.Suite.Scenario.Coding.AllowedPaths,
								"checks":       row.Suite.Scenario.Coding.Checks,
							},
							"mode": "evaluation",
						},
					)
					if err != nil {
						return err
					}
					spec := sdk.ExecutionSpec{
						AgentID:      row.Suite.Target.AgentID,
						AgentVersion: row.Suite.Target.AgentVersion,
						Query:        string(query),
						Filesystem:   environments.Filesystem(t.ID),
						Scope: &sdk.Scope{
							TenantID:  row.TenantID,
							ProjectID: row.ProjectID,
							ActorID:   row.ActorID,
							TaskID:    "eval:" + t.ID,
							RoundID:   "1",
						},
					}
					remaining := time.Until(
						row.CreatedAt.Add(
							time.Duration(row.Suite.Scenario.TimeoutSeconds) * time.Second,
						),
					)
					if remaining < time.Second {
						return nil
					}
					r, _, err := submitExecution(
						ctx,
						tx,
						row.OwnerID,
						"eval:"+t.ID,
						spec,
						remaining,
					)
					if err != nil {
						return err
					}
					changes["run_id"] = r.ID
				}
				if err := tx.Model(&current).
					Update("updated_at", gorm.Expr("clock_timestamp()")).
					Error; err != nil {
					return err
				}
				return tx.Model(&t).Updates(changes).Error
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// This narrow service lease allows the Coding gateway to authorize only the Run
// belonging to this coding Trial. It never grants cross-owner Run access.
func (s *Store) CheckCodingEvaluationLease(ctx context.Context, trialID, runID string, epoch int64) (bool, error) {
	var trial table.EvaluationTrial
	if err := s.db.WithContext(ctx).Where("id = ? AND run_id = ?", trialID, runID).Take(&trial).Error; err != nil {
		return false, recordError(err)
	}
	var row table.Evaluation
	if err := s.db.WithContext(ctx).Where("id = ?", trial.ExperimentID).Take(&row).Error; err != nil {
		return false, err
	}
	if row.Status == "Canceled" || row.Suite.Scenario.Kind != "coding" || trial.Coding == nil ||
		trial.Coding.Status != "Ready" {
		return false, nil
	}
	return s.CheckExecutionLease(ctx, row.OwnerID, runID, epoch)
}

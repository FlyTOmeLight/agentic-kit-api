package durable

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func (s *Store) StartEvaluation(
	ctx context.Context,
	owner, key string,
	req sdk.StartEvaluationRequest,
) (*sdk.EvaluationExperiment, bool, error) {
	if owner == "" || strings.TrimSpace(key) == "" || len(key) > 128 || req.Scope.TenantID == "" ||
		(req.Suite.Scenario.Kind != "single_turn" && req.Scope.ProjectID == "") ||
		req.Scope.ActorID == "" ||
		len(req.Suite.Dataset.Cases) > 100 ||
		len(req.Suite.Evaluators) > 20 ||
		len(req.Suite.Metrics) > 20 ||
		(req.Suite.Scenario.Filesystem != nil && len(req.Suite.Dataset.Cases) != 1) {
		return nil, false, ErrInvalid
	}
	digest, err := evaluation.Digest(req.Suite)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, false, ErrInvalid
	}
	hash := Hash(data)
	var row evaluationRecord
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
			"evaluation:"+owner+":"+key,
		).Error; err != nil {
			return err
		}
		err := tx.Where("owner_id = ? AND request_key = ?", owner, key).Take(&row).Error
		if err == nil {
			if row.RequestHash != hash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if req.Suite.Scenario.Kind == "coding" {
			var a agentRecord
			if err := tx.Where("owner_id = ? AND id = ?", owner, req.Suite.Target.AgentID).
				Take(&a).
				Error; err != nil {
				return recordError(err)
			}
			var v versionRecord
			if err := tx.Where("agent_id = ? AND number = ?", a.ID, req.Suite.Target.AgentVersion).
				Take(&v).
				Error; err != nil {
				return recordError(err)
			}
			if len(v.Config.Toolsets) != 1 || v.Config.Toolsets[0] != "filesystem" {
				return ErrInvalid
			}
		}
		row = evaluationRecord{
			ID:          NewID(),
			OwnerID:     owner,
			RequestKey:  key,
			RequestHash: hash,
			TenantID:    req.Scope.TenantID,
			ProjectID:   req.Scope.ProjectID,
			ActorID:     req.Scope.ActorID,
			Status:      "Running",
			Suite:       req.Suite,
			SuiteDigest: digest,
		}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		for _, c := range req.Suite.Dataset.Cases {
			trial := evaluationTrialRecord{ID: NewID(), ExperimentID: row.ID, CaseID: c.ID}
			if req.Suite.Scenario.Kind == "coding" {
				trial.Coding = &sdk.CodingEvaluationEvidence{
					Status:   "Preparing",
					BaseHead: req.Suite.Scenario.Coding.BaseHead,
				}
				if err = tx.Create(&trial).Error; err != nil {
					return err
				}
				continue
			}
			// Each Trial gets a separate conversation. Retries and service restarts reuse its Run.
			spec := sdk.ExecutionSpec{
				AgentID:      req.Suite.Target.AgentID,
				AgentVersion: req.Suite.Target.AgentVersion,
				Query:        c.Input,
				Filesystem:   req.Suite.Scenario.Filesystem,
				Scope: &sdk.Scope{
					TenantID:  req.Scope.TenantID,
					ProjectID: req.Scope.ProjectID,
					ActorID:   req.Scope.ActorID,
					TaskID:    "eval:" + trial.ID,
					RoundID:   "1",
				},
			}
			run, _, err := submitExecution(
				ctx,
				tx,
				owner,
				"eval:"+trial.ID,
				spec,
				time.Duration(req.Suite.Scenario.TimeoutSeconds)*time.Second,
			)
			if err != nil {
				return err
			}
			trial.RunID = &run.ID
			if err = tx.Create(&trial).Error; err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	out, err := s.Evaluation(ctx, owner, row.ID)
	return out, created, err
}

func evaluationSnapshot(tx *gorm.DB, row evaluationRecord) (*sdk.EvaluationExperiment, error) {
	out := &sdk.EvaluationExperiment{
		ID: row.ID,
		Scope: sdk.EvaluationScope{
			TenantID:  row.TenantID,
			ProjectID: row.ProjectID,
			ActorID:   row.ActorID,
		},
		Status:      row.Status,
		Suite:       row.Suite,
		SuiteDigest: row.SuiteDigest,
		Report:      row.Report,
		CreatedAt:   row.CreatedAt.UTC(),
		UpdatedAt:   row.UpdatedAt.UTC(),
		Trials:      []sdk.EvaluationTrial{},
	}
	var trials []evaluationTrialRecord
	if err := tx.Where("experiment_id = ?", row.ID).Order("case_id").Find(&trials).Error; err != nil {
		return nil, err
	}
	observations := make([]sdk.EvaluationObservation, 0, len(trials))
	allDone, attention := true, false
	for _, t := range trials {
		codingDone := t.Coding == nil ||
			(t.Coding.Stopped && (t.Coding.Status == "Completed" || t.Coding.Status == "Failed" || t.Coding.Status == "Stopped"))
		if t.RunID == nil {
			out.Trials = append(
				out.Trials,
				sdk.EvaluationTrial{ID: t.ID, CaseID: t.CaseID, Coding: t.Coding},
			)
			allDone = allDone && codingDone
			if codingDone && t.Coding != nil {
				observations = append(
					observations,
					sdk.EvaluationObservation{
						TrialID: t.ID,
						CaseID:  t.CaseID,
						Coding:  t.Coding,
						Run: sdk.Run{
							AgentID:       row.Suite.Target.AgentID,
							ConfigVersion: fmt.Sprint(row.Suite.Target.AgentVersion),
							Status:        Failed,
							Error:         t.Coding.Error,
						},
						EvidenceComplete: true,
					},
				)
			}
			continue
		}
		run, err := readRun(tx.Where("owner_id = ? AND id = ?", row.OwnerID, t.RunID))
		if err != nil {
			return nil, err
		}
		public := run.Response()
		out.Trials = append(
			out.Trials,
			sdk.EvaluationTrial{
				ID:     t.ID,
				CaseID: t.CaseID,
				RunID:  *t.RunID,
				Run:    &public,
				Coding: t.Coding,
			},
		)
		observations = append(
			observations,
			sdk.EvaluationObservation{
				TrialID:          t.ID,
				CaseID:           t.CaseID,
				Run:              public,
				Coding:           t.Coding,
				EvidenceComplete: run.Status.Terminal() && codingDone,
			},
		)
		allDone = allDone && run.Status.Terminal() && codingDone
		attention = attention || run.Status == WaitingClarification || run.Status == NeedsReview
	}
	if row.Status != "Completed" && row.Status != "Canceled" {
		var err error
		out.Report, err = evaluation.Evaluate(row.Suite, observations)
		if err != nil {
			return nil, err
		}
		out.Status = "Running"
		if attention {
			out.Status = "NeedsAttention"
		}
		if allDone {
			out.Status = "Completed"
		}
		out.Report.Passed = allDone && out.Report.Passed
	}
	return out, nil
}

func (s *Store) Evaluation(ctx context.Context, owner, id string) (*sdk.EvaluationExperiment, error) {
	var row evaluationRecord
	// A consistent read prevents mixing pre-cancel and post-cancel Trial states.
	var out *sdk.EvaluationExperiment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error; err != nil {
			return recordError(err)
		}
		var err error
		out, err = evaluationSnapshot(tx, row)
		if err == nil {
			// Completion is public only after the final report commits.
			out.Status = row.Status
			if row.Status != "Completed" && row.Status != "Canceled" {
				out.Report.Passed = false
			}
		}
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}

func (s *Store) Evaluations(
	ctx context.Context,
	owner string,
	opts sdk.ListEvaluationsOptions,
) ([]sdk.EvaluationExperiment, error) {
	if opts.Limit < 1 || opts.Limit > 100 || opts.TenantID == "" {
		return nil, ErrInvalid
	}
	var rows []evaluationRecord
	q := s.db.WithContext(ctx).Where("owner_id = ? AND tenant_id = ? AND id > ?", owner, opts.TenantID, opts.After)
	if opts.ProjectID != "" {
		q = q.Where("project_id = ?", opts.ProjectID)
	}
	if opts.AllowedProjectIDs != nil {
		q = q.Where("project_id = '' OR project_id IN ?", opts.AllowedProjectIDs)
	}
	err := q.Order("id").Limit(opts.Limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]sdk.EvaluationExperiment, 0, len(rows))
	for _, row := range rows {
		item, err := s.Evaluation(ctx, owner, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, nil
}

// ReconcileEvaluation saves scores after terminal Runs. It performs no model or
// tool calls; concurrent API instances serialize on the experiment row.
func (s *Store) ReconcileEvaluation(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row evaluationRecord
		if err := tx.Where("id = ?", id).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&row).
			Error; err != nil {
			return recordError(err)
		}
		if row.Status == "Completed" || row.Status == "Canceled" {
			return nil
		}
		out, err := evaluationSnapshot(tx, row)
		if err != nil {
			return err
		}
		data, err := json.Marshal(out.Report)
		if err != nil {
			return err
		}
		return tx.Model(&row).
			Updates(map[string]any{"status": out.Status, "report": string(data), "updated_at": gorm.Expr("clock_timestamp()")}).
			Error
	})
}

func (s *Store) CancelEvaluation(ctx context.Context, owner, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row evaluationRecord
		if err := tx.Where("owner_id = ? AND id = ?", owner, id).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&row).
			Error; err != nil {
			return recordError(err)
		}
		if row.Status == "Completed" || row.Status == "Canceled" {
			return nil
		}
		var trials []evaluationTrialRecord
		if err := tx.Where("experiment_id = ?", id).Order("run_id").Find(&trials).Error; err != nil {
			return err
		}
		canceled := false
		for _, t := range trials {
			if t.RunID == nil {
				canceled = true
				continue
			}
			changed, err := cancelRun(ctx, tx, owner, *t.RunID)
			if err != nil {
				return err
			}
			canceled = canceled || changed
		}
		out, err := evaluationSnapshot(tx, row)
		if err != nil {
			return err
		}
		data, err := json.Marshal(out.Report)
		if err != nil {
			return err
		}
		status := "Completed"
		if canceled || row.Suite.Scenario.Kind == "coding" {
			status = "Canceled"
			out.Report.Passed = false
			data, err = json.Marshal(out.Report)
			if err != nil {
				return err
			}
		}
		return tx.Model(&row).
			Updates(map[string]any{"status": status, "report": string(data), "updated_at": gorm.Expr("clock_timestamp()")}).
			Error
	})
}

// ReconcileEvaluations visits pending records in updated order, so long-running
// trials do not starve newer experiments. Database state survives process exit.
func (s *Store) ReconcileEvaluations(ctx context.Context) error {
	var rows []evaluationRecord
	if err := s.db.WithContext(ctx).
		Select("id").
		Where("status IN ?", []string{"Running", "NeedsAttention"}).
		Order("updated_at,id").
		Limit(100).
		Find(&rows).
		Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.ReconcileEvaluation(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

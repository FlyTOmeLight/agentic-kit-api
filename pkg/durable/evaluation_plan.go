package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func evaluationPlanFromRow(r table.EvaluationPlan) *sdk.EvaluationPlan {
	return &sdk.EvaluationPlan{
		ProjectID: r.ProjectID,
		ID:        r.ID,
		Revision:  r.Revision,
		Suite:     r.Suite,
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// A caller-selected ID permits retry after a lost response without duplicating a plan.
// Revision checks prevent one editor from silently overwriting another editor.
func (s *Store) SaveEvaluationPlan(
	ctx context.Context,
	owner, id string,
	req sdk.SaveEvaluationPlanRequest,
) (*sdk.EvaluationPlan, error) {
	if owner == "" || strings.TrimSpace(id) == "" || len(id) > 128 || req.Revision < 0 ||
		len(req.Suite.Dataset.Cases) > 100 ||
		len(req.Suite.Evaluators) > 20 ||
		len(req.Suite.Metrics) > 20 {
		return nil, ErrInvalid
	}
	if err := evaluation.Validate(req.Suite); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := s.AgentVersion(ctx, owner, req.Suite.Target.AgentID, req.Suite.Target.AgentVersion); err != nil {
		return nil, err
	}
	data, err := json.Marshal(req.Suite)
	if err != nil {
		return nil, ErrInvalid
	}
	var row table.EvaluationPlan
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
			"evaluation-plan:"+owner+":"+id,
		).Error; err != nil {
			return err
		}
		err := tx.Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if req.Revision != 0 {
				return ErrNotFound
			}
			row = table.EvaluationPlan{
				ID:        id,
				OwnerID:   owner,
				Revision:  1,
				Suite:     req.Suite,
				ProjectID: req.ProjectID,
			}
			return tx.Create(&row).Error
		}
		if err != nil {
			return err
		}
		if row.Revision != req.Revision {
			existing, _ := json.Marshal(row.Suite)
			if row.Revision == req.Revision+1 && string(existing) == string(data) &&
				row.ProjectID == req.ProjectID {
				return nil
			}
			return ErrConflict
		}
		return tx.Model(&row).
			Clauses(clause.Returning{}).
			Updates(map[string]any{"project_id": req.ProjectID, "suite": string(data), "revision": req.Revision + 1}).
			Error
	})
	if err != nil {
		return nil, err
	}
	return evaluationPlanFromRow(row), nil
}

func (s *Store) EvaluationPlan(ctx context.Context, owner, id string) (*sdk.EvaluationPlan, error) {
	var row table.EvaluationPlan
	if err := s.db.WithContext(ctx).Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error; err != nil {
		return nil, recordError(err)
	}
	return evaluationPlanFromRow(row), nil
}

func (s *Store) EvaluationPlans(ctx context.Context, owner, after string, limit int) ([]sdk.EvaluationPlan, error) {
	return s.WorkspaceEvaluationPlans(ctx, owner, after, limit, nil)
}

func (s *Store) WorkspaceEvaluationPlans(
	ctx context.Context,
	owner, after string,
	limit int,
	projects []string,
) ([]sdk.EvaluationPlan, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	var rows []table.EvaluationPlan
	q := s.db.WithContext(ctx).Where("owner_id = ? AND id > ?", owner, after)
	if projects != nil {
		q = q.Where("project_id = '' OR project_id IN ?", projects)
	}
	if err := q.Order("id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]sdk.EvaluationPlan, 0, len(rows))
	for _, row := range rows {
		out = append(out, *evaluationPlanFromRow(row))
	}
	return out, nil
}

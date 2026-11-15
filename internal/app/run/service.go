package run

import (
	"context"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// Service is the transactional behavior required by this HTTP module.
type Service interface {
	Get(ctx context.Context, owner, id string) (*durable.Run, error)
	Trace(ctx context.Context, owner, id string, after int64) (*durable.RunTrace, error)
	Cancel(ctx context.Context, owner, id string) error
	SupplyResult(ctx context.Context, owner, id, step, result string, kind durable.InteractionKind) error
	SupplyExternalResult(ctx context.Context, owner, id, step, externalRef, result string) error
	Events(ctx context.Context, owner, id string, after int64) ([]durable.Event, error)
	CreateGoal(ctx context.Context, goal durable.Goal) (*durable.Goal, bool, error)
	GetGoal(ctx context.Context, owner, id string) (*durable.Goal, error)
	ListGoals(ctx context.Context, owner string, status durable.GoalStatus, limit int) ([]durable.Goal, error)
	TransitionGoal(ctx context.Context, owner, id string, to durable.GoalStatus) error
	RecordEvaluation(
		ctx context.Context,
		owner, goalID, runID string,
		status durable.EvaluationStatus,
		output string,
	) (*durable.Evaluation, error)
	GetEvaluation(ctx context.Context, owner, goalID, runID string) (*durable.Evaluation, error)
	CheckGoalProgress(ctx context.Context, owner, goalID string) (*durable.Goal, error)
	SteerGoal(ctx context.Context, owner, goalID, content string) error
}

var _ Service = (*durable.Store)(nil)

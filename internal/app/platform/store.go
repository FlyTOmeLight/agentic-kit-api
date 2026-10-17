package platform

import (
	"context"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// Store is the persistence contract consumed by platform business operations.
type Store interface {
	Datasets(context.Context, string, string, int) ([]sdk.Dataset, error)
	Dataset(context.Context, string, string) (*sdk.Dataset, error)
	SaveDataset(context.Context, string, string, sdk.SaveDatasetRequest) (*sdk.Dataset, error)
	PublishDataset(context.Context, string, string, int64) (*sdk.DatasetVersion, error)
	DatasetVersion(context.Context, string, string, int64) (*sdk.DatasetVersion, error)
	WorkspaceEvaluationPlans(context.Context, string, string, int, []string) ([]sdk.EvaluationPlan, error)
	PublishCodingRelease(context.Context, string, string, string, int64, string) (*sdk.AgentVersion, error)
	SubmitExecution(context.Context, string, string, sdk.ExecutionSpec, time.Duration) (*durable.Run, bool, error)
	Get(context.Context, string, string) (*durable.Run, error)
	Cancel(context.Context, string, string) error
	SaveEvaluationPlan(context.Context, string, string, sdk.SaveEvaluationPlanRequest) (*sdk.EvaluationPlan, error)
	EvaluationPlan(context.Context, string, string) (*sdk.EvaluationPlan, error)
	EvaluationPlans(context.Context, string, string, int) ([]sdk.EvaluationPlan, error)
	Agent(context.Context, string, string) (*sdk.Agent, error)
	Agents(context.Context, string, string, int) ([]sdk.Agent, error)
	UpdateAgent(context.Context, string, string, string, int64, sdk.AgentConfig) (*sdk.Agent, error)
	PublishAgent(context.Context, string, string, int64) (*sdk.AgentVersion, error)
	AgentVersion(context.Context, string, string, int64) (*sdk.AgentVersion, error)
	StartEvaluation(
		context.Context,
		string,
		string,
		sdk.StartEvaluationRequest,
	) (*sdk.EvaluationExperiment, bool, error)
	Evaluation(context.Context, string, string) (*sdk.EvaluationExperiment, error)
	Evaluations(context.Context, string, sdk.ListEvaluationsOptions) ([]sdk.EvaluationExperiment, error)
	CancelEvaluation(context.Context, string, string) error
	Trace(context.Context, string, string, int64) (*sdk.RunTrace, error)
}

package evaluation

import (
	"context"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Service interface {
	StartEvaluation(
		context.Context,
		string,
		string,
		sdk.StartEvaluationRequest,
	) (*sdk.EvaluationExperiment, bool, error)
	Evaluation(context.Context, string, string) (*sdk.EvaluationExperiment, error)
	Evaluations(context.Context, string, sdk.ListEvaluationsOptions) ([]sdk.EvaluationExperiment, error)
	CancelEvaluation(context.Context, string, string) error
}

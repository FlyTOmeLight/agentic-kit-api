package agenticv1

import "context"

type EvaluationAPI interface {
	Start(context.Context, string, StartEvaluationRequest) (*EvaluationExperiment, bool, error)
	Get(context.Context, string) (*EvaluationExperiment, error)
	List(context.Context, ListEvaluationsOptions) (*Page[EvaluationExperiment], error)
	Cancel(context.Context, string) error
	Compare(context.Context, string, string) (*EvaluationComparison, error)
}

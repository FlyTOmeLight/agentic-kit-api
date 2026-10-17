package agenticv1

import "context"

type EvaluationAPI interface {
	Start(ctx context.Context, req *StartEvaluationRequest) (*StartEvaluationResponse, error)
	Get(ctx context.Context, req *GetEvaluationRequest) (*EvaluationExperiment, error)
	List(ctx context.Context, req *ListEvaluationsRequest) (*Page[EvaluationExperiment], error)
	Cancel(ctx context.Context, req *CancelEvaluationRequest) error
	Compare(ctx context.Context, req *CompareEvaluationsRequest) (*EvaluationComparison, error)
}

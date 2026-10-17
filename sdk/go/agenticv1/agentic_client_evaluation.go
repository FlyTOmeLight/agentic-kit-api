package agenticv1

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type evaluationClient struct{ transport *transport }

var _ EvaluationAPI = (*evaluationClient)(nil)

func (c *evaluationClient) Start(ctx context.Context, req *StartEvaluationRequest) (*StartEvaluationResponse, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("idempotency key is required")
	}
	out, status, err := doJSON[EvaluationExperiment](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/evaluations",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		http.StatusAccepted,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return &StartEvaluationResponse{EvaluationExperiment: *out, Created: status == http.StatusAccepted}, nil
}

func (c *evaluationClient) Get(ctx context.Context, req *GetEvaluationRequest) (*EvaluationExperiment, error) {
	return jsonResult[EvaluationExperiment](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/evaluations/"+url.PathEscape(req.ID),
		nil,
		200,
	)
}

func (c *evaluationClient) List(ctx context.Context, req *ListEvaluationsRequest) (*Page[EvaluationExperiment], error) {
	query := url.Values{"tenantID": {req.TenantID}, "projectID": {req.ProjectID}, "after": {req.After}}
	if req.Limit != 0 {
		query.Set("limit", strconv.Itoa(req.Limit))
	}
	return jsonResult[Page[EvaluationExperiment]](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/evaluations?"+query.Encode(),
		nil,
		200,
	)
}

func (c *evaluationClient) Cancel(ctx context.Context, req *CancelEvaluationRequest) error {
	_, _, err := doJSON[struct{}](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/evaluations/"+url.PathEscape(req.ID)+"/cancel",
		nil,
		nil,
		204,
	)
	return err
}

func (c *evaluationClient) Compare(ctx context.Context, req *CompareEvaluationsRequest) (*EvaluationComparison, error) {
	return jsonResult[EvaluationComparison](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/evaluations/"+url.PathEscape(
			req.CandidateID,
		)+"/compare?baselineID="+url.QueryEscape(
			req.BaselineID,
		),
		nil,
		200,
	)
}

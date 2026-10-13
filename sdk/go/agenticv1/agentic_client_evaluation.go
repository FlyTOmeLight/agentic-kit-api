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

func (c *evaluationClient) Start(
	ctx context.Context,
	key string,
	req StartEvaluationRequest,
) (*EvaluationExperiment, bool, error) {
	if strings.TrimSpace(key) == "" {
		return nil, false, errors.New("idempotency key is required")
	}
	out, status, err := doJSON[EvaluationExperiment](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/evaluations",
		req,
		http.Header{"Idempotency-Key": {key}},
		http.StatusAccepted,
		http.StatusOK,
	)
	return out, status == http.StatusAccepted, err
}

func (c *evaluationClient) Get(ctx context.Context, id string) (*EvaluationExperiment, error) {
	return jsonResult[EvaluationExperiment](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/evaluations/"+url.PathEscape(id),
		nil,
		200,
	)
}

func (c *evaluationClient) List(ctx context.Context, opts ListEvaluationsOptions) (*Page[EvaluationExperiment], error) {
	query := url.Values{"tenantID": {opts.TenantID}, "projectID": {opts.ProjectID}, "after": {opts.After}}
	if opts.Limit != 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
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

func (c *evaluationClient) Cancel(ctx context.Context, id string) error {
	_, _, err := doJSON[struct{}](
		ctx,
		c.transport,
		http.MethodPost,
		"/v1/evaluations/"+url.PathEscape(id)+"/cancel",
		nil,
		nil,
		204,
	)
	return err
}

func (c *evaluationClient) Compare(ctx context.Context, candidateID, baselineID string) (*EvaluationComparison, error) {
	return jsonResult[EvaluationComparison](
		ctx,
		c.transport,
		http.MethodGet,
		"/v1/evaluations/"+url.PathEscape(candidateID)+"/compare?baselineID="+url.QueryEscape(baselineID),
		nil,
		200,
	)
}

package agenticv1

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ProjectClient uses a Forge session and CSRF token. The caller must use the
// public same-origin base URL. It never accepts a runtime service credential.
type ProjectClient struct {
	transport *transport
	path      string
}

func NewProjectClient(baseURL, tenantID, projectID, session, csrf string, hc *http.Client) (*ProjectClient, error) {
	if projectID == "" {
		return nil, errors.New("project ID required")
	}
	return newAssetClient(baseURL, tenantID, projectID, session, csrf, hc)
}

// NewWorkspaceClient addresses enterprise assets without selecting a project.
func NewWorkspaceClient(baseURL, tenantID, session, csrf string, hc *http.Client) (*ProjectClient, error) {
	return newAssetClient(baseURL, tenantID, "", session, csrf, hc)
}

func newAssetClient(baseURL, tenantID, projectID, session, csrf string, hc *http.Client) (*ProjectClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") ||
		tenantID == "" ||
		session == "" ||
		csrf == "" ||
		strings.ContainsAny(csrf, "\r\n") {
		return nil, errors.New("invalid project client configuration")
	}
	cookie := &http.Cookie{Name: "forge_session", Value: session}
	if err := cookie.Valid(); err != nil {
		return nil, errors.New("invalid session cookie")
	}
	origin := u.Scheme + "://" + u.Host
	transport, err := newTransport(origin, "", hc)
	if err != nil {
		return nil, err
	}
	transport.client.SetHeader("Cookie", cookie.String()).
		SetHeader("X-CSRF-Token", csrf).
		SetHeader("Origin", origin)
	path := "/api/v1/tenants/" + url.PathEscape(tenantID)
	if projectID != "" {
		path += "/projects/" + url.PathEscape(projectID)
	}
	return &ProjectClient{transport: transport, path: path}, nil
}

func (c *ProjectClient) Agents(ctx context.Context, req *ListProjectAgentsRequest) (*ProjectAgentList, error) {
	return jsonResult[ProjectAgentList](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/agents", req.PageOptions.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) Agent(ctx context.Context, req *GetProjectAgentRequest) (*ProjectAgent, error) {
	return jsonResult[ProjectAgent](ctx, c.transport, "GET", c.path+"/agents/"+url.PathEscape(req.ID), nil, 200)
}

func (c *ProjectClient) UpdateAgent(ctx context.Context, req *UpdateProjectAgentRequest) (*ProjectAgent, error) {
	return jsonResult[ProjectAgent](ctx, c.transport, "PUT", c.path+"/agents/"+url.PathEscape(req.ID), req, 200)
}

func (c *ProjectClient) Candidate(ctx context.Context, req *PublishProjectAgentRequest) (*ProjectAgentVersion, error) {
	return jsonResult[ProjectAgentVersion](
		ctx,
		c.transport,
		"POST",
		c.path+"/agents/"+url.PathEscape(req.ID)+"/candidates",
		PublishAgentRequest{Revision: req.Revision},
		200,
	)
}

func (c *ProjectClient) Version(ctx context.Context, req *GetProjectAgentVersionRequest) (*ProjectAgentVersion, error) {
	return jsonResult[ProjectAgentVersion](
		ctx,
		c.transport,
		"GET",
		c.path+"/agents/"+url.PathEscape(req.ID)+"/versions/"+strconv.FormatInt(req.Number, 10),
		nil,
		200,
	)
}

func (c *ProjectClient) Start(
	ctx context.Context,
	req *StartProjectEvaluationRequest,
) (*StartProjectEvaluationResponse, error) {
	out, status, err := doJSON[EvaluationExperiment](
		ctx,
		c.transport,
		"POST",
		c.path+"/evaluations",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		202,
		200,
	)
	if err != nil {
		return nil, err
	}
	return &StartProjectEvaluationResponse{EvaluationExperiment: *out, Created: status == 202}, nil
}

func (c *ProjectClient) Evaluations(
	ctx context.Context,
	req *ListProjectEvaluationsRequest,
) (*Page[EvaluationExperiment], error) {
	return jsonResult[Page[EvaluationExperiment]](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/evaluations", req.PageOptions.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) Evaluation(
	ctx context.Context,
	req *GetProjectEvaluationRequest,
) (*EvaluationExperiment, error) {
	return jsonResult[EvaluationExperiment](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluations/"+url.PathEscape(req.ID),
		nil,
		200,
	)
}

func (c *ProjectClient) Cancel(ctx context.Context, req *CancelProjectEvaluationRequest) error {
	_, _, err := doJSON[struct{}](
		ctx,
		c.transport,
		"POST",
		c.path+"/evaluations/"+url.PathEscape(req.ID)+"/cancel",
		nil,
		nil,
		204,
	)
	return err
}

func (c *ProjectClient) Compare(
	ctx context.Context,
	req *CompareProjectEvaluationsRequest,
) (*EvaluationComparison, error) {
	return jsonResult[EvaluationComparison](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluations/"+url.PathEscape(
			req.CandidateID,
		)+"/compare?baselineID="+url.QueryEscape(
			req.BaselineID,
		),
		nil,
		200,
	)
}

func (c *ProjectClient) Trace(ctx context.Context, req *GetProjectTrialTraceRequest) (*RunTrace, error) {
	return jsonResult[RunTrace](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluations/"+url.PathEscape(
			req.ID,
		)+"/trials/"+url.PathEscape(
			req.TrialID,
		)+"/trace?after="+strconv.FormatInt(
			req.After,
			10,
		),
		nil,
		200,
	)
}

func (c *ProjectClient) EvaluationPlans(
	ctx context.Context,
	req *ListEvaluationPlansRequest,
) (*Page[EvaluationPlan], error) {
	return jsonResult[Page[EvaluationPlan]](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/evaluation-plans", req.PageOptions.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) EvaluationPlan(ctx context.Context, req *GetEvaluationPlanRequest) (*EvaluationPlan, error) {
	return jsonResult[EvaluationPlan](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluation-plans/"+url.PathEscape(req.ID),
		nil,
		200,
	)
}

func (c *ProjectClient) SaveEvaluationPlan(
	ctx context.Context,
	req *SaveEvaluationPlanRequest,
) (*EvaluationPlan, error) {
	return jsonResult[EvaluationPlan](
		ctx,
		c.transport,
		"PUT",
		c.path+"/evaluation-plans/"+url.PathEscape(req.ID),
		req,
		200,
	)
}

func (c *ProjectClient) StartTrial(
	ctx context.Context,
	req *StartProjectTrialRequest,
) (*StartProjectTrialResponse, error) {
	out, status, err := doJSON[ProjectTrial](
		ctx,
		c.transport,
		"POST",
		c.path+"/agents/"+url.PathEscape(req.AgentID)+"/trials",
		req,
		http.Header{"Idempotency-Key": {req.IdempotencyKey}},
		202,
		200,
	)
	if err != nil {
		return nil, err
	}
	return &StartProjectTrialResponse{ProjectTrial: *out, Created: status == 202}, nil
}

func (c *ProjectClient) Trial(ctx context.Context, req *GetProjectTrialRequest) (*ProjectTrial, error) {
	return jsonResult[ProjectTrial](
		ctx,
		c.transport,
		"GET",
		c.path+"/agents/"+url.PathEscape(req.AgentID)+"/trials/"+url.PathEscape(req.ID),
		nil,
		200,
	)
}

func (c *ProjectClient) CancelTrial(ctx context.Context, req *CancelProjectTrialRequest) error {
	_, _, err := doJSON[struct{}](
		ctx,
		c.transport,
		"POST",
		c.path+"/agents/"+url.PathEscape(req.AgentID)+"/trials/"+url.PathEscape(req.ID)+"/cancel",
		nil,
		nil,
		204,
	)
	return err
}

func (c *ProjectClient) Datasets(ctx context.Context, req *ListDatasetsRequest) (*Page[Dataset], error) {
	return jsonResult[Page[Dataset]](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/datasets", req.PageOptions.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) Dataset(ctx context.Context, req *GetDatasetRequest) (*Dataset, error) {
	return jsonResult[Dataset](ctx, c.transport, "GET", c.path+"/datasets/"+url.PathEscape(req.ID), nil, 200)
}

func (c *ProjectClient) SaveDataset(ctx context.Context, req *SaveDatasetRequest) (*Dataset, error) {
	return jsonResult[Dataset](ctx, c.transport, "PUT", c.path+"/datasets/"+url.PathEscape(req.ID), req, 200)
}

func (c *ProjectClient) PublishDataset(ctx context.Context, req *PublishDatasetRequest) (*DatasetVersion, error) {
	return jsonResult[DatasetVersion](
		ctx,
		c.transport,
		"POST",
		c.path+"/datasets/"+url.PathEscape(req.ID)+"/versions",
		PublishAgentRequest{Revision: req.Revision},
		200,
	)
}

func (c *ProjectClient) DatasetVersion(ctx context.Context, req *GetDatasetVersionRequest) (*DatasetVersion, error) {
	return jsonResult[DatasetVersion](
		ctx,
		c.transport,
		"GET",
		c.path+"/datasets/"+url.PathEscape(req.ID)+"/versions/"+strconv.FormatInt(req.Number, 10),
		nil,
		200,
	)
}

// Close releases this session client after its requests have finished.
func (c *ProjectClient) Close() error { return c.transport.close() }

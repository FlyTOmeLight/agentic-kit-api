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

func (c *ProjectClient) Agents(ctx context.Context, opts PageOptions) (*ProjectAgentList, error) {
	return jsonResult[ProjectAgentList](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/agents", opts.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) Agent(ctx context.Context, id string) (*ProjectAgent, error) {
	return jsonResult[ProjectAgent](ctx, c.transport, "GET", c.path+"/agents/"+url.PathEscape(id), nil, 200)
}

func (c *ProjectClient) CreateAgent(ctx context.Context, req CreateProjectAgentRequest) (*ProjectAgent, error) {
	return jsonResult[ProjectAgent](ctx, c.transport, "POST", c.path+"/agents", req, 201)
}

func (c *ProjectClient) UpdateAgent(
	ctx context.Context,
	id string,
	req UpdateProjectAgentRequest,
) (*ProjectAgent, error) {
	return jsonResult[ProjectAgent](ctx, c.transport, "PUT", c.path+"/agents/"+url.PathEscape(id), req, 200)
}

func (c *ProjectClient) Candidate(ctx context.Context, id string, revision int64) (*ProjectAgentVersion, error) {
	return jsonResult[ProjectAgentVersion](
		ctx,
		c.transport,
		"POST",
		c.path+"/agents/"+url.PathEscape(id)+"/candidates",
		PublishAgentRequest{Revision: revision},
		200,
	)
}

func (c *ProjectClient) Version(ctx context.Context, id string, version int64) (*ProjectAgentVersion, error) {
	return jsonResult[ProjectAgentVersion](
		ctx,
		c.transport,
		"GET",
		c.path+"/agents/"+url.PathEscape(id)+"/versions/"+strconv.FormatInt(version, 10),
		nil,
		200,
	)
}

func (c *ProjectClient) Start(
	ctx context.Context,
	key string,
	req StartProjectEvaluationRequest,
) (*EvaluationExperiment, bool, error) {
	out, status, err := doJSON[EvaluationExperiment](
		ctx,
		c.transport,
		"POST",
		c.path+"/evaluations",
		req,
		http.Header{"Idempotency-Key": {key}},
		202,
		200,
	)
	return out, status == 202, err
}

func (c *ProjectClient) Evaluations(ctx context.Context, opts PageOptions) (*Page[EvaluationExperiment], error) {
	return jsonResult[Page[EvaluationExperiment]](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/evaluations", opts.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) Evaluation(ctx context.Context, id string) (*EvaluationExperiment, error) {
	return jsonResult[EvaluationExperiment](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluations/"+url.PathEscape(id),
		nil,
		200,
	)
}

func (c *ProjectClient) Cancel(ctx context.Context, id string) error {
	_, _, err := doJSON[struct{}](
		ctx,
		c.transport,
		"POST",
		c.path+"/evaluations/"+url.PathEscape(id)+"/cancel",
		nil,
		nil,
		204,
	)
	return err
}

func (c *ProjectClient) Compare(ctx context.Context, candidate, baseline string) (*EvaluationComparison, error) {
	return jsonResult[EvaluationComparison](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluations/"+url.PathEscape(candidate)+"/compare?baselineID="+url.QueryEscape(baseline),
		nil,
		200,
	)
}

func (c *ProjectClient) Trace(ctx context.Context, id, trial string, after int64) (*RunTrace, error) {
	return jsonResult[RunTrace](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluations/"+url.PathEscape(
			id,
		)+"/trials/"+url.PathEscape(
			trial,
		)+"/trace?after="+strconv.FormatInt(
			after,
			10,
		),
		nil,
		200,
	)
}

func (c *ProjectClient) EvaluationPlans(ctx context.Context, opts PageOptions) (*Page[EvaluationPlan], error) {
	return jsonResult[Page[EvaluationPlan]](
		ctx,
		c.transport,
		"GET",
		withQuery(c.path+"/evaluation-plans", opts.query()),
		nil,
		200,
	)
}

func (c *ProjectClient) EvaluationPlan(ctx context.Context, id string) (*EvaluationPlan, error) {
	return jsonResult[EvaluationPlan](
		ctx,
		c.transport,
		"GET",
		c.path+"/evaluation-plans/"+url.PathEscape(id),
		nil,
		200,
	)
}

func (c *ProjectClient) SaveEvaluationPlan(
	ctx context.Context,
	id string,
	req SaveEvaluationPlanRequest,
) (*EvaluationPlan, error) {
	return jsonResult[EvaluationPlan](
		ctx,
		c.transport,
		"PUT",
		c.path+"/evaluation-plans/"+url.PathEscape(id),
		req,
		200,
	)
}

func (c *ProjectClient) StartTrial(
	ctx context.Context,
	agent, key string,
	req StartProjectTrialRequest,
) (*ProjectTrial, bool, error) {
	out, status, err := doJSON[ProjectTrial](
		ctx,
		c.transport,
		"POST",
		c.path+"/agents/"+url.PathEscape(agent)+"/trials",
		req,
		http.Header{"Idempotency-Key": {key}},
		202,
		200,
	)
	return out, status == 202, err
}

func (c *ProjectClient) Trial(ctx context.Context, agent, id string) (*ProjectTrial, error) {
	return jsonResult[ProjectTrial](
		ctx,
		c.transport,
		"GET",
		c.path+"/agents/"+url.PathEscape(agent)+"/trials/"+url.PathEscape(id),
		nil,
		200,
	)
}

func (c *ProjectClient) CancelTrial(ctx context.Context, agent, id string) error {
	_, _, err := doJSON[struct{}](
		ctx,
		c.transport,
		"POST",
		c.path+"/agents/"+url.PathEscape(agent)+"/trials/"+url.PathEscape(id)+"/cancel",
		nil,
		nil,
		204,
	)
	return err
}

func (c *ProjectClient) Templates(ctx context.Context) (*Page[ProjectTemplate], error) {
	return jsonResult[Page[ProjectTemplate]](ctx, c.transport, "GET", c.path+"/agent-templates", nil, 200)
}

func (c *ProjectClient) Datasets(ctx context.Context, opts PageOptions) (*Page[Dataset], error) {
	return jsonResult[Page[Dataset]](ctx, c.transport, "GET", withQuery(c.path+"/datasets", opts.query()), nil, 200)
}

func (c *ProjectClient) Dataset(ctx context.Context, id string) (*Dataset, error) {
	return jsonResult[Dataset](ctx, c.transport, "GET", c.path+"/datasets/"+url.PathEscape(id), nil, 200)
}

func (c *ProjectClient) SaveDataset(ctx context.Context, id string, req SaveDatasetRequest) (*Dataset, error) {
	return jsonResult[Dataset](ctx, c.transport, "PUT", c.path+"/datasets/"+url.PathEscape(id), req, 200)
}

func (c *ProjectClient) PublishDataset(ctx context.Context, id string, revision int64) (*DatasetVersion, error) {
	return jsonResult[DatasetVersion](
		ctx,
		c.transport,
		"POST",
		c.path+"/datasets/"+url.PathEscape(id)+"/versions",
		PublishAgentRequest{Revision: revision},
		200,
	)
}

func (c *ProjectClient) DatasetVersion(ctx context.Context, id string, n int64) (*DatasetVersion, error) {
	return jsonResult[DatasetVersion](
		ctx,
		c.transport,
		"GET",
		c.path+"/datasets/"+url.PathEscape(id)+"/versions/"+strconv.FormatInt(n, 10),
		nil,
		200,
	)
}

// Close releases this session client after its requests have finished.
func (c *ProjectClient) Close() error { return c.transport.close() }

package platform

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// Service owns platform template preparation, evaluation snapshots and release gates.
// Store keeps the shared durable contract independent of HTTP requests.
type Service struct {
	store  Store
	cfg    Config
	tenant *client.Tenant
}

func NewService(store Store, cfg Config, tenant *client.Tenant) *Service {
	return &Service{store: store, cfg: cfg, tenant: tenant}
}

func (h *Service) environmentAccess(c *core.Context, requested, kind string, write bool) error {
	legacy := c.Param("pid")
	if legacy != "" {
		if requested != "" && requested != legacy {
			return serrors.NewError(403, "ProjectMismatch", "历史受限资产只能用于原项目")
		}
		return nil
	}
	if kind == "single_turn" && requested == "" {
		return nil
	}
	if requested == "" {
		return serrors.NewError(400, "ProjectRequired", "Coding 评测需要选择有权限的仓库项目")
	}
	value, ok := c.Get("workspaceAccess")
	if ok {
		minimum := 20
		if write {
			minimum = 30
		}
		for _, p := range value.(workspaceAccess).Projects {
			if p.ID == requested && p.Level >= minimum {
				return nil
			}
		}
	}
	return serrors.NewError(403, "EnvironmentAccessRequired", "没有所选项目的评测权限")
}

func (h *Service) freezeDataset(c *core.Context, s *sdk.EvaluationSuite) error {
	if s.Dataset.ID == "" {
		if c.Param("pid") != "" {
			return nil
		}
		return serrors.NewError(400, "DatasetVersionRequired", "请选择已发布的数据集版本")
	}
	n, err := strconv.ParseInt(s.Dataset.Version, 10, 64)
	if err != nil || n < 1 {
		return serrors.NewError(400, "InvalidDatasetVersion", "数据集版本无效")
	}
	version, err := h.store.DatasetVersion(c.Request.Context(), c.GetString("projectOwner"), s.Dataset.ID, n)
	if err != nil {
		return err
	}
	s.Dataset = version.Snapshot
	return nil
}

func (h *Service) validSuite(c *core.Context, s sdk.EvaluationSuite) error {
	if s.Scenario.Filesystem != nil {
		return serrors.NewError(400, "ScenarioUnavailable", "当前项目入口支持无工具的单轮输出评测")
	}
	v, err := h.store.AgentVersion(
		c.Request.Context(),
		c.GetString("projectOwner"),
		s.Target.AgentID,
		s.Target.AgentVersion,
	)
	if err != nil {
		return err
	}
	if (writing(v.Config) && s.Scenario.Kind != "single_turn") ||
		(!writing(v.Config) && (!coding(v.Config) || h.cfg.CodingURL == "" || s.Scenario.Kind != "coding")) {
		return serrors.NewError(409, "ScenarioUnavailable", "评测场景与 Agent 工具能力不匹配")
	}
	if err := evaluation.Validate(s); err != nil {
		return serrors.NewError(400, "InvalidSuite", err.Error())
	}
	return nil
}

func (h *Service) CreateAgent(c *core.Context, req sdk.CreateProjectAgentRequest) (sdk.ProjectAgent, error) {
	for _, t := range h.cfg.Templates {
		if t.ID != req.TemplateID {
			continue
		}
		v, err := h.store.AgentVersion(c.Request.Context(), h.cfg.TemplateOwner, t.AgentID, t.Version)
		if err != nil {
			return sdk.ProjectAgent{}, err
		}
		if !writing(v.Config) && !(h.cfg.CodingURL != "" && coding(v.Config)) {
			return sdk.ProjectAgent{}, serrors.NewError(409, "ScenarioUnavailable", "该模板需要独立工具环境，暂未开放项目评测")
		}
		if coding(v.Config) {
			for i := range v.Config.Tools {
				v.Config.Tools[i].URL = strings.TrimRight(
					h.cfg.CodingURL,
					"/",
				) + "/internal/v1/evaluation-tools/" + v.Config.Tools[i].Name
				v.Config.Tools[i].TokenEnv = "CODING_TOOLS_TOKEN"
				if v.Config.Tools[i].Name == "finish" {
					v.Config.Tools[i].Description = "Submit the candidate summary and end this evaluation run. The platform captures the patch and independently verifies the frozen checks. No branch or merge request is published."
				}
			}
		}
		a, err := h.store.CreateAgent(c.Request.Context(), c.GetString("projectOwner"), req.Name, v.Config)
		if err != nil {
			return sdk.ProjectAgent{}, err
		}
		return publicAgent(a), nil
	}
	return sdk.ProjectAgent{}, serrors.NewError(400, "InvalidTemplate", "请选择已授权模板")
}

func (h *Service) UpdateAgent(c *core.Context, req sdk.UpdateProjectAgentRequest) (sdk.ProjectAgent, error) {
	a, err := h.store.Agent(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		return sdk.ProjectAgent{}, err
	}
	a.Draft.SystemPrompt = req.Prompt
	a.Draft.MaxTurns = req.MaxTurns
	updated, err := h.store.UpdateAgent(
		c.Request.Context(),
		c.GetString("projectOwner"),
		a.ID,
		req.Name,
		req.Revision,
		a.Draft,
	)
	if err != nil {
		return sdk.ProjectAgent{}, err
	}
	return publicAgent(updated), nil
}

func (h *Service) StartEvaluation(
	c *core.Context,
	req sdk.StartProjectEvaluationRequest,
) (*sdk.EvaluationExperiment, bool, error) {
	s := req.Suite
	if err := h.freezeDataset(c, &s); err != nil {
		return nil, false, err
	}
	if err := h.environmentAccess(c, req.ProjectID, s.Scenario.Kind, true); err != nil {
		return nil, false, err
	}
	if err := h.validSuite(c, s); err != nil {
		return nil, false, err
	}
	scope := sdk.EvaluationScope{
		TenantID:  c.Param("tid"),
		ProjectID: environmentProject(c, req.ProjectID),
		ActorID:   c.GetString("projectActor"),
	}
	out, created, err := h.store.StartEvaluation(
		c.Request.Context(),
		c.GetString("projectOwner"),
		req.IdempotencyKey,
		sdk.StartEvaluationRequest{Suite: s, Scope: scope},
	)
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

func (h *Service) SaveEvaluationPlan(c *core.Context, req sdk.SaveEvaluationPlanRequest) (*sdk.EvaluationPlan, error) {
	if err := h.freezeDataset(c, &req.Suite); err != nil {
		return nil, err
	}
	if err := h.validSuite(c, req.Suite); err != nil {
		return nil, err
	}
	if err := h.environmentAccess(c, req.ProjectID, req.Suite.Scenario.Kind, true); err != nil {
		return nil, err
	}
	if old, err := h.store.EvaluationPlan(
		c.Request.Context(),
		c.GetString("projectOwner"),
		req.ID,
	); err == nil {
		if err := h.environmentAccess(c, old.ProjectID, old.Suite.Scenario.Kind, true); err != nil {
			return nil, err
		}
	} else if !errors.Is(
		err,
		durable.ErrNotFound,
	) {
		return nil, err
	}
	row, err := h.store.SaveEvaluationPlan(c.Request.Context(), c.GetString("projectOwner"), req.ID, req)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (h *Service) ApproveCodingRelease(
	c *core.Context,
	req sdk.CodingReleaseRequest,
) (sdk.CodingReleaseApproval, error) {
	if auth.UserID(c) != h.cfg.TemplateOwner || h.cfg.CodingURL == "" {
		return sdk.CodingReleaseApproval{}, serrors.NewError(
			403,
			"Forbidden",
			"trusted Coding application required",
		)
	}
	var grant struct {
		Allowed bool `json:"allowed"`
		Level   int  `json:"level"`
	}
	status := h.tenant.Check(
		c.Request.Context(),
		"/internal/v1/access/check",
		map[string]any{
			"user_id":    req.ActorID,
			"tenant_id":  req.TenantID,
			"project_id": req.ProjectID,
			"action":     "write",
		},
		&grant,
	)
	if status != 200 {
		return sdk.CodingReleaseApproval{}, serrors.NewError(status, "AccessUnavailable", "无法校验项目权限")
	}
	if !grant.Allowed || grant.Level < 40 {
		return sdk.CodingReleaseApproval{}, serrors.NewError(403, "ProjectAdminRequired", "需要项目管理员权限")
	}
	owner := tenantOwner(req.TenantID)
	experiment, err := h.store.Evaluation(c.Request.Context(), owner, req.ExperimentID)
	if errors.Is(err, durable.ErrNotFound) { // Existing project-private evidence keeps its original owner.
		owner = projectOwner(req.TenantID, req.ProjectID)
		experiment, err = h.store.Evaluation(c.Request.Context(), owner, req.ExperimentID)
	}
	if err != nil {
		return sdk.CodingReleaseApproval{}, err
	}
	suite := experiment.Suite
	if experiment.Scope.TenantID != req.TenantID || experiment.Scope.ProjectID != req.ProjectID ||
		suite.Target.AgentID != req.AgentID ||
		suite.Target.AgentVersion != req.AgentVersion ||
		suite.Scenario.Kind != "coding" ||
		suite.Scenario.Coding == nil ||
		experiment.Status != "Completed" ||
		experiment.Report == nil ||
		!experiment.Report.Passed ||
		experiment.Report.SuiteDigest != experiment.SuiteDigest {
		return sdk.CodingReleaseApproval{}, serrors.NewError(
			409,
			"EvaluationGateFailed",
			"必须选择此项目、此候选已完成且通过的 Coding 实验",
		)
	}
	strict := false
	for _, metric := range suite.Metrics {
		for _, evaluator := range suite.Evaluators {
			if metric.Evaluator != evaluator.ID || evaluator.Kind != "checks_passed" ||
				evaluator.Scope != "checks" ||
				metric.MinimumScore != 1 ||
				metric.MinimumCoverage != 1 {
				continue
			}
			for _, result := range experiment.Report.Metrics {
				if result.ID == metric.ID && result.Passed && result.Value != nil &&
					*result.Value == 1 &&
					result.Coverage == 1 &&
					result.Scored == len(suite.Dataset.Cases) {
					strict = true
				}
			}
		}
	}
	if !strict {
		return sdk.CodingReleaseApproval{}, serrors.NewError(
			409,
			"ChecksGateRequired",
			"项目启用要求独立检查通过率和覆盖率门槛均为 100%",
		)
	}
	candidate, err := h.store.AgentVersion(c.Request.Context(), owner, req.AgentID, req.AgentVersion)
	if err != nil {
		return sdk.CodingReleaseApproval{}, err
	}
	if !coding(candidate.Config) {
		return sdk.CodingReleaseApproval{}, serrors.NewError(409, "CodingRequired", "只有 Coding 候选可用于项目执行")
	}
	version, err := h.store.PublishCodingRelease(
		c.Request.Context(),
		auth.UserID(c),
		owner,
		req.AgentID,
		req.AgentVersion,
		h.cfg.CodingURL,
	)
	if err != nil {
		return sdk.CodingReleaseApproval{}, err
	}
	return sdk.CodingReleaseApproval{
		ExperimentID:   experiment.ID,
		SuiteDigest:    experiment.SuiteDigest,
		AgentID:        req.AgentID,
		AgentVersion:   req.AgentVersion,
		RuntimeAgentID: version.AgentID,
		RuntimeVersion: version.Number,
		Model:          version.Config.Model.Name,
		Scenario:       *suite.Scenario.Coding,
	}, nil
}

func (h *Service) StartTrial(c *core.Context, req sdk.StartProjectTrialRequest) (*sdk.ProjectTrial, bool, error) {
	key := req.IdempotencyKey
	if strings.TrimSpace(key) == "" || len(key) > 100 || strings.TrimSpace(req.Input) == "" {
		return nil, false, serrors.NewError(400, "InvalidTrial", "请输入试运行内容，超时应为 1–3600 秒")
	}
	owner := c.GetString("projectOwner")
	v, err := h.store.AgentVersion(c.Request.Context(), owner, req.AgentID, req.AgentVersion)
	if err != nil {
		return nil, false, err
	}
	if !writing(v.Config) {
		return nil, false, serrors.NewError(409, "ScenarioUnavailable", "该版本需要独立工具环境，暂不支持直接试运行")
	}
	spec := sdk.ExecutionSpec{AgentID: v.AgentID, AgentVersion: v.Number, Query: req.Input, Scope: &sdk.Scope{
		TenantID:  c.Param("tid"),
		ProjectID: c.Param("pid"),
		ActorID:   c.GetString("projectActor"),
		TaskID:    "try:" + durable.Hash([]byte(key)),
		RoundID:   "1",
	}}
	run, created, err := h.store.SubmitExecution(
		c.Request.Context(),
		owner,
		"try:"+key,
		spec,
		time.Duration(req.TimeoutSeconds)*time.Second,
	)
	if err != nil {
		return nil, false, err
	}
	return &sdk.ProjectTrial{Run: run.Response(), Input: run.Spec.Query}, created, nil
}

func (h *Service) ReadTrial(c *core.Context, req sdk.GetProjectTrialRequest) (*durable.Run, error) {
	run, err := h.store.Get(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		return nil, err
	}
	if run.Spec.AgentID != req.AgentID || run.Spec.Scope == nil ||
		!strings.HasPrefix(run.Spec.Scope.TaskID, "try:") {
		return nil, durable.ErrNotFound
	}
	return run, nil
}

func (h *Service) Templates(c *core.Context) ([]sdk.ProjectTemplate, error) {
	out := []sdk.ProjectTemplate{}
	for _, t := range h.cfg.Templates {
		v, err := h.store.AgentVersion(c.Request.Context(), h.cfg.TemplateOwner, t.AgentID, t.Version)
		if err != nil {
			return nil, serrors.NewError(503, "TemplateUnavailable", "受信任模板暂不可用")
		}
		if writing(v.Config) || (h.cfg.CodingURL != "" && coding(v.Config)) {
			out = append(
				out,
				sdk.ProjectTemplate{
					Kind:     agentKind(v.Config),
					ID:       t.ID,
					Name:     t.Name,
					Model:    v.Config.Model.Name,
					Version:  t.Version,
					Prompt:   v.Config.SystemPrompt,
					MaxTurns: v.Config.MaxTurns,
					Tools:    configuredTools(v.Config),
				},
			)
		}
	}
	return out, nil
}

func (h *Service) CompareEvaluations(
	c *core.Context,
	req sdk.CompareProjectEvaluationsRequest,
) (*sdk.EvaluationComparison, error) {
	candidate, err := h.store.Evaluation(c.Request.Context(), c.GetString("projectOwner"), req.CandidateID)
	if err != nil {
		return nil, err
	}
	baseline, err := h.store.Evaluation(c.Request.Context(), c.GetString("projectOwner"), req.BaselineID)
	if err != nil {
		return nil, err
	}
	if err := h.environmentAccess(c, candidate.Scope.ProjectID, candidate.Suite.Scenario.Kind, false); err != nil {
		return nil, err
	}
	if err := h.environmentAccess(c, baseline.Scope.ProjectID, baseline.Suite.Scenario.Kind, false); err != nil {
		return nil, err
	}
	out, err := evaluation.Compare(*baseline, *candidate)
	if err != nil {
		return nil, serrors.NewError(409, "IncompatibleExperiments", err.Error())
	}
	return out, nil
}

func (h *Service) TrialTrace(c *core.Context, req sdk.GetProjectTrialTraceRequest) (*sdk.RunTrace, error) {
	experiment, err := h.store.Evaluation(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		return nil, err
	}
	if err := h.environmentAccess(
		c,
		experiment.Scope.ProjectID,
		experiment.Suite.Scenario.Kind,
		false,
	); err != nil {
		return nil, err
	}
	for _, trial := range experiment.Trials {
		if trial.ID == req.TrialID {
			out, err := h.store.Trace(
				c.Request.Context(),
				c.GetString("projectOwner"),
				trial.RunID,
				req.After,
			)
			if err != nil {
				return nil, err
			}
			return out, nil
		}
	}
	return nil, durable.ErrNotFound
}

func (h *Service) CancelEvaluation(c *core.Context, req sdk.CancelProjectEvaluationRequest) error {
	e, err := h.store.Evaluation(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		return err
	}
	if err := h.environmentAccess(c, e.Scope.ProjectID, e.Suite.Scenario.Kind, true); err != nil {
		return err
	}
	if err := h.store.CancelEvaluation(
		c.Request.Context(),
		c.GetString("projectOwner"),
		req.ID,
	); err != nil {
		return err
	}
	return nil
}

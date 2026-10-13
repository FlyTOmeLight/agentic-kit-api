package platform

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func configuredTools(config sdk.AgentConfig) []string {
	tools := append([]string{}, config.Toolsets...)
	for _, t := range config.Tools {
		tools = append(tools, t.Name)
	}
	return tools
}

func publicAgent(a *sdk.Agent) sdk.ProjectAgent {
	return sdk.ProjectAgent{
		Kind:             agentKind(a.Draft),
		Tools:            configuredTools(a.Draft),
		ID:               a.ID,
		Name:             a.Name,
		Revision:         a.Revision,
		CandidateVersion: a.PublishedVersion,
		Prompt:           a.Draft.SystemPrompt,
		MaxTurns:         a.Draft.MaxTurns,
		Model:            a.Draft.Model.Name,
	}
}

func agentKind(c sdk.AgentConfig) string {
	if coding(c) {
		return "Coding"
	}
	return "Writing"
}

func coding(c sdk.AgentConfig) bool {
	if len(c.Toolsets) != 1 || c.Toolsets[0] != "filesystem" || len(c.Tools) != 2 {
		return false
	}
	finish, checks := false, false
	for _, t := range c.Tools {
		if t.Kind != "" && t.Kind != "HTTP" {
			return false
		}
		if t.Name == "finish" && t.EndRun {
			finish = true
		}
		if t.Name == "run_checks" {
			checks = true
		}
	}
	return finish && checks
}

func writing(c sdk.AgentConfig) bool { return len(c.Tools) == 0 && len(c.Toolsets) == 0 }

func (h *controller) agents(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	after := c.Query("after")
	if !ok {
		return
	}
	items, err := h.service.Agents(c.Request.Context(), c.GetString("projectOwner"), after, limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	out := sdk.ProjectAgentList{
		Items:     []sdk.ProjectAgent{},
		Templates: []sdk.ProjectTemplate{},
		CanEdit:   c.GetBool("projectCanEdit"),
	}
	for i := range items {
		out.Items = append(out.Items, publicAgent(&items[i]))
	}
	if c.Param("pid") != "" { // Historical project clients retain their original contract.
		var ok bool
		out.Templates, ok = h.templateList(c)
		if !ok {
			return
		}
	}

	c.JSON(200, out)
}

func (h *controller) templateList(c *gin.Context) ([]sdk.ProjectTemplate, bool) {
	out := sdk.ProjectAgentList{Templates: []sdk.ProjectTemplate{}}
	for _, t := range h.cfg.Templates {
		v, err := h.service.AgentVersion(c.Request.Context(), h.cfg.TemplateOwner, t.AgentID, t.Version)
		if err != nil {
			serrors.Abort(c, 503, "TemplateUnavailable", "受信任模板暂不可用")
			return nil, false
		}
		if writing(v.Config) || (h.cfg.CodingURL != "" && coding(v.Config)) {
			out.Templates = append(
				out.Templates,
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
	return out.Templates, true
}

func (h *controller) templates(c *gin.Context) {
	if items, ok := h.templateList(c); ok {
		c.JSON(200, sdk.Page[sdk.ProjectTemplate]{Items: items})
	}
}

func (h *controller) createAgent(c *gin.Context) {
	var req sdk.CreateProjectAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	for _, t := range h.cfg.Templates {
		if t.ID != req.TemplateID {
			continue
		}
		v, err := h.service.AgentVersion(c.Request.Context(), h.cfg.TemplateOwner, t.AgentID, t.Version)
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		if !writing(v.Config) && !(h.cfg.CodingURL != "" && coding(v.Config)) {
			serrors.Abort(c, 409, "ScenarioUnavailable", "该模板需要独立工具环境，暂未开放项目评测")
			return
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
		a, err := h.service.CreateAgent(c.Request.Context(), c.GetString("projectOwner"), req.Name, v.Config)
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		c.JSON(201, publicAgent(a))
		return
	}
	serrors.Abort(c, 400, "InvalidTemplate", "请选择已授权模板")
}

func (h *controller) agent(c *gin.Context) {
	a, err := h.service.Agent(c.Request.Context(), c.GetString("projectOwner"), c.Param("agent"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, publicAgent(a))
}

func (h *controller) updateAgent(c *gin.Context) {
	var req sdk.UpdateProjectAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	a, err := h.service.Agent(c.Request.Context(), c.GetString("projectOwner"), c.Param("agent"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	a.Draft.SystemPrompt = req.Prompt
	a.Draft.MaxTurns = req.MaxTurns
	updated, err := h.service.UpdateAgent(
		c.Request.Context(),
		c.GetString("projectOwner"),
		a.ID,
		req.Name,
		req.Revision,
		a.Draft,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, publicAgent(updated))
}

func (h *controller) candidate(c *gin.Context) {
	var req sdk.PublishAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	v, err := h.service.PublishAgent(
		c.Request.Context(),
		c.GetString("projectOwner"),
		c.Param("agent"),
		req.Revision,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, publicVersion(v))
}

func publicVersion(v *sdk.AgentVersion) sdk.ProjectAgentVersion {
	return sdk.ProjectAgentVersion{
		AgentID:  v.AgentID,
		Number:   v.Number,
		Prompt:   v.Config.SystemPrompt,
		Model:    v.Config.Model.Name,
		MaxTurns: v.Config.MaxTurns,
	}
}

func (h *controller) version(c *gin.Context) {
	n, err := strconv.ParseInt(c.Param("version"), 10, 64)
	if err != nil || n < 1 {
		apperror.Respond(c, durable.ErrInvalid)
		return
	}
	v, err := h.service.AgentVersion(c.Request.Context(), c.GetString("projectOwner"), c.Param("agent"), n)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, publicVersion(v))
}

func (h *controller) start(c *gin.Context) {
	var req sdk.StartProjectEvaluationRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	s := req.Suite
	if !h.freezeDataset(c, &s) || !h.environmentAccess(c, req.ProjectID, s.Scenario.Kind, true) {
		return
	}
	if !h.validSuite(c, s) {
		return
	}
	scope := sdk.EvaluationScope{
		TenantID:  c.Param("tid"),
		ProjectID: h.environmentProject(c, req.ProjectID),
		ActorID:   c.GetString("projectActor"),
	}
	out, created, err := h.service.StartEvaluation(
		c.Request.Context(),
		c.GetString("projectOwner"),
		c.GetHeader("Idempotency-Key"),
		sdk.StartEvaluationRequest{Suite: s, Scope: scope},
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, out)
}

func (h *controller) evaluations(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	after := c.Query("after")
	if !ok {
		return
	}
	items, err := h.service.Evaluations(
		c.Request.Context(),
		c.GetString("projectOwner"),
		sdk.ListEvaluationsOptions{
			PageOptions:       sdk.PageOptions{After: after, Limit: limit},
			TenantID:          c.Param("tid"),
			ProjectID:         c.Param("pid"),
			AllowedProjectIDs: allowedProjects(c),
		},
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.EvaluationExperiment]{Items: items})
}

func (h *controller) experiment(c *gin.Context) {
	out, err := h.service.Evaluation(c.Request.Context(), c.GetString("projectOwner"), c.Param("experiment"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !h.environmentAccess(c, out.Scope.ProjectID, out.Suite.Scenario.Kind, false) {
		return
	}
	c.JSON(200, out)
}

func (h *controller) cancel(c *gin.Context) {
	e, err := h.service.Evaluation(c.Request.Context(), c.GetString("projectOwner"), c.Param("experiment"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !h.environmentAccess(c, e.Scope.ProjectID, e.Suite.Scenario.Kind, true) {
		return
	}
	if err := h.service.CancelEvaluation(
		c.Request.Context(),
		c.GetString("projectOwner"),
		c.Param("experiment"),
	); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(204)
}

func (h *controller) compare(c *gin.Context) {
	candidate, err := h.service.Evaluation(c.Request.Context(), c.GetString("projectOwner"), c.Param("experiment"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	baseline, err := h.service.Evaluation(c.Request.Context(), c.GetString("projectOwner"), c.Query("baselineID"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !h.environmentAccess(c, candidate.Scope.ProjectID, candidate.Suite.Scenario.Kind, false) ||
		!h.environmentAccess(c, baseline.Scope.ProjectID, baseline.Suite.Scenario.Kind, false) {
		return
	}
	out, err := evaluation.Compare(*baseline, *candidate)
	if err != nil {
		serrors.Abort(c, 409, "IncompatibleExperiments", err.Error())
		return
	}
	c.JSON(200, out)
}

func (h *controller) trace(c *gin.Context) {
	experiment, err := h.service.Evaluation(c.Request.Context(), c.GetString("projectOwner"), c.Param("experiment"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	after := int64(0)
	if raw := c.Query("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			apperror.Respond(c, durable.ErrInvalid)
			return
		}
	}
	if !h.environmentAccess(c, experiment.Scope.ProjectID, experiment.Suite.Scenario.Kind, false) {
		return
	}
	for _, trial := range experiment.Trials {
		if trial.ID == c.Param("trial") {
			out, err := h.service.Trace(
				c.Request.Context(),
				c.GetString("projectOwner"),
				trial.RunID,
				after,
			)
			if err != nil {
				apperror.Respond(c, err)
				return
			}
			c.JSON(200, out)
			return
		}
	}
	apperror.Respond(c, durable.ErrNotFound)
}

func (h *controller) validSuite(c *gin.Context, s sdk.EvaluationSuite) bool {
	if s.Scenario.Filesystem != nil {
		serrors.Abort(c, 400, "ScenarioUnavailable", "当前项目入口支持无工具的单轮输出评测")
		return false
	}
	v, err := h.service.AgentVersion(
		c.Request.Context(),
		c.GetString("projectOwner"),
		s.Target.AgentID,
		s.Target.AgentVersion,
	)
	if err != nil {
		apperror.Respond(c, err)
		return false
	}
	if (writing(v.Config) && s.Scenario.Kind != "single_turn") ||
		(!writing(v.Config) && (!coding(v.Config) || h.cfg.CodingURL == "" || s.Scenario.Kind != "coding")) {
		serrors.Abort(c, 409, "ScenarioUnavailable", "评测场景与 Agent 工具能力不匹配")
		return false
	}
	if err := evaluation.Validate(s); err != nil {
		serrors.Abort(c, 400, "InvalidSuite", err.Error())
		return false
	}
	return true
}

func (h *controller) plans(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	if !ok {
		return
	}
	rows, err := h.service.WorkspaceEvaluationPlans(
		c.Request.Context(),
		c.GetString("projectOwner"),
		c.Query("after"),
		limit,
		allowedProjects(c),
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.EvaluationPlan]{Items: rows})
}

func (h *controller) plan(c *gin.Context) {
	row, err := h.service.EvaluationPlan(c.Request.Context(), c.GetString("projectOwner"), c.Param("plan"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !h.environmentAccess(c, row.ProjectID, row.Suite.Scenario.Kind, false) {
		return
	}
	c.JSON(200, row)
}

func (h *controller) savePlan(c *gin.Context) {
	var req sdk.SaveEvaluationPlanRequest
	if !binder.BindJSON(c, &req) || !h.freezeDataset(c, &req.Suite) || !h.validSuite(c, req.Suite) ||
		!h.environmentAccess(c, req.ProjectID, req.Suite.Scenario.Kind, true) {
		return
	}
	if old, err := h.service.EvaluationPlan(
		c.Request.Context(),
		c.GetString("projectOwner"),
		c.Param("plan"),
	); err == nil {
		if !h.environmentAccess(c, old.ProjectID, old.Suite.Scenario.Kind, true) {
			return
		}
	} else if !errors.Is(
		err,
		durable.ErrNotFound,
	) {
		apperror.Respond(c, err)
		return
	}
	row, err := h.service.SaveEvaluationPlan(c.Request.Context(), c.GetString("projectOwner"), c.Param("plan"), req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

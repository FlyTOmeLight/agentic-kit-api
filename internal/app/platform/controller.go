package platform

import (
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
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

// agents 列出企业 Agent。
// @Summary 列出企业 Agent
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param pid path string false "项目路径下的项目 ID"
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Success 200 {object} agenticv1.ProjectAgentList "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents [get]
func (h *Controller) agents(c *core.Context) {
	var page sdk.PageOptions
	if err := binder.Bind(c, &page).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	items, err := h.store.Agents(c.Request.Context(), c.GetString("projectOwner"), page.After, page.Limit)
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

func (h *Controller) templateList(c *core.Context) ([]sdk.ProjectTemplate, bool) {
	templates, err := h.business.Templates(c)
	if err != nil {
		apperror.Respond(c, err)
		return nil, false
	}
	return templates, true
}

// templates 列出受信任 Agent 模板。
// @Summary 列出受信任 Agent 模板
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Success 200 {object} agenticv1.Page[agenticv1.ProjectTemplate] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agent-templates [get]
func (h *Controller) templates(c *core.Context) {
	if items, ok := h.templateList(c); ok {
		c.JSON(200, sdk.Page[sdk.ProjectTemplate]{Items: items})
	}
}

// createAgent 从模板创建 Agent。
// @Summary 从模板创建 Agent
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param pid path string false "项目路径下的项目 ID"
// @Param request body agenticv1.CreateProjectAgentRequest true "JSON 请求"
// @Success 201 {object} agenticv1.ProjectAgent "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents [post]
func (h *Controller) createAgent(c *core.Context) {
	var req sdk.CreateProjectAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	out, err := h.business.CreateAgent(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(201, out)
}

// agent 查询企业 Agent。
// @Summary 查询企业 Agent
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.ProjectAgent "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent} [get]
func (h *Controller) agent(c *core.Context) {
	var req sdk.GetProjectAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	a, err := h.store.Agent(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, publicAgent(a))
}

// updateAgent 更新企业 Agent 草稿。
// @Summary 更新企业 Agent 草稿
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param pid path string false "项目路径下的项目 ID"
// @Param request body agenticv1.UpdateProjectAgentRequest true "JSON 请求"
// @Success 200 {object} agenticv1.ProjectAgent "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent} [put]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent} [put]
func (h *Controller) updateAgent(c *core.Context) {
	var req sdk.UpdateProjectAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	out, err := h.business.UpdateAgent(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

// candidate 发布候选版本。
// @Summary 发布候选版本
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param pid path string false "项目路径下的项目 ID"
// @Param request body agenticv1.PublishAgentRequest true "JSON 请求"
// @Success 200 {object} agenticv1.ProjectAgentVersion "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent}/candidates [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent}/candidates [post]
func (h *Controller) candidate(c *core.Context) {
	var req sdk.PublishProjectAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	v, err := h.store.PublishAgent(
		c.Request.Context(),
		c.GetString("projectOwner"),
		req.ID,
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

// version 查询候选版本。
// @Summary 查询候选版本
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param version path int true "version" minimum(1)
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.ProjectAgentVersion "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent}/versions/{version} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent}/versions/{version} [get]
func (h *Controller) version(c *core.Context) {
	var req sdk.GetProjectAgentVersionRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	v, err := h.store.AgentVersion(c.Request.Context(), c.GetString("projectOwner"), req.ID, req.Number)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, publicVersion(v))
}

// start 启动企业评测。
// @Summary 启动企业评测
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param pid path string false "项目路径下的项目 ID"
// @Param Idempotency-Key header string true "幂等标识；相同请求重放返回已有任务" maxlength(128)
// @Param request body agenticv1.StartProjectEvaluationRequest true "JSON 请求"
// @Success 202 {object} agenticv1.EvaluationExperiment "成功"
// @Success 200 {object} agenticv1.EvaluationExperiment "幂等重放"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluations [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluations [post]
func (h *Controller) start(c *core.Context) {
	var req sdk.StartProjectEvaluationRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	var headers struct {
		IdempotencyKey string `header:"Idempotency-Key" binding:"required,max=128"`
	}
	if err := binder.Bind(c, &headers).Header().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	req.IdempotencyKey = headers.IdempotencyKey

	out, created, err := h.business.StartEvaluation(c, req)
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

// evaluations 列出企业评测。
// @Summary 列出企业评测
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param pid path string false "项目路径下的项目 ID"
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Success 200 {object} agenticv1.Page[agenticv1.EvaluationExperiment] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluations [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluations [get]
func (h *Controller) evaluations(c *core.Context) {
	var page sdk.PageOptions
	if err := binder.Bind(c, &page).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	items, err := h.store.Evaluations(
		c.Request.Context(),
		c.GetString("projectOwner"),
		sdk.ListEvaluationsOptions{
			PageOptions:       sdk.PageOptions{After: page.After, Limit: page.Limit},
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

// experiment 查询企业评测。
// @Summary 查询企业评测
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param experiment path string true "experiment"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.EvaluationExperiment "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluations/{experiment} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluations/{experiment} [get]
func (h *Controller) experiment(c *core.Context) {
	var req sdk.GetProjectEvaluationRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	out, err := h.store.Evaluation(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !h.environmentAccess(c, out.Scope.ProjectID, out.Suite.Scenario.Kind, false) {
		return
	}
	c.JSON(200, out)
}

// cancel 取消企业评测。
// @Summary 取消企业评测
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param experiment path string true "experiment"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluations/{experiment}/cancel [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluations/{experiment}/cancel [post]
func (h *Controller) cancel(c *core.Context) {
	var req sdk.CancelProjectEvaluationRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	if err := h.business.CancelEvaluation(c, req); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(204)
}

// compare 比较企业评测。
// @Summary 比较企业评测
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param experiment path string true "experiment"
// @Param pid path string false "项目路径下的项目 ID"
// @Param baselineID query string true "基线实验 ID"
// @Success 200 {object} agenticv1.EvaluationComparison "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluations/{experiment}/compare [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluations/{experiment}/compare [get]
func (h *Controller) compare(c *core.Context) {
	var req sdk.CompareProjectEvaluationsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	out, err := h.business.CompareEvaluations(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

// trace 查询评测试验轨迹。
// @Summary 查询评测试验轨迹
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param experiment path string true "experiment"
// @Param trial path string true "trial"
// @Param pid path string false "项目路径下的项目 ID"
// @Param after query int false "事件序号游标" default(0) minimum(0)
// @Success 200 {object} agenticv1.RunTrace "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluations/{experiment}/trials/{trial}/trace [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluations/{experiment}/trials/{trial}/trace [get]
func (h *Controller) trace(c *core.Context) {
	var req sdk.GetProjectTrialTraceRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	out, err := h.business.TrialTrace(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

// plans 列出评测计划。
// @Summary 列出评测计划
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param pid path string false "项目路径下的项目 ID"
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Success 200 {object} agenticv1.Page[agenticv1.EvaluationPlan] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluation-plans [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluation-plans [get]
func (h *Controller) plans(c *core.Context) {
	var page sdk.PageOptions
	if err := binder.Bind(c, &page).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	rows, err := h.store.WorkspaceEvaluationPlans(
		c.Request.Context(),
		c.GetString("projectOwner"),
		page.After,
		page.Limit,
		allowedProjects(c),
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.EvaluationPlan]{Items: rows})
}

// plan 查询评测计划。
// @Summary 查询评测计划
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param plan path string true "plan"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.EvaluationPlan "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluation-plans/{plan} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluation-plans/{plan} [get]
func (h *Controller) plan(c *core.Context) {
	var req sdk.GetEvaluationPlanRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	row, err := h.store.EvaluationPlan(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if !h.environmentAccess(c, row.ProjectID, row.Suite.Scenario.Kind, false) {
		return
	}
	c.JSON(200, row)
}

// savePlan 保存评测计划。
// @Summary 保存评测计划
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param plan path string true "plan"
// @Param pid path string false "项目路径下的项目 ID"
// @Param request body agenticv1.SaveEvaluationPlanRequest true "JSON 请求"
// @Success 200 {object} agenticv1.EvaluationPlan "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/evaluation-plans/{plan} [put]
// @Router /api/v1/tenants/{tid}/projects/{pid}/evaluation-plans/{plan} [put]
func (h *Controller) savePlan(c *core.Context) {
	var req sdk.SaveEvaluationPlanRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	out, err := h.business.SaveEvaluationPlan(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

package agent

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// createAgent 创建 Agent。
// @Summary 创建 Agent
// @Tags Agent
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body agenticv1.CreateAgentRequest true "JSON 请求"
// @Success 201 {object} agenticv1.Agent "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/agents [post]
func (h *Controller) createAgent(c *core.Context) {
	var req sdk.CreateAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	a, err := h.service.CreateAgent(c.Request.Context(), auth.UserID(c), req.Name, req.Config)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

// getAgent 查询 Agent。
// @Summary 查询 Agent
// @Tags Agent
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 200 {object} agenticv1.Agent "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/agents/{id} [get]
func (h *Controller) getAgent(c *core.Context) {
	var req sdk.GetAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	a, err := h.service.Agent(c.Request.Context(), auth.UserID(c), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// updateAgent 更新 Agent 草稿。
// @Summary 更新 Agent 草稿
// @Tags Agent
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param request body agenticv1.UpdateAgentRequest true "JSON 请求"
// @Success 200 {object} agenticv1.Agent "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/agents/{id} [put]
func (h *Controller) updateAgent(c *core.Context) {
	var req sdk.UpdateAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	a, err := h.service.UpdateAgent(
		c.Request.Context(),
		auth.UserID(c),
		req.ID,
		req.Name,
		req.Revision,
		req.Config,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// publishAgent 发布不可变 Agent 版本。
// @Summary 发布不可变 Agent 版本
// @Tags Agent
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param request body agenticv1.PublishAgentRequest true "JSON 请求"
// @Success 200 {object} agenticv1.AgentVersion "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/agents/{id}/publish [post]
func (h *Controller) publishAgent(c *core.Context) {
	var req sdk.PublishAgentRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	v, err := h.service.PublishAgent(c.Request.Context(), auth.UserID(c), req.ID, req.Revision)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// getVersion 查询 Agent 版本。
// @Summary 查询 Agent 版本
// @Tags Agent
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param number path int true "number" minimum(1)
// @Success 200 {object} agenticv1.AgentVersion "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/agents/{id}/versions/{number} [get]
func (h *Controller) getVersion(c *core.Context) {
	var req sdk.GetAgentVersionRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	v, err := h.service.AgentVersion(c.Request.Context(), auth.UserID(c), req.ID, req.Number)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

// listAgents 列出 Agent。
// @Summary 列出 Agent
// @Tags Agent
// @Produce json
// @Security BearerAuth
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Success 200 {object} agenticv1.Page[agenticv1.Agent] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/agents [get]
func (h *Controller) listAgents(c *core.Context) {
	var req sdk.ListAgentsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	items, err := h.service.Agents(c.Request.Context(), auth.UserID(c), req.After, req.Limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Agent]{Items: items})
}

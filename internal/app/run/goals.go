package run

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// RegisterGoalRoutes registers goal management endpoints under /internal/v1.
// Goals are internal control-plane objects; they are not part of the public
// v1 contract until the product boundary is decided.
func (h *Controller) RegisterGoalRoutes(router core.Router) {
	group := router.Group("/goals")
	group.POST("", h.createGoal)
	group.GET("/:id", h.getGoal)
	group.GET("", h.listGoals)
	group.POST("/:id/pause", h.pauseGoal)
	group.POST("/:id/resume", h.resumeGoal)
	group.POST("/:id/steer", h.steerGoal)
	group.POST("/:id/evaluations", h.recordEvaluation)
	group.GET("/:id/evaluations/:runID", h.getEvaluation)
}

type createGoalRequest struct {
	SessionID   string                  `json:"sessionID" binding:"required,max=128"`
	RequestKey  string                  `json:"requestKey" binding:"required,max=128"`
	RequestHash string                  `json:"requestHash" binding:"required,max=128"`
	Objective   string                  `json:"objective" binding:"required"`
	Evaluator   execution.EvaluatorSpec `json:"evaluator"`
	Budget      execution.BudgetSpec    `json:"budget"`
}

// createGoal creates or replays a goal.
// @Summary 创建或重放 Goal
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createGoalRequest true "JSON 请求"
// @Success 200 {object} execution.Goal "幂等重放"
// @Success 201 {object} execution.Goal "创建成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 409 {object} serrors.Error "幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals [post]
func (h *Controller) createGoal(c *core.Context) {
	var req createGoalRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	goal := durable.Goal{
		OwnerID:     auth.UserID(c),
		SessionID:   req.SessionID,
		RequestKey:  req.RequestKey,
		RequestHash: req.RequestHash,
		Objective:   req.Objective,
		Evaluator:   req.Evaluator,
		Budget:      req.Budget,
	}
	created, ok, err := h.service.CreateGoal(c.Request.Context(), goal)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := http.StatusOK
	if ok {
		status = http.StatusCreated
	}
	c.JSON(status, created)
}

// getGoal 查询 Goal。
// @Summary 查询 Goal
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 200 {object} execution.Goal "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals/{id} [get]
func (h *Controller) getGoal(c *core.Context) {
	var req struct {
		ID string `uri:"id" binding:"required,max=128"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	goal, err := h.service.GetGoal(c.Request.Context(), auth.UserID(c), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, goal)
}

// listGoals 列出 Goal。
// @Summary 列出 Goal
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param status query string false "状态过滤"
// @Param limit query int false "上限" minimum(1) maximum(200)
// @Success 200 {array} execution.Goal "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals [get]
func (h *Controller) listGoals(c *core.Context) {
	var req struct {
		Status string `form:"status" binding:"omitempty,max=32"`
		Limit  int    `form:"limit" binding:"omitempty,min=1,max=200"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	goals, err := h.service.ListGoals(
		c.Request.Context(),
		auth.UserID(c),
		durable.GoalStatus(req.Status),
		req.Limit,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, goals)
}

type steerGoalRequest struct {
	Content string `json:"content" binding:"required,max=4096"`
}

// steerGoal 注入 Steering 纠偏消息到当前活跃 Run。
// @Summary 注入 Steering
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "goal id"
// @Param request body steerGoalRequest true "JSON 请求"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "Goal 无活跃 Run 或状态冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals/{id}/steer [post]
func (h *Controller) steerGoal(c *core.Context) {
	var req struct {
		ID string `uri:"id" binding:"required,max=128"`
		steerGoalRequest
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if err := h.service.SteerGoal(c.Request.Context(), auth.UserID(c), req.ID, req.Content); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// pauseGoal 暂停 Goal。
// @Summary 暂停 Goal
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals/{id}/pause [post]
func (h *Controller) pauseGoal(c *core.Context) { h.transitionGoal(c, durable.GoalPaused) }

// resumeGoal 恢复 Goal。
// @Summary 恢复 Goal
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals/{id}/resume [post]
func (h *Controller) resumeGoal(c *core.Context) { h.transitionGoal(c, durable.GoalActive) }

func (h *Controller) transitionGoal(c *core.Context, to durable.GoalStatus) {
	var req struct {
		ID string `uri:"id" binding:"required,max=128"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if err := h.service.TransitionGoal(c.Request.Context(), auth.UserID(c), req.ID, to); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

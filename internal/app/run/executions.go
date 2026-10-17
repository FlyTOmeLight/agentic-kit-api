package run

import (
	"context"
	"net/http"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type ExecutionService interface {
	SubmitExecution(context.Context, string, string, sdk.ExecutionSpec, time.Duration) (*durable.Run, bool, error)
	CheckExecutionLease(context.Context, string, string, int64) (bool, error)
}

var _ ExecutionService = (*durable.Store)(nil)

func (h *Controller) RegisterExecutionRoutes(router core.Router) {
	group := router.Group("/internal/v1")
	group.GET("/healthz", h.health)
	group.POST("/executions", h.submitExecution)
	group.GET("/executions/:id/lease", h.executionLease)
}

// health 查询内部执行入口健康状态。
// @Summary 查询内部执行入口健康状态
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]string "成功"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Router /internal/v1/healthz [get]
func (h *Controller) health(c *core.Context) { c.JSON(200, map[string]string{"status": "ok"}) }

// submitExecution 提交执行任务。
// @Summary 提交执行任务
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param Idempotency-Key header string true "幂等标识；相同请求重放返回已有任务" maxlength(128)
// @Param request body agenticv1.SubmitExecutionRequest true "JSON 请求"
// @Success 202 {object} agenticv1.Run "成功"
// @Success 200 {object} agenticv1.Run "幂等重放"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/executions [post]
func (h *Controller) submitExecution(c *core.Context) {
	var req sdk.SubmitExecutionRequest
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
	r, created, err := h.execution.SubmitExecution(
		c.Request.Context(),
		auth.UserID(c),
		req.IdempotencyKey,
		req.Spec,
		time.Duration(req.TimeoutSeconds)*time.Second,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, r.Response())
}

// executionLease 校验任务执行权。
// @Summary 校验任务执行权
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param epoch query int true "执行 epoch" minimum(1)
// @Success 200 {object} agenticv1.LeaseCheck "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/executions/{id}/lease [get]
func (h *Controller) executionLease(c *core.Context) {
	var req sdk.CheckExecutionLeaseRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	allowed, err := h.execution.CheckExecutionLease(c.Request.Context(), auth.UserID(c), req.ID, req.Epoch)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.LeaseCheck{Allowed: allowed})
}

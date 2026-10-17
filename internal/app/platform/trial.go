package platform

import (
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// startTrial 启动 Agent 试运行。
// @Summary 启动 Agent 试运行
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param pid path string false "项目路径下的项目 ID"
// @Param Idempotency-Key header string true "幂等标识；相同请求重放返回已有任务" maxlength(100)
// @Param request body agenticv1.StartProjectTrialRequest true "JSON 请求"
// @Success 202 {object} agenticv1.ProjectTrial "成功"
// @Success 200 {object} agenticv1.ProjectTrial "幂等重放"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent}/trials [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent}/trials [post]
func (h *Controller) startTrial(c *core.Context) {
	var req sdk.StartProjectTrialRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	var headers struct {
		IdempotencyKey string `header:"Idempotency-Key" binding:"required,max=100"`
	}
	if err := binder.Bind(c, &headers).Header().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	req.IdempotencyKey = headers.IdempotencyKey

	out, created, err := h.business.StartTrial(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := 200
	if created {
		status = 202
	}
	c.JSON(status, out)
}

func (h *Controller) readTrial(c *core.Context) (*durable.Run, bool) {
	var req sdk.GetProjectTrialRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return nil, false
	}

	run, err := h.business.ReadTrial(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return nil, false
	}
	return run, true
}

// trial 查询 Agent 试运行。
// @Summary 查询 Agent 试运行
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param run path string true "run"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.ProjectTrial "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent}/trials/{run} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent}/trials/{run} [get]
func (h *Controller) trial(c *core.Context) {
	run, ok := h.readTrial(c)
	if !ok {
		return
	}
	c.JSON(200, sdk.ProjectTrial{Run: run.Response(), Input: run.Spec.Query})
}

// cancelTrial 取消 Agent 试运行。
// @Summary 取消 Agent 试运行
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param agent path string true "agent"
// @Param run path string true "run"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/agents/{agent}/trials/{run}/cancel [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/agents/{agent}/trials/{run}/cancel [post]
func (h *Controller) cancelTrial(c *core.Context) {
	run, ok := h.readTrial(c)
	if !ok {
		return
	}
	if err := h.store.Cancel(c.Request.Context(), c.GetString("projectOwner"), run.ID); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(204)
}

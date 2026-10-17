package run

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// get 查询任务。
// @Summary 查询任务
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 200 {object} agenticv1.Run "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/runs/{id} [get]
func (h *Controller) get(c *core.Context) {
	var req sdk.GetRunRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	run, err := h.service.Get(c.Request.Context(), auth.UserID(c), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, run.Response())
}

// cancel 取消任务。
// @Summary 取消任务
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/runs/{id}/cancel [post]
func (h *Controller) cancel(c *core.Context) {
	var req sdk.CancelRunRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if err := h.service.Cancel(c.Request.Context(), auth.UserID(c), req.ID); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// answer 提交澄清答案。
// @Summary 提交澄清答案
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param request body agenticv1.SupplyResultRequest true "JSON 请求"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/runs/{id}/answer [post]
func (h *Controller) answer(c *core.Context) { h.supply(c, durable.WaitingClarification) }

// resolve 提交人工复核结果。
// @Summary 提交人工复核结果
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param request body agenticv1.SupplyResultRequest true "JSON 请求"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/runs/{id}/resolve [post]
func (h *Controller) resolve(c *core.Context) { h.supply(c, durable.NeedsReview) }

func (h *Controller) supply(c *core.Context, status durable.Status) {
	var req struct {
		sdk.GetRunRequest
		sdk.SupplyResultRequest
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if err := h.service.SupplyResult(
		c.Request.Context(),
		auth.UserID(c),
		req.ID,
		req.StepID,
		req.Result,
		status,
	); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

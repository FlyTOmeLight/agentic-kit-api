package evaluation

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Controller struct {
	service Service
}

func NewController(service Service) *Controller {
	return &Controller{service: service}
}

// start 启动评测实验。
// @Summary 启动评测实验
// @Tags Evaluation
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param Idempotency-Key header string true "幂等标识；相同请求重放返回已有任务" maxlength(128)
// @Param request body agenticv1.StartEvaluationRequest true "JSON 请求"
// @Success 202 {object} agenticv1.EvaluationExperiment "成功"
// @Success 200 {object} agenticv1.EvaluationExperiment "幂等重放"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/evaluations [post]
func (h *Controller) start(c *core.Context) {
	var req sdk.StartEvaluationRequest
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
	out, created, err := h.service.StartEvaluation(
		c.Request.Context(),
		auth.UserID(c),
		req.IdempotencyKey,
		req,
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

// list 列出评测实验。
// @Summary 列出评测实验
// @Tags Evaluation
// @Produce json
// @Security BearerAuth
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Param tenantID query string false "按企业过滤"
// @Param projectID query string false "按项目过滤"
// @Success 200 {object} agenticv1.Page[agenticv1.EvaluationExperiment] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/evaluations [get]
func (h *Controller) list(c *core.Context) {
	var req sdk.ListEvaluationsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	out, err := h.service.Evaluations(c.Request.Context(), auth.UserID(c), req.ListEvaluationsOptions)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.EvaluationExperiment]{Items: out})
}

// get 查询评测实验。
// @Summary 查询评测实验
// @Tags Evaluation
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 200 {object} agenticv1.EvaluationExperiment "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/evaluations/{id} [get]
func (h *Controller) get(c *core.Context) {
	var req sdk.GetEvaluationRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	out, err := h.service.Evaluation(c.Request.Context(), auth.UserID(c), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

// cancel 取消评测实验。
// @Summary 取消评测实验
// @Tags Evaluation
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 204 "成功，无响应体"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/evaluations/{id}/cancel [post]
func (h *Controller) cancel(c *core.Context) {
	var req sdk.CancelEvaluationRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	if err := h.service.CancelEvaluation(c.Request.Context(), auth.UserID(c), req.ID); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(204)
}

// compare 比较评测实验。
// @Summary 比较评测实验
// @Tags Evaluation
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param baselineID query string true "基线实验 ID"
// @Success 200 {object} agenticv1.EvaluationComparison "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/evaluations/{id}/compare [get]
func (h *Controller) compare(c *core.Context) {
	var req sdk.CompareEvaluationsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	baseline, err := h.service.Evaluation(c.Request.Context(), auth.UserID(c), req.BaselineID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	candidate, err := h.service.Evaluation(c.Request.Context(), auth.UserID(c), req.CandidateID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	out, err := evaluation.Compare(*baseline, *candidate)
	if err != nil {
		serrors.Abort(c, 409, "IncompatibleEvaluation", err.Error())
		return
	}
	c.JSON(200, out)
}

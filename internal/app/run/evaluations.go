package run

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type recordEvaluationRequest struct {
	RunID  string `json:"runID" binding:"required,max=128"`
	Status string `json:"status" binding:"required,oneof=Passed Failed Error"`
	Output string `json:"output" binding:"max=65536"`
}

// recordEvaluation 记录 Worker 的验收结果并推进 Goal。
// @Summary 记录验收结果
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "goal id"
// @Param request body recordEvaluationRequest true "JSON 请求"
// @Success 200 {object} execution.Evaluation "成功或幂等重放"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "同一 Run 的验收结果冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals/{id}/evaluations [post]
func (h *Controller) recordEvaluation(c *core.Context) {
	var req struct {
		ID string `uri:"id" binding:"required,max=128"`
		recordEvaluationRequest
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	owner := auth.UserID(c)
	evaluation, err := h.service.RecordEvaluation(
		c.Request.Context(),
		owner,
		req.ID,
		req.RunID,
		durable.EvaluationStatus(req.Status),
		req.Output,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	if _, err = h.service.CheckGoalProgress(c.Request.Context(), owner, req.ID); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, evaluation)
}

// getEvaluation 查询某个 Run 的验收结果。
// @Summary 查询验收结果
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "goal id"
// @Param runID path string true "run id"
// @Success 200 {object} execution.Evaluation "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /internal/v1/goals/{id}/evaluations/{runID} [get]
func (h *Controller) getEvaluation(c *core.Context) {
	var req struct {
		ID    string `uri:"id" binding:"required,max=128"`
		RunID string `uri:"runID" binding:"required,max=128"`
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	evaluation, err := h.service.GetEvaluation(
		c.Request.Context(),
		auth.UserID(c),
		req.ID,
		req.RunID,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, evaluation)
}

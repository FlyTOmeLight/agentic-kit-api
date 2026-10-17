package run

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// trace 查询任务执行轨迹。
// @Summary 查询任务执行轨迹
// @Tags Run
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param after query int false "事件序号游标" default(0) minimum(0)
// @Success 200 {object} agenticv1.RunTrace "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/runs/{id}/trace [get]
func (h *Controller) trace(c *core.Context) {
	var req sdk.GetRunTraceRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	trace, err := h.service.Trace(c.Request.Context(), auth.UserID(c), req.ID, req.After)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, trace)
}

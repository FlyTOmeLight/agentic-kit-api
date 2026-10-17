package platform

import (
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// RegisterReleaseRoutes runs after runtime bearer authentication. The public
// gateway rejects /internal; current project administration is checked again.
func (h *Controller) RegisterReleaseRoutes(r core.Router) {
	r.POST("/internal/v1/coding-release-approval", h.approveCodingRelease)
}

// approveCodingRelease 审批 Coding 候选启用。
// @Summary 审批 Coding 候选启用
// @Tags Platform
// @Description 仅受信任 Coding 应用可调用；校验项目管理员权限及已完成评测的检查通过率和覆盖率门槛。
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body agenticv1.CodingReleaseRequest true "JSON 请求"
// @Success 200 {object} agenticv1.CodingReleaseApproval "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份或权限依赖暂不可用"
// @Router /internal/v1/coding-release-approval [post]
func (h *Controller) approveCodingRelease(c *core.Context) {
	var req sdk.CodingReleaseRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}

	out, err := h.business.ApproveCodingRelease(c, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

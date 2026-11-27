package run

import (
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func (h *Controller) RegisterWorkerRoutes(router core.Router) {
	group := router.Group("/internal/v1/worker")
	group.POST("/runs/:id/skills/read", h.readSkillFile)
}

// readSkillFile 按有效任务租约读取当前 Run 冻结清单中的 Skill 文件正文。
// @Summary 读取冻结 Skill 文件
// @Tags Run
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Run ID"
// @Param request body agenticv1.ReadSkillFileRequest true "JSON 请求"
// @Success 200 {object} agenticv1.SkillFileContent "成功"
// @Failure 400 {object} serrors.Error "参数或文件格式无效"
// @Failure 401 {object} serrors.Error "服务身份校验失败"
// @Failure 403 {object} serrors.Error "服务白名单或权限不足"
// @Failure 404 {object} serrors.Error "Skill 或文件不在冻结清单中"
// @Failure 409 {object} serrors.Error "租约失效或版本状态冲突"
// @Failure 503 {object} serrors.Error "Artifact 或对象存储暂不可用"
// @Router /internal/v1/worker/runs/{id}/skills/read [post]
func (h *Controller) readSkillFile(c *core.Context) {
	var req sdk.ReadSkillFileRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	content, err := h.execution.ReadSkillFile(
		c.Request.Context(),
		req.ID,
		req.WorkerID,
		req.Epoch,
		req.Skill,
		req.Path,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, content)
}

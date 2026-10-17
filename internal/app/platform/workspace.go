package platform

import (
	"crypto/sha256"
	"fmt"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type workspaceAccess struct {
	Allowed  bool               `json:"allowed"`
	CanEdit  bool               `json:"canEdit"`
	Projects []workspaceProject `json:"projects"`
}

type workspaceProject struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Level int    `json:"level"`
}

func tenantOwner(tenant string) string {
	return fmt.Sprintf("forge-tenant:%x", sha256.Sum256([]byte(tenant)))
}

func allowedProjects(c *core.Context) []string {
	value, ok := c.Get("workspaceAccess")
	if !ok {
		return nil
	}
	ids := []string{}
	for _, p := range value.(workspaceAccess).Projects {
		if p.Level >= 20 {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func environmentProject(c *core.Context, requested string) string {
	if c.Param("pid") != "" {
		return c.Param("pid")
	}
	return requested
}

func (h *Controller) environmentAccess(c *core.Context, requested, kind string, write bool) bool {
	if err := h.business.environmentAccess(c, requested, kind, write); err != nil {
		apperror.Respond(c, err)
		return false
	}
	return true
}

// datasets 列出数据集。
// @Summary 列出数据集
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param pid path string false "项目路径下的项目 ID"
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Success 200 {object} agenticv1.Page[agenticv1.Dataset] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/datasets [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/datasets [get]
func (h *Controller) datasets(c *core.Context) {
	var page sdk.PageOptions
	if err := binder.Bind(c, &page).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	rows, err := h.store.Datasets(c.Request.Context(), c.GetString("projectOwner"), page.After, page.Limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Dataset]{Items: rows})
}

// dataset 查询数据集。
// @Summary 查询数据集
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param dataset path string true "dataset"
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.Dataset "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/datasets/{dataset} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/datasets/{dataset} [get]
func (h *Controller) dataset(c *core.Context) {
	var req sdk.GetDatasetRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	row, err := h.store.Dataset(c.Request.Context(), c.GetString("projectOwner"), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

// saveDataset 保存数据集草稿。
// @Summary 保存数据集草稿
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param dataset path string true "dataset"
// @Param pid path string false "项目路径下的项目 ID"
// @Param request body agenticv1.SaveDatasetRequest true "JSON 请求"
// @Success 200 {object} agenticv1.Dataset "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/datasets/{dataset} [put]
// @Router /api/v1/tenants/{tid}/projects/{pid}/datasets/{dataset} [put]
func (h *Controller) saveDataset(c *core.Context) {
	var req sdk.SaveDatasetRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	row, err := h.store.SaveDataset(c.Request.Context(), c.GetString("projectOwner"), req.ID, req)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

// publishDataset 发布数据集版本。
// @Summary 发布数据集版本
// @Tags Platform
// @Accept json
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param X-CSRF-Token header string true "会话 CSRF 标识"
// @Param tid path string true "tid"
// @Param dataset path string true "dataset"
// @Param pid path string false "项目路径下的项目 ID"
// @Param request body agenticv1.PublishAgentRequest true "JSON 请求"
// @Success 200 {object} agenticv1.DatasetVersion "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/datasets/{dataset}/versions [post]
// @Router /api/v1/tenants/{tid}/projects/{pid}/datasets/{dataset}/versions [post]
func (h *Controller) publishDataset(c *core.Context) {
	var req sdk.PublishDatasetRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	row, err := h.store.PublishDataset(
		c.Request.Context(),
		c.GetString("projectOwner"),
		req.ID,
		req.Revision,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

// datasetVersion 查询数据集版本。
// @Summary 查询数据集版本
// @Tags Platform
// @Produce json
// @Param Cookie header string true "包含 forge_session 的会话 Cookie"
// @Param tid path string true "tid"
// @Param dataset path string true "dataset"
// @Param version path int true "version" minimum(1)
// @Param pid path string false "项目路径下的项目 ID"
// @Success 200 {object} agenticv1.DatasetVersion "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "权限不足"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Failure 503 {object} serrors.Error "身份、权限或模板依赖暂不可用"
// @Router /api/v1/tenants/{tid}/datasets/{dataset}/versions/{version} [get]
// @Router /api/v1/tenants/{tid}/projects/{pid}/datasets/{dataset}/versions/{version} [get]
func (h *Controller) datasetVersion(c *core.Context) {
	var req sdk.GetDatasetVersionRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	row, err := h.store.DatasetVersion(c.Request.Context(), c.GetString("projectOwner"), req.ID, req.Number)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, row)
}

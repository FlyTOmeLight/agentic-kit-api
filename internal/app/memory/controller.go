package memory

import (
	"net/http"
	"strconv"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Controller struct{ service *Service }

func NewController(service *Service) *Controller { return &Controller{service: service} }

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	g := v1.Group("/memories")
	g.POST("", h.create)
	g.GET("", h.list)
	g.GET("/:id", h.get)
	g.PUT("/:id", h.update)
	g.POST("/:id/retire", h.retire)
}

// createMemory 创建记忆条目。
// @Summary 创建记忆
// @Tags Memory
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body agenticv1.CreateMemoryRequest true "JSON 请求"
// @Success 201 {object} agenticv1.MemoryEntry "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 409 {object} serrors.Error "状态或幂等冲突"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/memories [post]
func (h *Controller) create(c *core.Context) {
	var req sdk.CreateMemoryRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	owner := auth.UserID(c)
	entry, err := h.service.Create(c.Request.Context(), owner, owner, req.IdempotencyKey, req.Document)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, entry)
}

// getMemory 查询记忆条目。
// @Summary 查询记忆
// @Tags Memory
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 200 {object} agenticv1.MemoryEntry "成功"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Router /v1/memories/{id} [get]
func (h *Controller) get(c *core.Context) {
	owner := auth.UserID(c)
	entry, err := h.service.Get(c.Request.Context(), owner, c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

// listMemories 列出记忆条目。
// @Summary 列出记忆
// @Tags Memory
// @Produce json
// @Security BearerAuth
// @Param after query string false "游标"
// @Param limit query int false "数量" default(50)
// @Success 200 {array} agenticv1.MemoryEntry "成功"
// @Router /v1/memories [get]
func (h *Controller) list(c *core.Context) {
	owner := auth.UserID(c)
	after := c.Query("after")
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100 {
			limit = n
		}
	}
	entries, err := h.service.List(c.Request.Context(), owner, after, limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, entries)
}

// updateMemory 更新记忆条目。
// @Summary 更新记忆
// @Tags Memory
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param request body agenticv1.UpdateMemoryRequest true "JSON 请求"
// @Success 200 {object} agenticv1.MemoryEntry "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态或修订冲突"
// @Router /v1/memories/{id} [put]
func (h *Controller) update(c *core.Context) {
	var req sdk.UpdateMemoryRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	owner := auth.UserID(c)
	entry, err := h.service.Update(
		c.Request.Context(),
		owner,
		owner,
		req.IdempotencyKey,
		c.Param("id"),
		req.ExpectedRevision,
		req.Document,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

// retireMemory 停用记忆条目。
// @Summary 停用记忆
// @Tags Memory
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param request body agenticv1.RetireMemoryRequest true "JSON 请求"
// @Success 200 {object} agenticv1.MemoryEntry "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态或修订冲突"
// @Router /v1/memories/{id}/retire [post]
func (h *Controller) retire(c *core.Context) {
	var req sdk.RetireMemoryRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	owner := auth.UserID(c)
	entry, err := h.service.Retire(
		c.Request.Context(),
		owner,
		owner,
		req.IdempotencyKey,
		c.Param("id"),
		req.ExpectedRevision,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

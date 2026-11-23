package memory

import (
	"net/http"
	"strconv"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Controller struct{ service *Service }

func NewController(service *Service) *Controller { return &Controller{service: service} }

func bindIdempotencyKey(c *core.Context, key *string) bool {
	var header struct {
		IdempotencyKey string `header:"Idempotency-Key" binding:"required,max=128"`
	}
	if err := binder.Bind(c, &header).Header().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return false
	}
	*key = header.IdempotencyKey
	return true
}

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	g := v1.Group("/memories")
	g.POST("", h.create)
	g.GET("", h.list)
	g.GET("/:id", h.get)
	g.GET("/:id/versions", h.listVersions)
	g.GET("/:id/versions/:versionID", h.getVersion)
	g.PUT("/:id", h.update)
	g.POST("/:id/retire", h.retire)
}

// listMemoryVersions lists adopted archived versions.
// @Summary 列出记忆版本
// @Tags Memory
// @Produce json
// @Security BearerAuth
// @Param id path string true "记忆 ID"
// @Param afterRevision query int false "上一页游标"
// @Param limit query int false "数量，1 到 100" default(50)
// @Success 200 {object} memory.VersionPage
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 503 {object} serrors.Error "项目权限检查暂不可用"
// @Router /v1/memories/{id}/versions [get]
func (h *Controller) listVersions(c *core.Context) {
	var after int64
	limit := 50
	if value := c.Query("afterRevision"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			apperror.Respond(c, memory.ErrInvalid)
			return
		}
		after = parsed
	}
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			apperror.Respond(c, memory.ErrInvalid)
			return
		}
		limit = parsed
	}
	page, err := h.service.ListVersions(c.Request.Context(), auth.UserID(c), c.Param("id"), after, limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

// getMemoryVersion reads one adopted archived version.
// @Summary 查询记忆版本
// @Tags Memory
// @Produce json
// @Security BearerAuth
// @Param id path string true "记忆 ID"
// @Param versionID path string true "版本 ID"
// @Success 200 {object} memory.Version
// @Failure 404 {object} serrors.Error "资源不存在或版本未生效"
// @Failure 503 {object} serrors.Error "归档版本或依赖不可用"
// @Router /v1/memories/{id}/versions/{versionID} [get]
func (h *Controller) getVersion(c *core.Context) {
	version, err := h.service.GetVersion(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Param("versionID"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, version)
}

// createMemory 创建记忆条目。
// @Summary 创建记忆
// @Description Idempotency-Key 必须通过请求 Header 发送，不能放在 JSON body 中。
// @Tags Memory
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param Idempotency-Key header string true "幂等标识；同键同参重放返回已有结果" maxlength(128)
// @Param request body agenticv1.CreateMemoryRequest true "JSON 请求"
// @Success 201 {object} agenticv1.MemoryEntry "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 403 {object} serrors.Error "记忆作用域不匹配或缺少项目写权限"
// @Failure 409 {object} serrors.Error "状态或幂等冲突"
// @Failure 503 {object} serrors.Error "本地生效结果不明、归档版本或依赖暂不可用"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/memories [post]
func (h *Controller) create(c *core.Context) {
	var req sdk.CreateMemoryRequest
	if !bindIdempotencyKey(c, &req.IdempotencyKey) {
		return
	}
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
// @Failure 503 {object} serrors.Error "项目权限检查暂不可用"
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
// @Failure 403 {object} serrors.Error "缺少项目读权限"
// @Failure 503 {object} serrors.Error "项目权限检查暂不可用"
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

// updateMemory 更新或恢复记忆条目。
// @Summary 更新或恢复记忆
// @Description restore 为 false 或省略时更新活跃条目；restore 为 true 时恢复已停用条目。
// @Description Idempotency-Key 必须通过请求 Header 发送，不能放在 JSON body 中。
// @Tags Memory
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param Idempotency-Key header string true "幂等标识；同键同参重放返回已有结果" maxlength(128)
// @Param request body agenticv1.UpdateMemoryRequest true "JSON 请求"
// @Success 200 {object} agenticv1.MemoryEntry "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 403 {object} serrors.Error "记忆作用域不匹配或缺少项目写权限"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态、修订或幂等冲突"
// @Failure 503 {object} serrors.Error "本地生效结果不明、归档版本或依赖暂不可用"
// @Router /v1/memories/{id} [put]
func (h *Controller) update(c *core.Context) {
	var req sdk.UpdateMemoryRequest
	if !bindIdempotencyKey(c, &req.IdempotencyKey) {
		return
	}
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	owner := auth.UserID(c)
	var entry *sdk.MemoryEntry
	var err error
	if req.Restore {
		entry, err = h.service.Restore(
			c.Request.Context(),
			owner,
			owner,
			req.IdempotencyKey,
			c.Param("id"),
			req.ExpectedRevision,
			req.Document,
		)
	} else {
		entry, err = h.service.Update(
			c.Request.Context(),
			owner,
			owner,
			req.IdempotencyKey,
			c.Param("id"),
			req.ExpectedRevision,
			req.Document,
		)
	}
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

// retireMemory 停用记忆条目。
// @Summary 停用记忆
// @Description Idempotency-Key 必须通过请求 Header 发送，不能放在 JSON body 中。
// @Tags Memory
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param Idempotency-Key header string true "幂等标识；同键同参重放返回已有结果" maxlength(128)
// @Param request body agenticv1.RetireMemoryRequest true "JSON 请求"
// @Success 200 {object} agenticv1.MemoryEntry "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 403 {object} serrors.Error "缺少项目写权限"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "状态或修订冲突"
// @Failure 503 {object} serrors.Error "本地生效结果不明或依赖暂不可用"
// @Router /v1/memories/{id}/retire [post]
func (h *Controller) retire(c *core.Context) {
	var req sdk.RetireMemoryRequest
	if !bindIdempotencyKey(c, &req.IdempotencyKey) {
		return
	}
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

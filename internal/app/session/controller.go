package session

import (
	"net/http"
	"strings"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/FlyTOmeLight/server-go/pkg/binder"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// createSession 创建会话。
// @Summary 创建会话
// @Tags Session
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body agenticv1.CreateSessionRequest true "JSON 请求"
// @Success 201 {object} agenticv1.Session "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/sessions [post]
func (h *Controller) createSession(c *core.Context) {
	var req sdk.CreateSessionRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	s, err := h.service.CreateSession(c.Request.Context(), auth.UserID(c), req.AgentID, req.Title)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(201, s)
}

// getSession 查询会话。
// @Summary 查询会话
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Success 200 {object} agenticv1.Session "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/sessions/{id} [get]
func (h *Controller) getSession(c *core.Context) {
	var req sdk.GetSessionRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	s, err := h.service.Session(c.Request.Context(), auth.UserID(c), req.ID)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, s)
}

// listSessions 列出会话。
// @Summary 列出会话
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Param agentID query string false "按 Agent 过滤"
// @Success 200 {object} agenticv1.Page[agenticv1.Session] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/sessions [get]
func (h *Controller) listSessions(c *core.Context) {
	var req sdk.ListSessionsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	items, err := h.service.Sessions(
		c.Request.Context(),
		auth.UserID(c),
		req.AgentID,
		req.After,
		req.Limit,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Session]{Items: items})
}

// listMessages 列出会话消息。
// @Summary 列出会话消息
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param after query int false "消息序号游标" default(0) minimum(0)
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(200)
// @Success 200 {object} agenticv1.Page[agenticv1.SessionMessage] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/sessions/{id}/messages [get]
func (h *Controller) listMessages(c *core.Context) {
	var req sdk.ListMessagesRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	items, err := h.service.Messages(c.Request.Context(), auth.UserID(c), req.SessionID, req.After, req.Limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.SessionMessage]{Items: items})
}

// listSessionRuns 列出会话任务。
// @Summary 列出会话任务
// @Tags Session
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param limit query int false "每页数量" default(50) minimum(1) maximum(100)
// @Param after query string false "上一页末项游标"
// @Success 200 {object} agenticv1.Page[agenticv1.Run] "成功"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/sessions/{id}/runs [get]
func (h *Controller) listSessionRuns(c *core.Context) {
	var req sdk.ListSessionRunsRequest
	if err := binder.Bind(c, &req).All().Error(); err != nil {
		serrors.AbortWithError(c, err)
		return
	}
	items, err := h.service.SessionRuns(c.Request.Context(), auth.UserID(c), req.SessionID, req.After, req.Limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	out := make([]sdk.Run, len(items))
	for i, r := range items {
		out[i] = r.Response()
	}
	c.JSON(200, sdk.Page[sdk.Run]{Items: out})
}

const (
	runLifetime    = 24 * time.Hour
	maxRunAttempts = 5
)

// submitMessage 提交用户消息。
// @Summary 提交用户消息
// @Tags Session
// @Description 消息与任务在同一事务创建；相同幂等标识重放返回原任务。
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "id"
// @Param Idempotency-Key header string true "幂等标识；相同请求重放返回已有任务" maxlength(128)
// @Param request body agenticv1.SendMessageRequest true "JSON 请求"
// @Success 202 {object} agenticv1.Run "成功"
// @Success 200 {object} agenticv1.Run "幂等重放"
// @Failure 400 {object} serrors.Error "参数无效"
// @Failure 401 {object} serrors.Error "身份校验失败"
// @Failure 404 {object} serrors.Error "资源不存在"
// @Failure 409 {object} serrors.Error "版本、任务状态、评测条件或幂等标识冲突"
// @Failure 413 {object} serrors.Error "请求体超过限制"
// @Failure 500 {object} serrors.Error "内部错误"
// @Router /v1/sessions/{id}/messages [post]
func (h *Controller) submitMessage(c *core.Context) {
	var req sdk.SendMessageRequest
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
	if strings.TrimSpace(req.IdempotencyKey) == "" || strings.TrimSpace(req.Query) == "" {
		serrors.Abort(
			c,
			http.StatusBadRequest,
			"InvalidParameter",
			"nonempty query and Idempotency-Key are required",
		)
		return
	}
	owner := auth.UserID(c)
	run, created, err := h.service.Submit(
		c.Request.Context(),
		owner,
		req.SessionID,
		req.IdempotencyKey,
		req.Query,
		runLifetime,
		maxRunAttempts,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, run.Response())
}

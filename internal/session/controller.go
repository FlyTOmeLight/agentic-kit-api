package session

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"github.com/gin-gonic/gin"
)

func (h *controller) createSession(c *gin.Context) {
	var req sdk.CreateSessionRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.AgentID == "" || utf8.RuneCountInString(req.AgentID) > 128 {
		httpx.Abort(c, 400, "InvalidRequest", "invalid agentID")
		return
	}
	s, err := h.store.CreateSession(c.Request.Context(), httpx.Owner(c), req.AgentID, req.Title)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(201, s)
}
func (h *controller) getSession(c *gin.Context) {
	s, err := h.store.Session(c.Request.Context(), httpx.Owner(c), c.Param("id"))
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(200, s)
}
func (h *controller) listSessions(c *gin.Context) {
	limit, ok := httpx.PageLimit(c, 100)
	if !ok {
		return
	}
	items, err := h.store.Sessions(c.Request.Context(), httpx.Owner(c), c.Query("agentID"), c.Query("after"), limit)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Session]{Items: items})
}
func (h *controller) listMessages(c *gin.Context) {
	limit, ok := httpx.PageLimit(c, 200)
	if !ok {
		return
	}
	after, err := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || after < 0 {
		httpx.Abort(c, 400, "InvalidCursor", "invalid message cursor")
		return
	}
	items, err := h.store.Messages(c.Request.Context(), httpx.Owner(c), c.Param("id"), after, limit)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.SessionMessage]{Items: items})
}
func (h *controller) listSessionRuns(c *gin.Context) {
	limit, ok := httpx.PageLimit(c, 100)
	if !ok {
		return
	}
	items, err := h.store.SessionRuns(c.Request.Context(), httpx.Owner(c), c.Param("id"), c.Query("after"), limit)
	if err != nil {
		httpx.RespondError(c, err)
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

func (h *controller) submitMessage(c *gin.Context) {
	var req sdk.SendMessageRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	var headers submitMessageHeaders
	if err := c.ShouldBindHeader(&headers); err != nil || strings.TrimSpace(headers.IdempotencyKey) == "" || strings.TrimSpace(req.Query) == "" {
		httpx.Abort(c, http.StatusBadRequest, "InvalidRequest", "nonempty query and Idempotency-Key are required")
		return
	}
	owner := httpx.Owner(c)
	run, created, err := h.store.Submit(c.Request.Context(), owner, c.Param("id"), headers.IdempotencyKey, req.Query, runLifetime, maxRunAttempts)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, run.Response())
}

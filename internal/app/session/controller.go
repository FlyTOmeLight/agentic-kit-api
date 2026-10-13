package session

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func (h *controller) createSession(c *gin.Context) {
	var req sdk.CreateSessionRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	if req.AgentID == "" || utf8.RuneCountInString(req.AgentID) > 128 {
		serrors.Abort(c, 400, "InvalidParameter", "invalid agentID")
		return
	}
	s, err := h.service.CreateSession(c.Request.Context(), auth.UserID(c), req.AgentID, req.Title)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(201, s)
}

func (h *controller) getSession(c *gin.Context) {
	s, err := h.service.Session(c.Request.Context(), auth.UserID(c), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, s)
}

func (h *controller) listSessions(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	if !ok {
		return
	}
	items, err := h.service.Sessions(
		c.Request.Context(),
		auth.UserID(c),
		c.Query("agentID"),
		c.Query("after"),
		limit,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Session]{Items: items})
}

func (h *controller) listMessages(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 200)
	if !ok {
		return
	}
	after, err := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || after < 0 {
		serrors.Abort(c, 400, "InvalidCursor", "invalid message cursor")
		return
	}
	items, err := h.service.Messages(c.Request.Context(), auth.UserID(c), c.Param("id"), after, limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.SessionMessage]{Items: items})
}

func (h *controller) listSessionRuns(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	if !ok {
		return
	}
	items, err := h.service.SessionRuns(c.Request.Context(), auth.UserID(c), c.Param("id"), c.Query("after"), limit)
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

func (h *controller) submitMessage(c *gin.Context) {
	var req sdk.SendMessageRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	var headers submitMessageHeaders
	if err := c.ShouldBindHeader(
		&headers,
	); err != nil || strings.TrimSpace(headers.IdempotencyKey) == "" ||
		strings.TrimSpace(req.Query) == "" {
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
		c.Param("id"),
		headers.IdempotencyKey,
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

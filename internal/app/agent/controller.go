package agent

import (
	"net/http"
	"strconv"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func (h *controller) createAgent(c *gin.Context) {
	var req sdk.CreateAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	a, err := h.service.CreateAgent(c.Request.Context(), auth.UserID(c), req.Name, req.Config)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

func (h *controller) getAgent(c *gin.Context) {
	a, err := h.service.Agent(c.Request.Context(), auth.UserID(c), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *controller) updateAgent(c *gin.Context) {
	var req sdk.UpdateAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	a, err := h.service.UpdateAgent(
		c.Request.Context(),
		auth.UserID(c),
		c.Param("id"),
		req.Name,
		req.Revision,
		req.Config,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *controller) publishAgent(c *gin.Context) {
	var req sdk.PublishAgentRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	if req.Revision < 1 {
		serrors.Abort(c, 400, "InvalidParameter", "invalid revision")
		return
	}
	v, err := h.service.PublishAgent(c.Request.Context(), auth.UserID(c), c.Param("id"), req.Revision)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *controller) getVersion(c *gin.Context) {
	number, err := strconv.ParseInt(c.Param("number"), 10, 64)
	if err != nil || number < 1 {
		serrors.Abort(c, 400, "InvalidParameter", "invalid version")
		return
	}
	v, err := h.service.AgentVersion(c.Request.Context(), auth.UserID(c), c.Param("id"), number)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *controller) listAgents(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	if !ok {
		return
	}
	items, err := h.service.Agents(c.Request.Context(), auth.UserID(c), c.Query("after"), limit)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.Agent]{Items: items})
}

package agent

import (
	"net/http"
	"strconv"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/gin-gonic/gin"
)

func (h *controller) createAgent(c *gin.Context) {
	var req createAgentRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	a, err := h.store.CreateAgent(c.Request.Context(), httpx.Owner(c), req.Name, req.Config)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}
func (h *controller) getAgent(c *gin.Context) {
	a, err := h.store.Agent(c.Request.Context(), httpx.Owner(c), c.Param("id"))
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}
func (h *controller) updateAgent(c *gin.Context) {
	var req updateAgentRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	a, err := h.store.UpdateAgent(c.Request.Context(), httpx.Owner(c), c.Param("id"), req.Name, req.Revision, req.Config)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}
func (h *controller) publishAgent(c *gin.Context) {
	var req publishRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	v, err := h.store.PublishAgent(c.Request.Context(), httpx.Owner(c), c.Param("id"), req.Revision)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *controller) getVersion(c *gin.Context) {
	number, err := strconv.ParseInt(c.Param("number"), 10, 64)
	if err != nil || number < 1 {
		httpx.Abort(c, 400, "InvalidRequest", "invalid version")
		return
	}
	v, err := h.store.AgentVersion(c.Request.Context(), httpx.Owner(c), c.Param("id"), number)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *controller) listAgents(c *gin.Context) {
	limit, ok := httpx.PageLimit(c, 100)
	if !ok {
		return
	}
	items, err := h.store.Agents(c.Request.Context(), httpx.Owner(c), c.Query("after"), limit)
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}

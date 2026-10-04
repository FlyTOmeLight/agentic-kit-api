package httpapi

import (
	"net/http"
	"strconv"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
)

type createAgentRequest struct {
	Name   string              `json:"name" binding:"required,max=200"`
	Config durable.AgentConfig `json:"config" binding:"required"`
}
type updateAgentRequest struct {
	Name     string              `json:"name" binding:"required,max=200"`
	Revision int64               `json:"revision" binding:"required,min=1"`
	Config   durable.AgentConfig `json:"config" binding:"required"`
}

func (h *handler) createAgent(c *gin.Context) {
	var req createAgentRequest
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.store.CreateAgent(c.Request.Context(), c.GetString(ownerKey), req.Name, req.Config)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}
func (h *handler) getAgent(c *gin.Context) {
	a, err := h.store.Agent(c.Request.Context(), c.GetString(ownerKey), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}
func (h *handler) updateAgent(c *gin.Context) {
	var req updateAgentRequest
	if !bindJSON(c, &req) {
		return
	}
	a, err := h.store.UpdateAgent(c.Request.Context(), c.GetString(ownerKey), c.Param("id"), req.Name, req.Revision, req.Config)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}
func (h *handler) publishAgent(c *gin.Context) {
	var req struct {
		Revision int64 `json:"revision" binding:"required,min=1"`
	}
	if !bindJSON(c, &req) {
		return
	}
	v, err := h.store.PublishAgent(c.Request.Context(), c.GetString(ownerKey), c.Param("id"), req.Revision)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func (h *handler) getVersion(c *gin.Context) {
	number, err := strconv.ParseInt(c.Param("number"), 10, 64)
	if err != nil || number < 1 {
		abort(c, 400, "InvalidRequest", "invalid version")
		return
	}
	v, err := h.store.AgentVersion(c.Request.Context(), c.GetString(ownerKey), c.Param("id"), number)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}
func pageLimit(c *gin.Context, max int) (int, bool) {
	n, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || n < 1 || n > max {
		abort(c, 400, "InvalidRequest", "invalid page size")
		return 0, false
	}
	return n, true
}
func (h *handler) listAgents(c *gin.Context) {
	limit, ok := pageLimit(c, 100)
	if !ok {
		return
	}
	items, err := h.store.Agents(c.Request.Context(), c.GetString(ownerKey), c.Query("after"), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}

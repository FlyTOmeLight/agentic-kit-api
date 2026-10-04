package httpapi

import (
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *handler) createSession(c *gin.Context) {
	var req struct {
		AgentID string `json:"agentID" binding:"required,max=128"`
		Title   string `json:"title" binding:"max=200"`
	}
	if !bindJSON(c, &req) {
		return
	}
	s, err := h.store.CreateSession(c.Request.Context(), c.GetString(ownerKey), req.AgentID, req.Title)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(201, s)
}
func (h *handler) getSession(c *gin.Context) {
	s, err := h.store.Session(c.Request.Context(), c.GetString(ownerKey), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, s)
}
func (h *handler) listSessions(c *gin.Context) {
	limit, ok := pageLimit(c, 100)
	if !ok {
		return
	}
	items, err := h.store.Sessions(c.Request.Context(), c.GetString(ownerKey), c.Query("agentID"), c.Query("after"), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}
func (h *handler) listMessages(c *gin.Context) {
	limit, ok := pageLimit(c, 200)
	if !ok {
		return
	}
	after, err := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || after < 0 {
		abort(c, 400, "InvalidCursor", "invalid message cursor")
		return
	}
	items, err := h.store.Messages(c.Request.Context(), c.GetString(ownerKey), c.Param("id"), after, limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}
func (h *handler) listSessionRuns(c *gin.Context) {
	limit, ok := pageLimit(c, 100)
	if !ok {
		return
	}
	items, err := h.store.SessionRuns(c.Request.Context(), c.GetString(ownerKey), c.Param("id"), c.Query("after"), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}

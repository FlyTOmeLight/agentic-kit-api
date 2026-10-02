package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
)

const (
	runLifetime    = 24 * time.Hour
	maxRunAttempts = 5
)

func (h *runHandler) create(c *gin.Context) {
	var req createRunRequest
	if !bindJSON(c, &req) {
		return
	}
	var headers createRunHeaders
	if err := c.ShouldBindHeader(&headers); err != nil || strings.TrimSpace(headers.IdempotencyKey) == "" || strings.TrimSpace(req.Query) == "" {
		abort(c, http.StatusBadRequest, "InvalidRequest", "nonempty query and Idempotency-Key are required")
		return
	}
	owner := c.GetString(ownerKey)
	agent, ok := h.agents[req.AgentID]
	if !ok || !slices.Contains(agent.Owners, owner) {
		abort(c, http.StatusNotFound, "NotFound", "agent not found")
		return
	}
	spec := agent.Spec
	spec.Query = req.Query
	run, created, err := h.store.Create(c.Request.Context(), owner, req.SessionID, headers.IdempotencyKey, spec, runLifetime, maxRunAttempts)
	if err != nil {
		respondError(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, run)
}

func (h *runHandler) get(c *gin.Context) {
	run, err := h.store.Get(c.Request.Context(), c.GetString(ownerKey), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, run)
}

func (h *runHandler) cancel(c *gin.Context) {
	if err := h.store.Cancel(c.Request.Context(), c.GetString(ownerKey), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *runHandler) answer(c *gin.Context)  { h.supply(c, durable.WaitingClarification) }
func (h *runHandler) resolve(c *gin.Context) { h.supply(c, durable.NeedsReview) }
func (h *runHandler) supply(c *gin.Context, status durable.Status) {
	var req supplyResultRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.store.SupplyResult(c.Request.Context(), c.GetString(ownerKey), c.Param("id"), req.StepID, req.Result, status); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
)

const (
	runLifetime    = 24 * time.Hour
	maxRunAttempts = 5
)

func (h *handler) create(c *gin.Context) {
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
	run, created, err := h.store.Submit(c.Request.Context(), owner, c.Param("id"), headers.IdempotencyKey, req.Query, runLifetime, maxRunAttempts)
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

func (h *handler) get(c *gin.Context) {
	run, err := h.store.Get(c.Request.Context(), c.GetString(ownerKey), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, run)
}

func (h *handler) cancel(c *gin.Context) {
	if err := h.store.Cancel(c.Request.Context(), c.GetString(ownerKey), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) answer(c *gin.Context)  { h.supply(c, durable.WaitingClarification) }
func (h *handler) resolve(c *gin.Context) { h.supply(c, durable.NeedsReview) }
func (h *handler) supply(c *gin.Context, status durable.Status) {
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

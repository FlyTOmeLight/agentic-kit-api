package run

import (
	"net/http"
	"unicode/utf8"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"github.com/gin-gonic/gin"
)

func (h *controller) get(c *gin.Context) {
	run, err := h.store.Get(c.Request.Context(), httpx.Owner(c), c.Param("id"))
	if err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, run.Response())
}

func (h *controller) cancel(c *gin.Context) {
	if err := h.store.Cancel(c.Request.Context(), httpx.Owner(c), c.Param("id")); err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *controller) answer(c *gin.Context)  { h.supply(c, durable.WaitingClarification) }
func (h *controller) resolve(c *gin.Context) { h.supply(c, durable.NeedsReview) }
func (h *controller) supply(c *gin.Context, status durable.Status) {
	var req sdk.SupplyResultRequest
	if !httpx.BindJSON(c, &req) {
		return
	}
	if req.StepID == "" || utf8.RuneCountInString(req.StepID) > 128 || req.Result == "" {
		httpx.Abort(c, 400, "InvalidRequest", "stepID and result are required")
		return
	}
	if err := h.store.SupplyResult(c.Request.Context(), httpx.Owner(c), c.Param("id"), req.StepID, req.Result, status); err != nil {
		httpx.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

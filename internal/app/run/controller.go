package run

import (
	"net/http"
	"unicode/utf8"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func (h *controller) get(c *gin.Context) {
	run, err := h.service.Get(c.Request.Context(), auth.UserID(c), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(http.StatusOK, run.Response())
}

func (h *controller) cancel(c *gin.Context) {
	if err := h.service.Cancel(c.Request.Context(), auth.UserID(c), c.Param("id")); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *controller) answer(c *gin.Context) { h.supply(c, durable.WaitingClarification) }

func (h *controller) resolve(c *gin.Context) { h.supply(c, durable.NeedsReview) }

func (h *controller) supply(c *gin.Context, status durable.Status) {
	var req sdk.SupplyResultRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	if req.StepID == "" || utf8.RuneCountInString(req.StepID) > 128 || req.Result == "" {
		serrors.Abort(c, 400, "InvalidParameter", "stepID and result are required")
		return
	}
	if err := h.service.SupplyResult(
		c.Request.Context(),
		auth.UserID(c),
		c.Param("id"),
		req.StepID,
		req.Result,
		status,
	); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

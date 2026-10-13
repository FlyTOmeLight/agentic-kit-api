package evaluation

import (
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/evaluation"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type controller struct{ service Service }

func (h *controller) start(c *gin.Context) {
	var req sdk.StartEvaluationRequest
	if !binder.BindJSON(c, &req) {
		return
	}
	out, created, err := h.service.StartEvaluation(
		c.Request.Context(),
		auth.UserID(c),
		c.GetHeader("Idempotency-Key"),
		req,
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, out)
}

func (h *controller) list(c *gin.Context) {
	limit, ok := binder.QueryLimit(c, 50, 100)
	if !ok {
		return
	}
	out, err := h.service.Evaluations(
		c.Request.Context(),
		auth.UserID(c),
		sdk.ListEvaluationsOptions{
			PageOptions: sdk.PageOptions{Limit: limit, After: c.Query("after")},
			TenantID:    c.Query("tenantID"),
			ProjectID:   c.Query("projectID"),
		},
	)
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, sdk.Page[sdk.EvaluationExperiment]{Items: out})
}

func (h *controller) get(c *gin.Context) {
	out, err := h.service.Evaluation(c.Request.Context(), auth.UserID(c), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	c.JSON(200, out)
}

func (h *controller) cancel(c *gin.Context) {
	if err := h.service.CancelEvaluation(c.Request.Context(), auth.UserID(c), c.Param("id")); err != nil {
		apperror.Respond(c, err)
		return
	}
	c.Status(204)
}

func (h *controller) compare(c *gin.Context) {
	baseline, err := h.service.Evaluation(c.Request.Context(), auth.UserID(c), c.Query("baselineID"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	candidate, err := h.service.Evaluation(c.Request.Context(), auth.UserID(c), c.Param("id"))
	if err != nil {
		apperror.Respond(c, err)
		return
	}
	out, err := evaluation.Compare(*baseline, *candidate)
	if err != nil {
		serrors.Abort(c, 409, "IncompatibleEvaluation", err.Error())
		return
	}
	c.JSON(200, out)
}

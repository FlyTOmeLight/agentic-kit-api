package run

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/pkg/binder"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/apperror"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type ExecutionService interface {
	SubmitExecution(context.Context, string, string, sdk.ExecutionSpec, time.Duration) (*durable.Run, bool, error)
	CheckExecutionLease(context.Context, string, string, int64) (bool, error)
	CheckCodingEvaluationLease(context.Context, string, string, int64) (bool, error)
}

var _ ExecutionService = (*durable.Store)(nil)

func RegisterExecutionRoutes(router gin.IRouter, store ExecutionService, owners []string) {
	allowed := map[string]bool{}
	for _, owner := range owners {
		allowed[owner] = true
	}
	group := router.Group("/internal/v1", func(c *gin.Context) {
		if !allowed[auth.UserID(c)] {
			serrors.Abort(c, 403, "Forbidden", "trusted application required")
			return
		}
		c.Next()
	})
	group.GET("/evaluation-trials/:trial/lease", func(c *gin.Context) {
		epoch, err := strconv.ParseInt(c.Query("epoch"), 10, 64)
		if err != nil || epoch < 1 {
			apperror.Respond(c, durable.ErrInvalid)
			return
		}
		allowed, err := store.CheckCodingEvaluationLease(
			c.Request.Context(),
			c.Param("trial"),
			c.Query("runID"),
			epoch,
		)
		if err != nil {
			apperror.Respond(c, err)
			return
		}
		c.JSON(200, sdk.LeaseCheck{Allowed: allowed})
	})
	group.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	group.POST("/executions", func(c *gin.Context) {
		var req sdk.SubmitExecutionRequest
		if !binder.BindJSON(c, &req) {
			return
		}
		r, created, e := store.SubmitExecution(
			c.Request.Context(),
			auth.UserID(c),
			c.GetHeader("Idempotency-Key"),
			req.Spec,
			time.Duration(req.TimeoutSeconds)*time.Second,
		)
		if e != nil {
			apperror.Respond(c, e)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		c.JSON(status, r.Response())
	})
	group.GET("/executions/:id/lease", func(c *gin.Context) {
		epoch, e := strconv.ParseInt(c.Query("epoch"), 10, 64)
		if e != nil || epoch < 1 {
			serrors.Abort(c, 400, "InvalidEpoch", "positive epoch required")
			return
		}
		ok, e := store.CheckExecutionLease(c.Request.Context(), auth.UserID(c), c.Param("id"), epoch)
		if e != nil {
			apperror.Respond(c, e)
			return
		}
		c.JSON(200, sdk.LeaseCheck{Allowed: ok})
	})
}

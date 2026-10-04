package run

import (
	"context"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"time"
)

type ExecutionService interface {
	SubmitExecution(context.Context, string, string, durable.Spec, time.Duration) (*durable.Run, bool, error)
	CheckExecutionLease(context.Context, string, string, int64) (bool, error)
}

var _ ExecutionService = (*durable.Store)(nil)

func RegisterExecutionRoutes(router *gin.Engine, store ExecutionService, owners []string) {
	allowed := map[string]bool{}
	for _, owner := range owners {
		allowed[owner] = true
	}
	group := router.Group("/internal/v1", func(c *gin.Context) {
		if !allowed[httpx.Owner(c)] {
			httpx.Abort(c, 403, "Forbidden", "trusted application required")
			return
		}
		c.Next()
	})
	group.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	group.POST("/executions", func(c *gin.Context) {
		var req sdk.SubmitExecutionRequest
		if !httpx.BindJSON(c, &req) {
			return
		}
		r, created, e := store.SubmitExecution(c.Request.Context(), httpx.Owner(c), c.GetHeader("Idempotency-Key"), durable.Spec{Scope: req.Spec.Scope, Query: req.Spec.Query, SystemPrompt: req.Spec.SystemPrompt, Model: req.Spec.Model, Tools: req.Spec.Tools, MaxTurns: req.Spec.MaxTurns}, time.Duration(req.TimeoutSeconds)*time.Second)
		if e != nil {
			httpx.RespondError(c, e)
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
			httpx.Abort(c, 400, "InvalidEpoch", "positive epoch required")
			return
		}
		ok, e := store.CheckExecutionLease(c.Request.Context(), httpx.Owner(c), c.Param("id"), epoch)
		if e != nil {
			httpx.RespondError(c, e)
			return
		}
		c.JSON(200, sdk.LeaseCheck{Allowed: ok})
	})
}

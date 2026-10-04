package run

import (
	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"time"
)

func RegisterExecutionRoutes(router *gin.Engine, store *durable.Store, owners []string) {
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
		var req struct {
			Spec           durable.Spec `json:"spec"`
			TimeoutSeconds int          `json:"timeoutSeconds"`
		}
		if !httpx.BindJSON(c, &req) {
			return
		}
		r, created, e := store.SubmitExecution(c.Request.Context(), httpx.Owner(c), c.GetHeader("Idempotency-Key"), req.Spec, time.Duration(req.TimeoutSeconds)*time.Second)
		if e != nil {
			httpx.RespondError(c, e)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		c.JSON(status, r)
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
		c.JSON(200, gin.H{"allowed": ok})
	})
}

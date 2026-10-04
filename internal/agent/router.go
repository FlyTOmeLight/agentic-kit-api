package agent

import (
	"net/http"
	"slices"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
)

type controller struct{ store *durable.Store }

func RegisterRoutes(v1 *gin.RouterGroup, store *durable.Store, configOwners []string) {
	h := &controller{store: store}
	agents := v1.Group("/agents")
	agents.GET("", h.listAgents)
	agents.GET("/:id", h.getAgent)
	agents.GET("/:id/versions/:number", h.getVersion)
	// Only deployment-trusted owners can configure worker destinations and credentials.
	configure := func(c *gin.Context) {
		if !slices.Contains(configOwners, httpx.Owner(c)) {
			httpx.Abort(c, http.StatusForbidden, "Forbidden", "agent configuration requires a trusted owner")
			return
		}
		c.Next()
	}
	agents.POST("", configure, h.createAgent)
	agents.PUT("/:id", configure, h.updateAgent)
	agents.POST("/:id/publish", configure, h.publishAgent)
}

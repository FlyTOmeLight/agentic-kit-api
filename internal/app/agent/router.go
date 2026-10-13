package agent

import (
	"net/http"
	"slices"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/gin-gonic/gin"
)

type controller struct{ service Service }

func RegisterRoutes(v1 *gin.RouterGroup, service Service, configOwners []string) {
	h := &controller{service: service}
	agents := v1.Group("/agents")
	agents.GET("", h.listAgents)
	agents.GET("/:id", h.getAgent)
	agents.GET("/:id/versions/:number", h.getVersion)
	// Only deployment-trusted owners can configure worker destinations and credentials.
	configure := func(c *gin.Context) {
		if !slices.Contains(configOwners, auth.UserID(c)) {
			serrors.Abort(
				c,
				http.StatusForbidden,
				"Forbidden",
				"agent configuration requires a trusted owner",
			)
			return
		}
		c.Next()
	}
	agents.POST("", configure, h.createAgent)
	agents.PUT("/:id", configure, h.updateAgent)
	agents.POST("/:id/publish", configure, h.publishAgent)
}

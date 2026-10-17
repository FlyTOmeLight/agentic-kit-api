package agent

import (
	"net/http"
	"slices"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
)

type Controller struct {
	service      Service
	configOwners []string
}

func NewController(service Service, configOwners []string) *Controller {
	return &Controller{service: service, configOwners: append([]string(nil), configOwners...)}
}

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	agents := v1.Group("/agents")
	agents.GET("", h.listAgents)
	agents.GET("/:id", h.getAgent)
	agents.GET("/:id/versions/:number", h.getVersion)
	// Only deployment-trusted owners can configure worker destinations and credentials.
	configure := func(c *core.Context) {
		if !slices.Contains(h.configOwners, auth.UserID(c)) {
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

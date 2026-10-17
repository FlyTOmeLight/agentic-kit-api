package agent

import core "github.com/FlyTOmeLight/server-go/pkg"

type Controller struct {
	service Service
}

func NewController(service Service) *Controller {
	return &Controller{service: service}
}

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	agents := v1.Group("/agents")
	agents.GET("", h.listAgents)
	agents.GET("/:id", h.getAgent)
	agents.GET("/:id/versions/:number", h.getVersion)
	agents.POST("", h.createAgent)
	agents.PUT("/:id", h.updateAgent)
	agents.POST("/:id/publish", h.publishAgent)
}

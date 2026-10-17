package session

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
)

type Controller struct{ service Service }

func NewController(service Service) *Controller { return &Controller{service: service} }

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	sessions := v1.Group("/sessions")
	sessions.POST("", h.createSession)
	sessions.GET("", h.listSessions)
	sessions.GET("/:id", h.getSession)
	sessions.GET("/:id/messages", h.listMessages)
	sessions.POST("/:id/messages", h.submitMessage)
	sessions.GET("/:id/runs", h.listSessionRuns)
}

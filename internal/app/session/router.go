package session

import (
	"github.com/gin-gonic/gin"
)

type controller struct{ service Service }

func RegisterRoutes(v1 *gin.RouterGroup, service Service) {
	h := &controller{service: service}
	sessions := v1.Group("/sessions")
	sessions.POST("", h.createSession)
	sessions.GET("", h.listSessions)
	sessions.GET("/:id", h.getSession)
	sessions.GET("/:id/messages", h.listMessages)
	sessions.POST("/:id/messages", h.submitMessage)
	sessions.GET("/:id/runs", h.listSessionRuns)
}

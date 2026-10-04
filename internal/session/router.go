package session

import (
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
)

type controller struct{ store *durable.Store }

func RegisterRoutes(v1 *gin.RouterGroup, store *durable.Store) {
	h := &controller{store: store}
	sessions := v1.Group("/sessions")
	sessions.POST("", h.createSession)
	sessions.GET("", h.listSessions)
	sessions.GET("/:id", h.getSession)
	sessions.GET("/:id/messages", h.listMessages)
	sessions.POST("/:id/messages", h.submitMessage)
	sessions.GET("/:id/runs", h.listSessionRuns)
}

package run

import (
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type controller struct {
	store *durable.Store
	redis *redis.Client
}

func RegisterRoutes(v1 *gin.RouterGroup, store *durable.Store, redisClient *redis.Client) {
	h := &controller{store: store, redis: redisClient}
	runs := v1.Group("/runs")
	runs.GET("/:id", h.get)
	runs.POST("/:id/cancel", h.cancel)
	runs.POST("/:id/answer", h.answer)
	runs.POST("/:id/resolve", h.resolve)
	runs.GET("/:id/events", h.events)
}

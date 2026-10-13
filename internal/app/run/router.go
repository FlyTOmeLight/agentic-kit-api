package run

import (
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type controller struct {
	service Service
	redis   *redis.Client
}

func RegisterRoutes(v1 *gin.RouterGroup, service Service, redisClient *redis.Client) {
	h := &controller{service: service, redis: redisClient}
	runs := v1.Group("/runs")
	runs.GET("/:id", h.get)
	runs.GET("/:id/trace", h.trace)
	runs.POST("/:id/cancel", h.cancel)
	runs.POST("/:id/answer", h.answer)
	runs.POST("/:id/resolve", h.resolve)
	runs.GET("/:id/events", h.events)
}

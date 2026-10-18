package run

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"
)

type Controller struct {
	service   Service
	redis     frameworkredis.Redis
	execution ExecutionService
}

func NewController(
	service Service,
	execution ExecutionService,
	redisClient frameworkredis.Redis,
) *Controller {
	return &Controller{service: service, execution: execution, redis: redisClient}
}

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	runs := v1.Group("/runs")
	runs.GET("/:id", h.get)
	runs.GET("/:id/trace", h.trace)
	runs.POST("/:id/cancel", h.cancel)
	runs.POST("/:id/answer", h.answer)
	runs.POST("/:id/resolve", h.resolve)
	runs.GET("/:id/events", h.events)
}

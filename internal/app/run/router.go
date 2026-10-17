package run

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/redis/go-redis/v9"
)

type Controller struct {
	service   Service
	redis     redis.UniversalClient
	execution ExecutionService
}

func NewController(
	service Service,
	execution ExecutionService,
	redisClient redis.UniversalClient,
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

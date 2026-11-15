package run

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
)

type Controller struct {
	service   Service
	redis     frameworkredis.Redis
	execution *appexec.Service
	callback  config.CallbackConfig
}

func NewController(
	service Service,
	execution *appexec.Service,
	redisClient frameworkredis.Redis,
	callback config.CallbackConfig,
) *Controller {
	return &Controller{service: service, execution: execution, redis: redisClient, callback: callback}
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

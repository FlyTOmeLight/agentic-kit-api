package run

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/redis/go-redis/v9"
)

type Controller struct {
	service   Service
	redis     *redis.Client
	execution ExecutionService
	owners    map[string]bool
}

func NewController(
	service Service,
	execution ExecutionService,
	redisClient *redis.Client,
	owners []string,
) *Controller {
	allowed := make(map[string]bool, len(owners))
	for _, owner := range owners {
		allowed[owner] = true
	}
	return &Controller{service: service, execution: execution, redis: redisClient, owners: allowed}
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

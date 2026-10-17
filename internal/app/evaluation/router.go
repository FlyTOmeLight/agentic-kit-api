package evaluation

import core "github.com/FlyTOmeLight/server-go/pkg"

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	group := v1.Group("/evaluations")
	group.POST("", h.start)
	group.GET("", h.list)
	group.GET("/:id", h.get)
	group.POST("/:id/cancel", h.cancel)
	group.GET("/:id/compare", h.compare)
}

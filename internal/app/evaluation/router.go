package evaluation

import (
	"slices"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"
)

func (h *Controller) RegisterRoutes(v1 *core.RouterGroup) {
	group := v1.Group("/evaluations")
	// A trusted gateway checks real membership before supplying scope and environment.
	group.Use(func(c *core.Context) {
		if !slices.Contains(h.owners, auth.UserID(c)) {
			serrors.Abort(c, 403, "Forbidden", "evaluation requires a trusted application")
			return
		}
		c.Next()
	})
	group.POST("", h.start)
	group.GET("", h.list)
	group.GET("/:id", h.get)
	group.POST("/:id/cancel", h.cancel)
	group.GET("/:id/compare", h.compare)
}

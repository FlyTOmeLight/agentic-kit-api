package evaluation

import (
	"slices"

	"github.com/FlyTOmeLight/server-go/auth"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/gin-gonic/gin"
)

func RegisterRoutes(v1 *gin.RouterGroup, service Service, owners []string) {
	h := &controller{service: service}
	group := v1.Group("/evaluations")
	// A trusted gateway checks real membership before supplying scope and environment.
	group.Use(func(c *gin.Context) {
		if !slices.Contains(owners, auth.UserID(c)) {
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

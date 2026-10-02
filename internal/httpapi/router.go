package httpapi

import (
	"net/http"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type runHandler struct {
	store  *durable.Store
	redis  *redis.Client
	agents map[string]config.Agent
}

// NewRouter owns HTTP concerns; durable.Store owns task transitions and transaction boundaries.
func NewRouter(store *durable.Store, redisClient *redis.Client, agents map[string]config.Agent, tokens map[string]string) *gin.Engine {
	h := &runHandler{store: store, redis: redisClient, agents: agents}
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.HandleMethodNotAllowed = true
	router.Use(requestLog(), recoverJSON(), authenticate(tokens))
	router.NoRoute(func(c *gin.Context) { abort(c, http.StatusNotFound, "NotFound", "route not found") })
	router.NoMethod(func(c *gin.Context) { abort(c, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed") })
	runs := router.Group("/v1/runs")
	runs.POST("", h.create)
	runs.GET("/:id", h.get)
	runs.POST("/:id/cancel", h.cancel)
	runs.POST("/:id/answer", h.answer)
	runs.POST("/:id/resolve", h.resolve)
	runs.GET("/:id/events", h.events)
	return router
}

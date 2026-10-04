package httpapi

import (
	"net/http"
	"slices"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type handler struct {
	store *durable.Store
	redis *redis.Client
}

// NewRouter owns HTTP concerns; durable.Store owns task transitions and transaction boundaries.
func NewRouter(store *durable.Store, redisClient *redis.Client, configOwners []string, tokens map[string]string) *gin.Engine {
	h := &handler{store: store, redis: redisClient}
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.HandleMethodNotAllowed = true
	router.Use(requestLog(), recoverJSON(), authenticate(tokens))
	router.NoRoute(func(c *gin.Context) { abort(c, http.StatusNotFound, "NotFound", "route not found") })
	router.NoMethod(func(c *gin.Context) { abort(c, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed") })
	runs := router.Group("/v1/runs")
	agents := router.Group("/v1/agents")
	agents.GET("", h.listAgents)
	agents.GET("/:id", h.getAgent)
	agents.GET("/:id/versions/:number", h.getVersion)
	// Config authors can select worker endpoints and credential references. Only
	// deployment-trusted owners receive this capability; ordinary tokens cannot.
	configure := func(c *gin.Context) {
		if !slices.Contains(configOwners, c.GetString(ownerKey)) {
			abort(c, 403, "Forbidden", "agent configuration requires a trusted owner")
			return
		}
		c.Next()
	}
	agents.POST("", configure, h.createAgent)
	agents.PUT("/:id", configure, h.updateAgent)
	agents.POST("/:id/publish", configure, h.publishAgent)
	sessions := router.Group("/v1/sessions")
	sessions.POST("", h.createSession)
	sessions.GET("", h.listSessions)
	sessions.GET("/:id", h.getSession)
	sessions.GET("/:id/messages", h.listMessages)
	sessions.POST("/:id/messages", h.create)
	sessions.GET("/:id/runs", h.listSessionRuns)
	runs.GET("/:id", h.get)
	runs.POST("/:id/cancel", h.cancel)
	runs.POST("/:id/answer", h.answer)
	runs.POST("/:id/resolve", h.resolve)
	runs.GET("/:id/events", h.events)
	return router
}

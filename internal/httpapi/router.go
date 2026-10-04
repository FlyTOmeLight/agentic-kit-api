package httpapi

import (
	"net/http"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpx"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/run"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/session"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// NewRouter assembles middleware and resource routes. Business modules own handlers.
func NewRouter(store *durable.Store, redisClient *redis.Client, configOwners []string, tokens map[string]string) *gin.Engine {
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.HandleMethodNotAllowed = true
	router.Use(requestLog(), recoverJSON(), authenticate(tokens))
	router.NoRoute(func(c *gin.Context) { httpx.Abort(c, http.StatusNotFound, "NotFound", "route not found") })
	router.NoMethod(func(c *gin.Context) {
		httpx.Abort(c, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
	})
	run.RegisterExecutionRoutes(router, store, configOwners)
	v1 := router.Group("/v1")
	agent.RegisterRoutes(v1, store, configOwners)
	session.RegisterRoutes(v1, store)
	run.RegisterRoutes(v1, store, redisClient)
	return router
}

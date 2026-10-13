package service

import (
	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/evaluation"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/run"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/session"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

// RegisterRoutes installs domain routes without starting resources or goroutines.
func registerRoutes(
	router *gin.RouterGroup,
	store *durable.Store,
	redisClient *redis.Client,
	configOwners []string,
	tokens map[string]string,
	cfg *platform.Config,
	tenant *client.Tenant,
) {
	if cfg != nil {
		platform.RegisterRoutes(router, store, *cfg, tenant)
	}
	authenticated := router.Group("", auth.BearerTokens(tokens))
	if cfg != nil {
		platform.RegisterReleaseRoutes(authenticated, store, *cfg, tenant)
	}
	run.RegisterExecutionRoutes(authenticated, store, configOwners)
	v1 := authenticated.Group("/v1")
	agent.RegisterRoutes(v1, store, configOwners)
	evaluation.RegisterRoutes(v1, store, configOwners)
	session.RegisterRoutes(v1, store)
	run.RegisterRoutes(v1, store, redisClient)
}

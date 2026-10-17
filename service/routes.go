package service

import (
	"github.com/FlyTOmeLight/server-go/auth"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/redis/go-redis/v9"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/evaluation"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/platform"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/run"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/session"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type routes struct {
	tokens     map[string]string
	agent      *agent.Controller
	evaluation *evaluation.Controller
	session    *session.Controller
	run        *run.Controller
	platform   *platform.Controller
}

func newRoutes(
	store *durable.Store,
	cache *redis.Client,
	owners []string,
	tokens map[string]string,
	cfg *platform.Config,
	tenant *client.Tenant,
) *routes {
	r := &routes{
		tokens:     tokens,
		agent:      agent.NewController(store, owners),
		evaluation: evaluation.NewController(store, owners),
		session:    session.NewController(store),
		run:        run.NewController(store, store, cache, owners),
	}
	if cfg != nil {
		r.platform = platform.NewController(store, platform.NewService(store, *cfg, tenant), *cfg, tenant)
	}
	return r
}

func (r *routes) register(router *core.RouterGroup) {
	if r.platform != nil {
		r.platform.RegisterRoutes(router)
	}
	authenticated := router.Group("", auth.BearerTokens(r.tokens))
	if r.platform != nil {
		r.platform.RegisterReleaseRoutes(authenticated)
	}
	r.run.RegisterExecutionRoutes(authenticated)
	v1 := authenticated.Group("/v1")
	r.agent.RegisterRoutes(v1)
	r.evaluation.RegisterRoutes(v1)
	r.session.RegisterRoutes(v1)
	r.run.RegisterRoutes(v1)
}

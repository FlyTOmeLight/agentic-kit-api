package service

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/run"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/session"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type routes struct {
	agent   *agent.Controller
	session *session.Controller
	run     *run.Controller
}

func newRoutes(
	store *durable.Store,
	cache frameworkredis.Redis,
) *routes {
	return &routes{
		agent:   agent.NewController(store),
		session: session.NewController(store),
		run:     run.NewController(store, store, cache),
	}
}

func (r *routes) register(router *core.RouterGroup) {
	// Identity and authorization will be supplied by the Forge tenant middleware.
	// Until it is installed, these routes have no authentication or owner context.
	authenticated := router.Group("")
	r.run.RegisterExecutionRoutes(authenticated)
	v1 := authenticated.Group("/v1")
	r.agent.RegisterRoutes(v1)
	r.session.RegisterRoutes(v1)
	r.run.RegisterRoutes(v1)
}

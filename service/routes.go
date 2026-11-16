package service

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	appmemory "github.com/FlyTOmeLight/agentic-kit-api/internal/app/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/run"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/session"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type routes struct {
	agent   *agent.Controller
	session *session.Controller
	run     *run.Controller
	memory  *appmemory.Controller
}

func newRoutes(
	store *durable.Store,
	cache frameworkredis.Redis,
	archive appmemory.Archiver,
) *routes {
	exec := appexec.NewService(store)
	var memoryCtrl *appmemory.Controller
	if store != nil {
		memorySvc := appmemory.NewService(store.DB(), archive)
		memoryCtrl = appmemory.NewController(memorySvc)
		exec = exec.WithMemoryRecaller(memorySvc)
	}
	return &routes{
		agent:   agent.NewController(store),
		session: session.NewController(session.NewService(store, exec)),
		run:     run.NewController(store, exec, cache),
		memory:  memoryCtrl,
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
	if r.memory != nil {
		r.memory.RegisterRoutes(v1)
	}
}

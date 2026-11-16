package service

import (
	core "github.com/FlyTOmeLight/server-go/pkg"
	frameworkredis "github.com/FlyTOmeLight/server-go/redis"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	appmemory "github.com/FlyTOmeLight/agentic-kit-api/internal/app/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/run"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/app/session"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/sandboxcenter"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type routes struct {
	agent        *agent.Controller
	session      *session.Controller
	run          *run.Controller
	memory       *appmemory.Controller
	sandbox      *sandboxcenter.Controller
	sandboxStore *sandboxcenter.Store
}

func newRoutes(
	store *durable.Store,
	cache frameworkredis.Redis,
	artifact client.ArtifactStore,
	sandboxStore *sandboxcenter.Store,
	sandboxProvider *sandboxcenter.Provider,
	callbackCfg config.CallbackConfig,
) *routes {
	exec := appexec.NewService(store, artifact)
	var memoryCtrl *appmemory.Controller
	if store != nil {
		memorySvc := appmemory.NewService(store.DB())
		memoryCtrl = appmemory.NewController(memorySvc)
		exec = exec.WithMemoryRecaller(memorySvc)
	}
	r := &routes{
		agent:        agent.NewController(agent.NewService(store.DB(), artifact)),
		session:      session.NewController(session.NewService(store, exec)),
		run:          run.NewController(store, exec, cache, callbackCfg),
		memory:       memoryCtrl,
		sandboxStore: sandboxStore,
	}
	if sandboxProvider != nil {
		r.sandbox = sandboxcenter.NewController(sandboxcenter.NewService(sandboxStore, sandboxProvider))
	}
	return r
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
	if r.sandbox != nil {
		r.sandbox.RegisterRoutes(v1)
	} else if r.sandboxStore != nil {
		sandboxcenter.RegisterStoreRoutes(v1, r.sandboxStore)
	}
	if r.memory != nil {
		r.memory.RegisterRoutes(v1)
	}
}

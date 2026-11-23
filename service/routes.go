package service

import (
	"net/http"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
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
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

type routes struct {
	agent           *agent.Controller
	session         *session.Controller
	run             *run.Controller
	memory          *appmemory.Controller
	memoryRecovery  pendingResumer
	sandbox         *sandboxcenter.Controller
	sandboxStore    *sandboxcenter.Store
	authentication  Authentication
	callbackEnabled bool
}

func newRoutes(
	store *durable.Store,
	cache frameworkredis.Redis,
	artifact client.ArtifactStore,
	archive appmemory.Archiver,
	sandboxStore *sandboxcenter.Store,
	sandboxProvider *sandboxcenter.Provider,
	callbackCfg config.CallbackConfig,
	authentication Authentication,
	pricing execution.PricingCatalog,
) *routes {
	exec := appexec.NewService(store, artifact).WithPricing(pricing)
	memorySvc := appmemory.NewService(store.DB(), archive).
		WithProjectReadCheck(authentication.ProjectRead).
		WithProjectWriteCheck(authentication.ProjectWrite)
	exec = exec.WithMemoryRecaller(memorySvc).WithProjectReadCheck(authentication.ProjectRead)
	r := &routes{
		agent:           agent.NewController(agent.NewService(store.DB(), artifact)),
		session:         session.NewController(session.NewService(store, exec)),
		run:             run.NewController(store, exec, cache, callbackCfg),
		memory:          appmemory.NewController(memorySvc),
		sandboxStore:    sandboxStore,
		authentication:  authentication,
		callbackEnabled: callbackCfg.Enabled,
	}
	if archive != nil {
		r.memoryRecovery = memorySvc
	}
	if sandboxProvider != nil {
		r.sandbox = sandboxcenter.NewController(sandboxcenter.NewService(sandboxStore, sandboxProvider))
	}
	return r
}

func (r *routes) register(router *core.RouterGroup) {
	if len(r.authentication.Propagation) > 0 {
		internal := router.Group("", r.authentication.Propagation...)
		internal.Use(requireTenantScope)
		r.run.RegisterExecutionRoutes(internal)
	}
	if r.callbackEnabled && len(r.authentication.Callback) > 0 {
		r.run.RegisterCallbackRoutes(router.Group("", r.authentication.Callback...))
	}
	v1 := router.Group("/v1", r.authentication.User...)
	v1.Use(requireTenantScope)
	r.agent.RegisterRoutes(v1)
	r.session.RegisterRoutes(v1)
	r.run.RegisterRoutes(v1)
	if len(r.authentication.Service) > 0 {
		sandbox := router.Group("/v1", r.authentication.Service...)
		if r.sandbox != nil {
			r.sandbox.RegisterRoutes(sandbox)
		} else if r.sandboxStore != nil {
			sandboxcenter.RegisterStoreRoutes(sandbox, r.sandboxStore)
		}
	}
	if r.memory != nil {
		r.memory.RegisterRoutes(v1)
	}
}

func requireTenantScope(c *core.Context) {
	scope, ok := access.FromContext(c.Request.Context())
	if !ok || scope.TenantID == "" || strings.TrimSpace(scope.TenantID) != scope.TenantID {
		serrors.Abort(c, http.StatusForbidden, "Forbidden", "verified tenant scope is required")
		return
	}
	c.Next()
}

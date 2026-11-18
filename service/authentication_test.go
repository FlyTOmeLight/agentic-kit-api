package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/sandboxcenter"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func TestRouteAuthenticationBoundaries(t *testing.T) {
	gate := func(name string) []core.HandlerFunc {
		return []core.HandlerFunc{func(c *core.Context) {
			c.Header("X-Test-Authentication", name)
			c.AbortWithStatus(http.StatusUnauthorized)
		}}
	}
	for _, test := range []struct {
		name                           string
		authentication                 Authentication
		callbackEnabled                bool
		propagation, sandbox, callback string
	}{
		{name: "optional routes disabled"},
		{
			name:           "propagation only",
			authentication: Authentication{Propagation: gate("propagation")},
			propagation:    "propagation",
		},
		{
			name:            "callback only",
			authentication:  Authentication{Callback: gate("callback")},
			callbackEnabled: true, callback: "callback",
		},
		{
			name:           "callback disabled despite handler",
			authentication: Authentication{Callback: gate("callback")},
		},
		{name: "callback missing authentication", callbackEnabled: true},
		{
			name: "all enabled",
			authentication: Authentication{
				Propagation: gate("propagation"), Service: gate("sandbox"), Callback: gate("callback"),
			},
			callbackEnabled: true, propagation: "propagation", sandbox: "sandbox", callback: "callback",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.authentication.User = gate("user")
			artifactServer, _ := newFakeArtifact(t)
			routes := newRoutes(
				&durable.Store{},
				nil,
				newArtifactClient(t, artifactServer),
				nil,
				sandboxcenter.NewStore(nil),
				nil,
				config.CallbackConfig{
					Enabled: test.callbackEnabled,
				},
				test.authentication,
				execution.PricingCatalog{},
			)
			router := gin.New()
			routes.register(router.Group(""))
			for _, route := range []struct{ method, path, auth string }{
				{method: "GET", path: "/v1/agents", auth: "user"},
				{method: "POST", path: "/internal/v1/executions", auth: test.propagation},
				{method: "GET", path: "/internal/v1/executions/run/lease", auth: test.propagation},
				{method: "POST", path: "/internal/v1/goals", auth: test.propagation},
				{method: "POST", path: "/internal/v1/executions/run/callback", auth: test.callback},
				{method: "GET", path: "/v1/sandboxes/sandbox", auth: test.sandbox},
			} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
				want := http.StatusUnauthorized
				if route.auth == "" {
					want = http.StatusNotFound
				}
				if response.Code != want ||
					response.Header().Get("X-Test-Authentication") != route.auth {
					t.Errorf("%s %s: status=%d authentication=%q want=%d/%q",
						route.method, route.path, response.Code,
						response.Header().Get("X-Test-Authentication"), want, route.auth)
				}
			}
		})
	}
}

func TestHTTPRoutesRequireTenantScope(t *testing.T) {
	for _, test := range []struct {
		name  string
		scope *access.Scope
		user  bool
	}{
		{name: "missing user middleware"},
		{name: "middleware without scope", user: true},
		{name: "empty scope", user: true, scope: &access.Scope{}},
		{name: "project without tenant", user: true, scope: &access.Scope{ProjectID: "project"}},
		{name: "whitespace tenant", user: true, scope: &access.Scope{TenantID: " tenant "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := func(c *core.Context) {
				if test.scope != nil {
					c.Request = c.Request.WithContext(
						access.WithScope(c.Request.Context(), *test.scope),
					)
				}
				c.Next()
			}
			authentication := Authentication{Propagation: []core.HandlerFunc{identity}}
			if test.user {
				authentication.User = []core.HandlerFunc{identity}
			}
			artifactServer, _ := newFakeArtifact(t)
			routes := newRoutes(
				&durable.Store{}, nil, newArtifactClient(t, artifactServer),
				nil, nil, nil, config.CallbackConfig{}, authentication,
				execution.PricingCatalog{},
			)
			router := gin.New()
			routes.register(router.Group(""))
			for _, route := range []struct{ method, path string }{
				{method: "GET", path: "/v1/agents"},
				{method: "GET", path: "/v1/sessions"},
				{method: "GET", path: "/v1/runs/run"},
				{method: "POST", path: "/internal/v1/executions"},
				{method: "GET", path: "/internal/v1/executions/run/lease"},
				{method: "POST", path: "/internal/v1/goals"},
			} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
				if response.Code != http.StatusForbidden {
					t.Errorf(
						"%s %s: status=%d body=%s",
						route.method,
						route.path,
						response.Code,
						response.Body,
					)
				}
			}
		})
	}
}

func TestTenantScopeGuardPreservesVerifiedScope(t *testing.T) {
	for _, scope := range []access.Scope{
		{TenantID: "tenant"},
		{TenantID: "tenant", ProjectID: "project"},
	} {
		t.Run(scope.TenantID+"/"+scope.ProjectID, func(t *testing.T) {
			router := gin.New()
			router.Use(requireTenantScope)
			router.GET("/", func(c *core.Context) {
				got, ok := access.FromContext(c.Request.Context())
				if !ok || got != scope {
					t.Errorf("scope=%+v, want=%+v", got, scope)
				}
				c.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request = request.WithContext(access.WithScope(request.Context(), scope))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		})
	}
}

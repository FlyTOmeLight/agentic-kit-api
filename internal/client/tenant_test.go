package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/constants"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
)

func testTenant(t *testing.T, handler http.HandlerFunc) *Tenant {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	tenant, err := NewTenant(server.URL, strings.Repeat("k", 32), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tenant.Close(); err != nil {
			t.Error(err)
		}
	})
	return tenant
}

func TestTenantUserScope(t *testing.T) {
	for _, test := range []struct {
		name, tenant, project string
		upstreamStatus, want  int
	}{
		{name: "tenant", tenant: "tenant", want: 204},
		{name: "project", tenant: "tenant", project: "project", want: 204},
		{name: "missing tenant", want: 400},
		{name: "denied membership", tenant: "tenant", upstreamStatus: 403, want: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tenant := testTenant(t, func(w http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/internal/v1/scope/resolve" ||
					request.Header.Get("Authorization") != "Bearer "+strings.Repeat("k", 32) {
					t.Error("unexpected Forge scope request or service credential")
				}
				var proof struct {
					APIKey    string  `json:"api_key"`
					TenantID  string  `json:"tenant_id"`
					ProjectID *string `json:"project_id"`
				}
				if err := json.NewDecoder(request.Body).Decode(&proof); err != nil {
					t.Error(err)
				}
				if proof.APIKey != "user-proof" || proof.TenantID != test.tenant {
					t.Error("original proof or tenant selection was lost")
				}
				if test.upstreamStatus != 0 {
					w.WriteHeader(test.upstreamStatus)
					_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"denied"}}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"principal": map[string]string{"id": "verified-user"},
					"user_id":   "verified-user", "tenant_id": proof.TenantID,
					"project_id": proof.ProjectID, "ownership_state": "active",
				})
			})
			router := gin.New()
			router.Group("", tenant.User()...).GET("/tenants/:tid/projects/:pid", func(c *core.Context) {
				scope, ok := access.FromContext(c.Request.Context())
				if !ok || scope.TenantID != test.tenant || scope.ProjectID != test.project ||
					auth.UserID(c) != "verified-user" {
					t.Errorf("unexpected verified scope: %+v", scope)
				}
				c.Status(204)
			})
			request := httptest.NewRequest(http.MethodGet, "/tenants/url-ignored/projects/url-ignored", nil)
			request.Header.Set("Authorization", "Bearer user-proof")
			request.Header.Set(constants.HeaderUserID, "forged-user")
			if test.tenant != "" {
				request.Header.Set(constants.HeaderTenantID, test.tenant)
			}
			if test.project != "" {
				request.Header.Set(constants.HeaderProjectID, test.project)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body)
			}
			if (calls == 0) != (test.tenant == "") || calls > 1 {
				t.Fatalf("unexpected Forge calls: %d", calls)
			}
		})
	}
}

func TestTenantServiceAndPropagation(t *testing.T) {
	for _, test := range []struct {
		name, service, tenant, user, project string
		propagation, missingCredential       bool
		upstreamStatus, want                 int
	}{
		{name: "service", service: "allowed", want: 204},
		{name: "denied service", service: "other", want: 403},
		{name: "missing credential", missingCredential: true, want: 401},
		{name: "invalid credential", upstreamStatus: 401, want: 401},
		{name: "Forge unavailable", upstreamStatus: 503, want: 503},
		{name: "propagated user", propagation: true, service: "allowed", tenant: "tenant", user: "owner", project: "project", want: 204},
		{name: "propagated tenant only scope", propagation: true, service: "allowed", tenant: "tenant", user: "owner", want: 204},
		{name: "denied propagation", propagation: true, service: "other", tenant: "tenant", user: "owner", want: 403},
		{name: "missing propagation", propagation: true, service: "allowed", want: 403},
		{name: "missing tenant", propagation: true, service: "allowed", user: "owner", want: 403},
		{name: "missing user", propagation: true, service: "allowed", tenant: "tenant", want: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tenant := testTenant(t, func(w http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/internal/v1/service/self" ||
					request.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
					t.Error("unexpected service authentication request")
				}
				if test.upstreamStatus != 0 {
					w.WriteHeader(test.upstreamStatus)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"service": test.service})
			})
			var middleware []core.HandlerFunc
			var err error
			if test.propagation {
				middleware, err = tenant.Propagation([]string{"allowed"})
			} else {
				middleware, err = tenant.Service([]string{"allowed"})
			}
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.Group("", middleware...).GET("/", func(c *core.Context) {
				scope, ok := access.FromContext(c.Request.Context())
				if test.propagation {
					if !ok || scope != (access.Scope{TenantID: test.tenant, ProjectID: test.project}) ||
						auth.UserID(c) != test.user {
						t.Errorf("unexpected propagated scope: %+v", scope)
					}
				} else if ok || auth.UserID(c) != test.service {
					t.Error("service authentication adopted a user scope")
				}
				c.Status(204)
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if !test.missingCredential {
				request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
			}
			request.Header.Set(constants.HeaderImpersonateTenantID, test.tenant)
			request.Header.Set(constants.HeaderImpersonateUserID, test.user)
			request.Header.Set(constants.HeaderImpersonateProjectID, test.project)
			request.Header.Set(constants.HeaderTenantID, "untrusted-tenant")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body)
			}
			if calls > 1 {
				t.Fatalf("service authentication repeated %d times", calls)
			}
		})
	}
}

func TestTenantDisabledInternalAuthentication(t *testing.T) {
	tenant := testTenant(t, func(http.ResponseWriter, *http.Request) {
		t.Error("disabled authentication contacted Forge")
	})
	for _, create := range []func([]string) ([]core.HandlerFunc, error){tenant.Service, tenant.Propagation} {
		handlers, err := create(nil)
		if err != nil || len(handlers) != 0 {
			t.Fatalf("empty allowlist enabled authentication: %v", err)
		}
	}
}

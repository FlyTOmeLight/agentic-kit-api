package client

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	tenantv1 "github.com/FlyTOmeLight/forge-tenant-api/sdk/go/tenantv1"
	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/constants"
	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
)

type Tenant struct {
	client   *tenantv1.Client
	services *tenantv1.ServiceAuthenticator
}

func NewTenant(endpoint, serviceAPIKey string, timeout time.Duration) (*Tenant, error) {
	client, err := tenantv1.New(tenantv1.Config{
		Endpoint: endpoint, ServiceAPIKey: serviceAPIKey, Timeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	services, err := tenantv1.NewServiceAuthenticator(tenantv1.ServiceAuthenticatorConfig{
		Endpoint: endpoint, Timeout: timeout,
	})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Tenant{client: client, services: services}, nil
}

func (t *Tenant) Close() error {
	return errors.Join(t.services.Close(), t.client.Close())
}

func (t *Tenant) CanReadProject(ctx context.Context, userID string, scope access.Scope) (bool, error) {
	result, err := t.client.CheckAccess(ctx, &tenantv1.CheckAccessRequest{
		UserID: userID, TenantID: scope.TenantID, ProjectID: scope.ProjectID, Action: tenantv1.AccessRead,
	})
	if err != nil {
		return false, err
	}
	return result.Allowed && result.Read && result.TenantID == scope.TenantID &&
		result.ProjectID == scope.ProjectID, nil
}

func (t *Tenant) CanWriteProject(ctx context.Context, userID string, scope access.Scope) (bool, error) {
	result, err := t.client.CheckAccess(ctx, &tenantv1.CheckAccessRequest{
		UserID: userID, TenantID: scope.TenantID, ProjectID: scope.ProjectID, Action: tenantv1.AccessWrite,
	})
	if err != nil {
		return false, err
	}
	return result.Allowed && result.Write && result.TenantID == scope.TenantID &&
		result.ProjectID == scope.ProjectID, nil
}

func (t *Tenant) User() []core.HandlerFunc {
	return []core.HandlerFunc{
		tenantv1.MiddlewareAuthorization(tenantv1.AuthorizationConfig{
			Client: t.client,
			Scope: func(*http.Request) (tenantv1.Scope, error) {
				return tenantv1.Scope{}, nil
			},
			RequireTenant: true,
			Action:        "",
		}),
		publishScope,
	}
}

func (t *Tenant) Service(allowedServices []string) ([]core.HandlerFunc, error) {
	if len(allowedServices) == 0 {
		return nil, nil
	}
	allowed := slices.Clone(allowedServices)
	middleware, err := tenantv1.MiddlewareServiceAuth(tenantv1.ServiceAuthConfig{
		Authenticate: t.services.Authenticate,
	})
	if err != nil {
		return nil, err
	}
	return []core.HandlerFunc{middleware, func(c *core.Context) {
		if !slices.Contains(allowed, auth.UserID(c)) {
			serrors.Abort(c, http.StatusForbidden, "Forbidden", "service is not permitted")
			return
		}
		c.Next()
	}}, nil
}

func (t *Tenant) Propagation(allowedServices []string) ([]core.HandlerFunc, error) {
	if len(allowedServices) == 0 {
		return nil, nil
	}
	allowed := slices.Clone(allowedServices)
	middleware, err := tenantv1.MiddlewareInternalPropagation(tenantv1.ServiceAuthConfig{
		Authenticate: t.services.Authenticate,
		Authorize: func(c *core.Context) error {
			if !slices.Contains(allowed, auth.UserID(c)) {
				return errors.New("service is not permitted to propagate identity")
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return []core.HandlerFunc{middleware, publishScope}, nil
}

func publishScope(c *core.Context) {
	tenantID := c.GetString(constants.KeyTenantID)
	if tenantID == "" || strings.TrimSpace(tenantID) != tenantID {
		serrors.Abort(c, http.StatusForbidden, "Forbidden", "verified tenant is required")
		return
	}
	c.Request = c.Request.WithContext(access.WithScope(c.Request.Context(), access.Scope{
		TenantID: tenantID, ProjectID: c.GetString(constants.KeyProjectID),
	}))
	c.Next()
}

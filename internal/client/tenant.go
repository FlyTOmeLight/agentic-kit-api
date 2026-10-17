package client

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/FlyTOmeLight/forge-tenant-api/sdk/go/tenantv1"
	framesdk "github.com/FlyTOmeLight/server-go/sdk"
)

// Tenant adapts platform authorization checks to the published Tenant SDK.
type Tenant struct{ client *tenantv1.Client }

func NewTenant(config framesdk.Config) (*Tenant, error) {
	if config.ProxyURL != "" {
		return nil, errors.New("published Tenant SDK does not support ProxyURL configuration")
	}
	token, err := serviceToken(config)
	if err != nil {
		return nil, err
	}
	c, err := tenantv1.New(
		tenantv1.Config{
			Endpoint:     config.Endpoint,
			ServiceToken: token,
			Timeout:      time.Duration(config.Timeout) * time.Second,
		},
	)
	if err != nil {
		return nil, err
	}
	return &Tenant{client: c}, nil
}

func (t *Tenant) Close() { _ = t.client.Close() }

func (t *Tenant) Check(ctx context.Context, path string, body, out any) int {
	raw, err := json.Marshal(body)
	if err != nil {
		return 503
	}
	var result any
	switch path {
	case "/internal/v1/session/check":
		var request tenantv1.CheckSessionRequest
		if json.Unmarshal(raw, &request) != nil {
			return 503
		}
		result, err = t.client.CheckSession(ctx, &request)
	case "/internal/v1/access/check":
		var request tenantv1.CheckAccessRequest
		if json.Unmarshal(raw, &request) != nil {
			return 503
		}
		result, err = t.client.CheckAccess(ctx, &request)
	case "/internal/v1/workspace/check":
		var request tenantv1.CheckWorkspaceRequest
		if json.Unmarshal(raw, &request) != nil {
			return 503
		}
		result, err = t.client.CheckWorkspace(ctx, &request)
	default:
		return 503
	}
	if err != nil {
		var detail *tenantv1.Error
		if errors.As(err, &detail) && (detail.Status == 401 || detail.Status == 403 || detail.Status == 404) {
			return detail.Status
		}
		return 503
	}
	raw, err = json.Marshal(result)
	if err != nil || json.Unmarshal(raw, out) != nil {
		return 503
	}
	return 200
}

package agent

import (
	"net/http/httptest"
	"testing"

	"github.com/FlyTOmeLight/server-go/constants"
	"github.com/gin-gonic/gin"
)

func TestRequestIdentityUsesOnlyVerifiedContext(t *testing.T) {
	for _, test := range []struct {
		name, user, tenant, project string
	}{
		{name: "verified project", user: "verified-user", tenant: "verified-tenant", project: "verified-project"},
		{name: "verified tenant", user: "verified-user", tenant: "verified-tenant"},
		{name: "unverified headers"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/agents/agent-1/publish", nil)
			for header, value := range map[string]string{
				"Authorization":                      "Bearer untrusted-inbound-token",
				"Cookie":                             "forge_session=untrusted-session",
				"X-CSRF-Token":                       "untrusted-csrf",
				"Origin":                             "https://untrusted.example",
				constants.HeaderTenantID:             "untrusted-tenant",
				constants.HeaderProjectID:            "untrusted-project",
				constants.HeaderImpersonateTenantID:  "untrusted-tenant",
				constants.HeaderImpersonateUserID:    "untrusted-user",
				constants.HeaderImpersonateProjectID: "untrusted-project",
				"X-Resource-User-ID":                 "untrusted-user",
			} {
				ctx.Request.Header.Set(header, value)
			}
			ctx.Set(constants.KeyUserID, test.user)
			ctx.Set(constants.KeyTenantID, test.tenant)
			ctx.Set(constants.KeyProjectID, test.project)
			ctx.Set(constants.KeyRequestID, "request-1")
			identity := RequestIdentity(ctx)
			if identity.TenantID != test.tenant || identity.PropagatedUserID != test.user ||
				identity.RequestID != "request-1" {
				t.Fatalf("unverified identity selected: %+v", identity)
			}
			if test.project == "" {
				if identity.ProjectID != nil {
					t.Fatalf("unverified project selected: %q", *identity.ProjectID)
				}
			} else if identity.ProjectID == nil || *identity.ProjectID != test.project {
				t.Fatalf("verified project lost: %+v", identity.ProjectID)
			}
		})
	}
}

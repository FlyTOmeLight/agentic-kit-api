package agent

import (
	"github.com/FlyTOmeLight/server-go/constants"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
)

// RequestIdentity passes the caller's verified identity to the artifact
// service. Until the Forge middleware is installed these keys are empty and
// the artifact client rejects the call as unavailable.
func RequestIdentity(c *core.Context) client.Identity {
	var projectID *string
	if project := c.GetString(constants.KeyProjectID); project != "" {
		projectID = &project
	}
	var resourceUserID *string
	if user := c.GetString(constants.KeyUserID); user != "" {
		resourceUserID = &user
	}
	session, _ := c.Cookie("forge_session")
	return client.Identity{
		TenantID:       c.GetString(constants.KeyTenantID),
		ProjectID:      projectID,
		ResourceUserID: resourceUserID,
		RequestID:      c.GetString(constants.KeyRequestID),
		Session:        session,
		CSRFToken:      c.GetHeader("X-CSRF-Token"),
		Origin:         c.GetHeader("Origin"),
	}
}

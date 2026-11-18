package agent

import (
	"github.com/FlyTOmeLight/server-go/constants"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
)

// RequestIdentity selects the user and scope verified by Forge middleware.
func RequestIdentity(c *core.Context) client.Identity {
	var projectID *string
	if project := c.GetString(constants.KeyProjectID); project != "" {
		projectID = &project
	}
	return client.Identity{
		TenantID:         c.GetString(constants.KeyTenantID),
		ProjectID:        projectID,
		PropagatedUserID: c.GetString(constants.KeyUserID),
		RequestID:        c.GetString(constants.KeyRequestID),
	}
}

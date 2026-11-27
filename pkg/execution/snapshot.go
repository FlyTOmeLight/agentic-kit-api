package execution

import (
	"maps"
	"slices"
	"strconv"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

// CompileRunSpec builds the immutable run snapshot from a published agent version.
func CompileRunSpec(
	config agent.AgentConfig,
	agentID string,
	version int64,
	query string,
	scope *workspace.Scope,
	filesystem *FilesystemBinding,
) Spec {
	mcpServers := slices.Clone(config.MCPServers)
	for index := range mcpServers {
		mcpServers[index].AllowedTools = slices.Clone(mcpServers[index].AllowedTools)
		mcpServers[index].ToolPolicies = maps.Clone(mcpServers[index].ToolPolicies)
	}
	return Spec{
		PricingFrozen: true,
		AgentID:       agentID,
		ConfigVersion: strconv.FormatInt(version, 10),
		Query:         query,
		SystemPrompt:  config.SystemPrompt,
		Model:         config.Model,
		Tools:         config.Tools,
		MCPServers:    mcpServers,
		Toolsets:      config.Toolsets,
		MaxTurns:      config.MaxTurns,
		Scope:         scope,
		Filesystem:    filesystem,
	}
}

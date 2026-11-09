package execution

import (
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
	return Spec{
		AgentID:       agentID,
		ConfigVersion: strconv.FormatInt(version, 10),
		Query:         query,
		SystemPrompt:  config.SystemPrompt,
		Model:         config.Model,
		Tools:         config.Tools,
		Toolsets:      config.Toolsets,
		MaxTurns:      config.MaxTurns,
		Scope:         scope,
		Filesystem:    filesystem,
	}
}

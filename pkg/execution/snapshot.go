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
	attachments ...Attachment,
) Spec {
	return Spec{
		Attachments:   slices.Clone(attachments),
		PricingFrozen: true,
		AgentID:       agentID,
		ConfigVersion: strconv.FormatInt(version, 10),
		Query:         query,
		SystemPrompt:  config.SystemPrompt,
		Model:         cloneModel(config.Model),
		Tools:         cloneTools(config.Tools),
		MCPServers:    cloneMCPServers(config.MCPServers),
		Toolsets:      slices.Clone(config.Toolsets),
		MaxTurns:      config.MaxTurns,
		SelfDelegate:  config.SelfDelegate,
		Scope:         scope,
		Filesystem:    filesystem,
	}
}

func CloneSubAgents(subAgents []SubAgentSnapshot) []SubAgentSnapshot {
	cloned := slices.Clone(subAgents)
	for index := range cloned {
		sub := &cloned[index]
		sub.PricingSnapshot = clonePointer(sub.PricingSnapshot)
		if sub.PricingSnapshot != nil {
			sub.PricingSnapshot.CachedInputPerMillion = clonePointer(
				sub.PricingSnapshot.CachedInputPerMillion,
			)
		}
		sub.Toolsets = slices.Clone(sub.Toolsets)
		sub.Model = cloneModel(sub.Model)
		sub.Tools = cloneTools(sub.Tools)
		sub.MCPServers = cloneMCPServers(sub.MCPServers)
	}
	return cloned
}

func cloneModel(model agent.ModelSpec) agent.ModelSpec {
	model.InputModalities = slices.Clone(model.InputModalities)
	model.Temperature = clonePointer(model.Temperature)
	model.TopP = clonePointer(model.TopP)
	model.MaxTokens = clonePointer(model.MaxTokens)
	model.Stream = clonePointer(model.Stream)
	return model
}

func clonePointer[Value any](ptr *Value) *Value {
	if ptr == nil {
		return nil
	}
	value := *ptr
	return &value
}

func cloneTools(tools []agent.ToolSpec) []agent.ToolSpec {
	cloned := slices.Clone(tools)
	for index := range cloned {
		cloned[index].Parameters = slices.Clone(cloned[index].Parameters)
	}
	return cloned
}

func cloneMCPServers(servers []agent.MCPServerSpec) []agent.MCPServerSpec {
	cloned := slices.Clone(servers)
	for index := range cloned {
		cloned[index].AllowedTools = slices.Clone(cloned[index].AllowedTools)
		cloned[index].ToolPolicies = maps.Clone(cloned[index].ToolPolicies)
	}
	return cloned
}

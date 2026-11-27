package execution

import (
	"reflect"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

func TestCompileRunSpecFreezesMCPServers(t *testing.T) {
	config := agent.AgentConfig{
		MCPServers: []agent.MCPServerSpec{{
			Name:         "catalog",
			URL:          "https://mcp.test/tools",
			TokenEnv:     "CATALOG_TOKEN",
			AllowedTools: []string{"lookup"},
			ToolPolicies: map[string]agent.Policy{"lookup": agent.ReadOnly},
			ForwardScope: true,
		}},
	}
	spec := CompileRunSpec(config, "agent", 1, "query", nil, nil)
	config.MCPServers[0].URL = "https://changed.test/tools"
	config.MCPServers[0].AllowedTools[0] = "changed"
	config.MCPServers[0].ToolPolicies["lookup"] = agent.Unsafe

	want := []agent.MCPServerSpec{{
		Name:         "catalog",
		URL:          "https://mcp.test/tools",
		TokenEnv:     "CATALOG_TOKEN",
		AllowedTools: []string{"lookup"},
		ToolPolicies: map[string]agent.Policy{"lookup": agent.ReadOnly},
		ForwardScope: true,
	}}
	if !reflect.DeepEqual(spec.MCPServers, want) {
		t.Fatalf("MCPServers = %+v, want %+v", spec.MCPServers, want)
	}
}

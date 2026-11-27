package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var (
	mcpServerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)
	mcpToolNamePattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	subAgentNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	credentialEnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

func validCredentialEnv(name string) bool {
	switch name {
	case "":
		return true
	case "DATABASE_URL", "SANDBOX_CENTER_TOKEN", "REDIS_PASSWORD", "FORGE_SERVICE_API_KEY",
		"TENANT_SERVICE_API_KEY", "AGENT_ARTIFACT_SERVICE_API_KEY", "CALLBACK_SIGNING_SECRET":
		return false
	default:
		return credentialEnvPattern.MatchString(name)
	}
}

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil &&
		u.Fragment == ""
}

// ValidateAgentConfig applies generic model, tool and policy rules. It does
// not classify configurations by product scenario.
func ValidateAgentConfig(c AgentConfig) error {
	model := c.Model
	maxStaticTools := 127 // Reserve a slot for read_step_result.
	if len(c.SubAgents) > 0 || c.SelfDelegate {
		// Reserve slots for built-in delegate_subagent and read_step_result tools.
		maxStaticTools = 126
	}
	if strings.TrimSpace(model.Name) == "" || !validEndpoint(model.BaseURL) ||
		!validCredentialEnv(model.APIKeyEnv) || model.ContextWindow < 0 ||
		(model.MaxTokens != nil && *model.MaxTokens <= 0) ||
		(model.ContextWindow > 0 &&
			(model.MaxTokens == nil || int64(*model.MaxTokens) >= model.ContextWindow)) ||
		c.MaxTurns < 1 || c.MaxTurns > 100 ||
		len(c.Tools) > maxStaticTools {
		return ErrInvalidConfig
	}
	seenModalities := make(map[string]bool, len(model.InputModalities))
	for _, kind := range model.InputModalities {
		switch kind {
		case "text", "image", "video":
		default:
			return ErrInvalidConfig
		}
		if seenModalities[kind] {
			return ErrInvalidConfig
		}
		seenModalities[kind] = true
	}
	seenSets := map[string]bool{}
	for _, set := range c.Toolsets {
		if set != "filesystem" || seenSets[set] {
			return ErrInvalidConfig
		}
		seenSets[set] = true
	}
	seen := map[string]bool{}
	for _, t := range c.Tools {
		var params map[string]any
		if strings.TrimSpace(t.Name) == "" || strings.HasPrefix(t.Name, MCPToolPrefix) ||
			t.Name == DelegateSubAgentToolName || t.Name == ReadStepResultToolName ||
			seen[t.Name] || json.Unmarshal(t.Parameters, &params) != nil ||
			params == nil ||
			params["type"] != "object" {
			return ErrInvalidConfig
		}
		seen[t.Name] = true
		if !validPolicy(t.Policy) || !validCredentialEnv(t.TokenEnv) {
			return ErrInvalidConfig
		}
		switch t.Kind {
		case "", "HTTP":
		default:
			return ErrInvalidConfig
		}
		if t.Name == "ask_for_clarification" {
			if t.Policy != ReadOnly {
				return ErrInvalidConfig
			}
		} else if !validEndpoint(t.URL) {
			return ErrInvalidConfig
		}
	}
	if len(c.SubAgents) > 10 {
		return ErrInvalidConfig
	}
	subNames := make(map[string]bool, len(c.SubAgents))
	for _, sub := range c.SubAgents {
		agentID := strings.TrimSpace(sub.AgentID)
		if !subAgentNamePattern.MatchString(sub.Name) || sub.Name == "self" || subNames[sub.Name] ||
			strings.TrimSpace(sub.Description) == "" || len(sub.Description) > 1024 ||
			agentID == "" || len(agentID) > 128 || agentID != sub.AgentID || sub.Version < 0 {
			return ErrInvalidConfig
		}
		subNames[sub.Name] = true
	}
	return validateMCPServers(c.MCPServers)
}

func validPolicy(policy Policy) bool {
	return policy == ReadOnly || policy == Idempotent || policy == Unsafe
}

func validMCPToolName(serverName, toolName string) bool {
	return mcpToolNamePattern.MatchString(toolName) && len(FormatMCPToolName(serverName, toolName)) <= 64
}

func validateMCPServers(servers []MCPServerSpec) error {
	if len(servers) > 16 {
		return ErrInvalidConfig
	}
	seen := make(map[string]bool, len(servers))
	for _, server := range servers {
		if !mcpServerNamePattern.MatchString(server.Name) || seen[server.Name] || !validEndpoint(server.URL) ||
			!validCredentialEnv(server.TokenEnv) ||
			!validPolicy(server.EffectiveDefaultPolicy()) || len(server.AllowedTools) > 64 {
			return ErrInvalidConfig
		}
		seen[server.Name] = true
		allowed := make(map[string]bool, len(server.AllowedTools))
		for _, toolName := range server.AllowedTools {
			if !validMCPToolName(server.Name, toolName) || allowed[toolName] {
				return ErrInvalidConfig
			}
			allowed[toolName] = true
		}
		for toolName, policy := range server.ToolPolicies {
			if !validMCPToolName(server.Name, toolName) || !validPolicy(policy) ||
				(len(allowed) > 0 && !allowed[toolName]) {
				return ErrInvalidConfig
			}
		}
	}
	return nil
}

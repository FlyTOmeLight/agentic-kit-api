package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	mcpServerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)
	mcpToolNamePattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil &&
		u.Fragment == ""
}

// ValidateAgentConfig applies generic model, tool and policy rules. It does
// not classify configurations by product scenario.
func ValidateAgentConfig(c AgentConfig) error {
	model := c.Model
	if strings.TrimSpace(model.Name) == "" || !validEndpoint(model.BaseURL) || model.ContextWindow < 0 ||
		(model.MaxTokens != nil && *model.MaxTokens <= 0) ||
		(model.ContextWindow > 0 &&
			(model.MaxTokens == nil || int64(*model.MaxTokens) >= model.ContextWindow)) ||
		c.MaxTurns < 1 || c.MaxTurns > 100 ||
		len(c.Tools) > 128 {
		return ErrInvalidConfig
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
			seen[t.Name] || json.Unmarshal(t.Parameters, &params) != nil ||
			params == nil ||
			params["type"] != "object" {
			return ErrInvalidConfig
		}
		seen[t.Name] = true
		if !validPolicy(t.Policy) {
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
			strings.ContainsFunc(server.TokenEnv, unicode.IsSpace) ||
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

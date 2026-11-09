package agent

import (
	"encoding/json"
	"net/url"
	"strings"
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
		if strings.TrimSpace(t.Name) == "" || seen[t.Name] || json.Unmarshal(t.Parameters, &params) != nil ||
			params == nil ||
			params["type"] != "object" {
			return ErrInvalidConfig
		}
		seen[t.Name] = true
		if t.Policy != ReadOnly && t.Policy != Idempotent && t.Policy != Unsafe {
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
	return nil
}

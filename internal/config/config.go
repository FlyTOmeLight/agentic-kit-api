package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
)

type Agent struct {
	ID     string       `json:"id"`
	Owners []string     `json:"owners"`
	Spec   durable.Spec `json:"spec"`
}

func LoadAgents(path string) (map[string]Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []Agent
	if err = json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	result := map[string]Agent{}
	for _, a := range list {
		if a.ID == "" || len(a.Owners) == 0 || a.Spec.ConfigVersion == "" || a.Spec.Model.Name == "" || a.Spec.MaxTurns < 1 || a.Spec.MaxTurns > 100 {
			return nil, fmt.Errorf("invalid agent configuration")
		}
		if _, ok := result[a.ID]; ok {
			return nil, fmt.Errorf("duplicate agent %s", a.ID)
		}
		if err = validateURL(a.Spec.Model.BaseURL); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, t := range a.Spec.Tools {
			if t.Name == "" || seen[t.Name] || !json.Valid(t.Parameters) {
				return nil, fmt.Errorf("invalid or duplicate tool")
			}
			seen[t.Name] = true
			switch t.Kind {
			case "", "HTTP":
			case "AppForgeMCP", "AppForgeOpenAPI":
				if t.ResourceID == "" || t.ToolID == "" {
					return nil, fmt.Errorf("AppForge tool needs resourceID and toolID")
				}
				if t.Policy == durable.Idempotent {
					return nil, fmt.Errorf("AppForge tools have no verified downstream idempotency contract; use ReadOnly or Unsafe")
				}
			default:
				return nil, fmt.Errorf("unsupported tool kind: %s", t.Kind)
			}
			if t.Policy != durable.ReadOnly && t.Policy != durable.Idempotent && t.Policy != durable.Unsafe {
				return nil, fmt.Errorf("tool retry policy must be explicit")
			}
			if t.Name == "ask_for_clarification" {
				if t.Policy != durable.ReadOnly {
					return nil, fmt.Errorf("clarification policy must be ReadOnly")
				}
			} else if err = validateURL(t.URL); err != nil {
				return nil, err
			}
		}
		a.Spec.AgentID = a.ID
		result[a.ID] = a
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no agents configured")
	}
	return result, nil
}
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("invalid endpoint URL")
	}
	return nil
}

// Tokens are deployment configuration. User identity is never trusted from request headers or bodies.
func LoadTokens(raw string) (map[string]string, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("API_TOKENS_JSON is required")
	}
	for token, owner := range m {
		if len(token) < 24 || strings.TrimSpace(owner) == "" {
			return nil, fmt.Errorf("tokens must be at least 24 characters and have an owner")
		}
	}
	return m, nil
}

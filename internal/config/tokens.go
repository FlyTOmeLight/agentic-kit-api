package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

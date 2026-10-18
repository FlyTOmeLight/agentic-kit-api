// Package agent holds Agent asset values and pure validation rules. Values
// here carry the public JSON shape; HTTP DTOs and persistence reuse them.
package agent

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrInvalidConfig rejects a configuration that violates generic asset rules.
var ErrInvalidConfig = errors.New("invalid agent config")

// Policy decides how a tool call may be replayed after an uncertain outcome.
type Policy string

const (
	ReadOnly   Policy = "ReadOnly"
	Idempotent Policy = "Idempotent"
	Unsafe     Policy = "Unsafe"
)

type ModelSpec struct {
	Temperature *float32 `json:"temperature,omitempty"`
	TopP        *float32 `json:"topP,omitempty"`
	MaxTokens   *int     `json:"maxTokens,omitempty"`
	Name        string   `json:"name"`
	BaseURL     string   `json:"baseURL"`
	APIKeyEnv   string   `json:"apiKeyEnv"`
}

type ToolSpec struct {
	EndRun         bool            `json:"endRun,omitempty"`
	ReturnDirectly bool            `json:"returnDirectly,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	ResourceID     string          `json:"resourceID,omitempty"`
	ToolID         string          `json:"toolID,omitempty"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Parameters     json.RawMessage `json:"parameters"`
	URL            string          `json:"url,omitempty"`
	TokenEnv       string          `json:"tokenEnv,omitempty"`
	Policy         Policy          `json:"policy"`
}

type AgentConfig struct {
	Toolsets     []string   `json:"toolsets,omitempty"`
	SystemPrompt string     `json:"systemPrompt"`
	Model        ModelSpec  `json:"model"`
	Tools        []ToolSpec `json:"tools,omitempty"`
	MaxTurns     int        `json:"maxTurns"`
}

type Agent struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Draft            AgentConfig `json:"draft"`
	Revision         int64       `json:"revision"`
	PublishedVersion int64       `json:"publishedVersion"`
	CreatedAt        time.Time   `json:"createdAt"`
	UpdatedAt        time.Time   `json:"updatedAt"`
}

type AgentVersion struct {
	AgentID   string      `json:"agentID"`
	Number    int64       `json:"number"`
	Config    AgentConfig `json:"config"`
	CreatedAt time.Time   `json:"createdAt"`
}

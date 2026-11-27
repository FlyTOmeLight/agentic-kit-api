// Package agent holds Agent asset values and pure validation rules. Values
// here carry the public JSON shape; HTTP DTOs and persistence reuse them.
package agent

import (
	"encoding/json"
	"errors"
	"slices"
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
	InputModalities []string `json:"inputModalities,omitempty"`
	Temperature     *float32 `json:"temperature,omitempty"`
	TopP            *float32 `json:"topP,omitempty"`
	MaxTokens       *int     `json:"maxTokens,omitempty"`
	Stream          *bool    `json:"stream,omitempty"`
	ContextWindow   int64    `json:"contextWindow,omitempty"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"baseURL"`
	APIKeyEnv       string   `json:"apiKeyEnv"`
}

// Streaming reports whether model calls should use upstream SSE streaming.
// A nil Stream defaults to true for backward compatibility with P0-1 live deltas.
func (m ModelSpec) Streaming() bool {
	return m.Stream == nil || *m.Stream
}

// SupportsInputModality reports which input modalities the model accepts.
func (m ModelSpec) SupportsInputModality(kind string) bool {
	if len(m.InputModalities) == 0 {
		return kind == "text" || kind == "image"
	}
	return slices.Contains(m.InputModalities, kind)
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

const (
	MCPToolPrefix            = "mcp__"
	DelegateSubAgentToolName = "delegate_subagent"
	ReadStepResultToolName   = "read_step_result"
)

func FormatMCPToolName(serverName, toolName string) string {
	return MCPToolPrefix + serverName + "__" + toolName
}

type MCPServerSpec struct {
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	TokenEnv      string            `json:"tokenEnv,omitempty"`
	DefaultPolicy Policy            `json:"defaultPolicy,omitempty"`
	AllowedTools  []string          `json:"allowedTools,omitempty"`
	ToolPolicies  map[string]Policy `json:"toolPolicies,omitempty"`
	ForwardScope  bool              `json:"forwardScope,omitempty"`
}

func (s MCPServerSpec) EffectiveDefaultPolicy() Policy {
	if s.DefaultPolicy == "" {
		return Unsafe
	}
	return s.DefaultPolicy
}

func (s MCPServerSpec) PolicyFor(toolName string) Policy {
	if policy, ok := s.ToolPolicies[toolName]; ok {
		return policy
	}
	return s.EffectiveDefaultPolicy()
}

type SubAgentSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AgentID     string `json:"agentID"`
	Version     int64  `json:"version,omitempty"`
}

type AgentConfig struct {
	Toolsets     []string        `json:"toolsets,omitempty"`
	SystemPrompt string          `json:"systemPrompt"`
	Model        ModelSpec       `json:"model"`
	Tools        []ToolSpec      `json:"tools,omitempty"`
	MCPServers   []MCPServerSpec `json:"mcpServers,omitempty"`
	MaxTurns     int             `json:"maxTurns"`
	SubAgents    []SubAgentSpec  `json:"subAgents,omitempty"`
	SelfDelegate bool            `json:"selfDelegate,omitempty"`
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

// Package contextbuilder turns frozen conversation and memory materials into
// one normalized model input. It performs no I/O and does not import durable,
// memory, HTTP or Eino types.
package contextbuilder

import "encoding/json"

const (
	PolicyVersionV1 = "context-v1"
	PolicyVersionV2 = "context-v2"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is the builder's normalized message shape. ToolCalls and ToolCallID
// keep the assistant/tool pairing required by model protocols.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallID,omitempty"`
}

// ToolDefinition is counted as required protocol input. It is not rendered
// into the message list; the model adapter passes the real tool schema.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type SourceKind string

const (
	SourceMemory  SourceKind = "Memory"
	SourceHistory SourceKind = "History"
)

// Source identifies one frozen material for stable ordering and omission
// reporting. Version is the frozen content reference, not a mutable pointer.
type Source struct {
	Kind     SourceKind `json:"kind"`
	ID       string     `json:"id"`
	Revision int64      `json:"revision,omitempty"`
	Version  string     `json:"version,omitempty"`
}

// ContextBlock is one authorized memory material. Higher Priority values are
// kept first when memory must be trimmed.
type ContextBlock struct {
	Source   Source `json:"source"`
	Content  string `json:"content"`
	Priority int    `json:"priority"`
}

// MessageGroup is a complete history turn. It may contain several
// assistant/tool cycles, but every tool call must have its result inside the
// same group.
type MessageGroup struct {
	Source   Source    `json:"source"`
	Messages []Message `json:"messages"`
}

// FrozenMaterials are the inputs fixed when the Run was accepted. History is
// ordered oldest to newest and each group is atomic.
type FrozenMaterials struct {
	SystemInstruction string           `json:"systemInstruction"`
	Tools             []ToolDefinition `json:"tools,omitempty"`
	Memory            []ContextBlock   `json:"memory,omitempty"`
	History           []MessageGroup   `json:"history,omitempty"`
}

// Policy versions composition, ordering and omission rules.
type Policy struct {
	Version string `json:"version"`
}

// Budget freezes the input limits and counting policy for one Run. The input
// token limit is the smaller of MaxInputTokens and ContextWindow minus
// OutputReserve when both are set. Zero means that dimension is unconstrained.
type Budget struct {
	ContextWindow          int64  `json:"contextWindow,omitempty"`
	OutputReserve          int64  `json:"outputReserve,omitempty"`
	MaxInputTokens         int64  `json:"maxInputTokens,omitempty"`
	MaxInputBytes          int64  `json:"maxInputBytes,omitempty"`
	ProtocolOverheadTokens int64  `json:"protocolOverheadTokens,omitempty"`
	CounterVersion         string `json:"counterVersion,omitempty"`
	MaxToolResultBytes     int64  `json:"maxToolResultBytes,omitempty"`
}

func (b Budget) InputTokenLimit() int64 {
	limit := b.MaxInputTokens
	if b.ContextWindow > 0 {
		windowLimit := b.ContextWindow - b.OutputReserve
		if limit == 0 || windowLimit < limit {
			limit = windowLimit
		}
	}
	return limit
}

func (b Budget) InputByteLimit() int64 { return b.MaxInputBytes }

// Counter measures canonical text under the frozen CounterVersion. A model
// adapter implements it; the builder never assumes bytes equal tokens.
type Counter interface {
	Count(text string) int64
}

type BuildInput struct {
	FrozenMaterials FrozenMaterials
	CurrentTurn     []Message
	Policy          Policy
	Budget          Budget
	Counter         Counter
}

type Usage struct {
	InputTokens   int64 `json:"inputTokens"`
	InputBytes    int64 `json:"inputBytes"`
	TokenLimit    int64 `json:"tokenLimit,omitempty"`
	ByteLimit     int64 `json:"byteLimit,omitempty"`
	OutputReserve int64 `json:"outputReserve,omitempty"`
}

// OmitBudget is the only omission reason in the first version.
const OmitBudget = "Budget"

type OmittedSource struct {
	Source Source `json:"source"`
	Reason string `json:"reason"`
}

type BuildResult struct {
	Messages         []Message       `json:"messages"`
	Usage            Usage           `json:"usage"`
	OmittedSources   []OmittedSource `json:"omittedSources,omitempty"`
	CollapsedSources []Source        `json:"collapsedSources,omitempty"`
}

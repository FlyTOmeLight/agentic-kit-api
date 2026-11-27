package agenticv1

import (
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

type Status = execution.Status

const (
	Queued               = execution.Queued
	Running              = execution.Running
	WaitingClarification = execution.WaitingClarification
	NeedsReview          = execution.NeedsReview
	Completed            = execution.Completed
	Failed               = execution.Failed
	Canceled             = execution.Canceled
)

type Policy = agent.Policy

const (
	ReadOnly   = agent.ReadOnly
	Idempotent = agent.Idempotent
	Unsafe     = agent.Unsafe
)

type Message = execution.Message

type AttachmentKind = execution.AttachmentKind

type Attachment = execution.Attachment

type FileArtifact = execution.FileArtifact

const (
	AttachmentImage    = execution.AttachmentImage
	AttachmentDocument = execution.AttachmentDocument
	AttachmentAudio    = execution.AttachmentAudio
	AttachmentVideo    = execution.AttachmentVideo
	AttachmentFile     = execution.AttachmentFile
)

type ToolCall = execution.ToolCall

type ModelSpec = agent.ModelSpec

type ToolSpec = agent.ToolSpec

type MCPServerSpec = agent.MCPServerSpec

type SubAgentSpec = agent.SubAgentSpec

type SubAgentSnapshot = execution.SubAgentSnapshot

type Run = execution.Run

// RunTrace is a replayable page of public execution events, never a checkpoint.
type RunTrace = execution.RunTrace

type Event = execution.Event

type StreamDelta = execution.StreamDelta

const (
	EventModelReasoningDelta = execution.EventModelReasoningDelta
	EventModelContentDelta   = execution.EventModelContentDelta
)

type SupplyResultRequest struct {
	StepID string `json:"stepID" binding:"required,max=128"`
	Result string `json:"result" binding:"required"`
}

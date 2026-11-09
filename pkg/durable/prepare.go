package durable

import (
	"context"
	"net/url"
	"strings"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil &&
		u.Fragment == ""
}

// PrepareChatSubmission resolves the published agent version and conversation history.
func PrepareChatSubmission(
	ctx context.Context,
	tx *gorm.DB,
	facts execution.SubmissionFacts,
) (execution.PreparedSubmission, error) {
	var agentRow table.Agent
	if err := tx.WithContext(ctx).Where("owner_id = ? AND id = ?", facts.Owner, facts.SessionAgentID).
		Take(&agentRow).Error; err != nil {
		return execution.PreparedSubmission{}, recordError(err)
	}
	if agentRow.PublishedVersion == 0 {
		return execution.PreparedSubmission{}, ErrConflict
	}
	var version table.AgentVersion
	if err := tx.WithContext(ctx).Where("agent_id = ? AND number = ?", agentRow.ID, agentRow.PublishedVersion).
		Take(&version).Error; err != nil {
		return execution.PreparedSubmission{}, err
	}
	if len(version.Config.Toolsets) > 0 {
		return execution.PreparedSubmission{}, ErrInvalid
	}
	spec := execution.CompileRunSpec(version.Config, agentRow.ID, version.Number, facts.Query, nil, nil)
	history, err := loadConversationHistory(tx, facts.Owner, facts.SessionID)
	if err != nil {
		return execution.PreparedSubmission{}, err
	}
	spec.History = legacyHistory(history)
	snapshot, err := freezeContext(spec, history)
	if err != nil {
		return execution.PreparedSubmission{}, err
	}
	return execution.PreparedSubmission{
		Spec:            spec,
		UserMessage:     execution.Message{Role: "user", Content: facts.Query},
		ContextSnapshot: snapshot,
	}, nil
}

// PrepareApplicationSubmission resolves an explicit published version and optional filesystem binding.
func PrepareApplicationSubmission(
	ctx context.Context,
	tx *gorm.DB,
	facts execution.SubmissionFacts,
) (execution.PreparedSubmission, error) {
	input := facts.ExecutionSpec
	var agentRow table.Agent
	if err := tx.WithContext(ctx).Where("owner_id = ? AND id = ?", facts.Owner, input.AgentID).
		Take(&agentRow).Error; err != nil {
		return execution.PreparedSubmission{}, recordError(err)
	}
	var version table.AgentVersion
	if err := tx.WithContext(ctx).Where("agent_id = ? AND number = ?", agentRow.ID, input.AgentVersion).
		Take(&version).Error; err != nil {
		return execution.PreparedSubmission{}, recordError(err)
	}
	spec := execution.CompileRunSpec(
		version.Config,
		agentRow.ID,
		version.Number,
		input.Query,
		input.Scope,
		input.Filesystem,
	)
	if len(spec.Toolsets) > 0 {
		if spec.Filesystem == nil || spec.Filesystem.Provider != "AIO" ||
			!validEndpoint(spec.Filesystem.URL) ||
			spec.Filesystem.TokenEnv == "" ||
			!strings.HasPrefix(spec.Filesystem.Workspace, "/") {
			return execution.PreparedSubmission{}, ErrInvalid
		}
	} else if spec.Filesystem != nil {
		return execution.PreparedSubmission{}, ErrInvalid
	}
	history, err := loadConversationHistory(tx, facts.Owner, facts.SessionID)
	if err != nil {
		return execution.PreparedSubmission{}, err
	}
	spec.History = legacyHistory(history)
	snapshot, err := freezeContext(spec, history)
	if err != nil {
		return execution.PreparedSubmission{}, err
	}
	return execution.PreparedSubmission{
		Spec:            spec,
		UserMessage:     execution.Message{Role: "user", Content: input.Query},
		ContextSnapshot: snapshot,
	}, nil
}

// loadConversationHistory returns complete stored turns oldest first. A Run is
// one turn: its User message, assistant decisions and paired tool results stay
// together. Incomplete turns are omitted rather than injecting orphan calls.
func loadConversationHistory(tx *gorm.DB, owner, session string) ([]contextbuilder.MessageGroup, error) {
	var runs []table.Run
	if err := tx.Model(&table.Run{}).
		Select("id").
		Where("owner_id = ? AND session_id = ?", owner, session).
		Order("created_at, id").
		Find(&runs).
		Error; err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	ids := make([]string, len(runs))
	for i := range runs {
		ids[i] = runs[i].ID
	}
	var stored []table.Message
	if err := tx.Where("session_id = ? AND run_id IN ?", session, ids).
		Order("seq").
		Find(&stored).
		Error; err != nil {
		return nil, err
	}
	byRun := make(map[string][]execution.Message, len(runs))
	for _, message := range stored {
		byRun[message.RunID] = append(byRun[message.RunID], message.Message)
	}
	groups := make([]contextbuilder.MessageGroup, 0, len(runs))
	for _, run := range runs {
		if group, ok := completeHistoryGroup(run.ID, byRun[run.ID]); ok {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

func completeHistoryGroup(runID string, messages []execution.Message) (contextbuilder.MessageGroup, bool) {
	if len(messages) == 0 || messages[0].Role != "user" {
		return contextbuilder.MessageGroup{}, false
	}
	group := contextbuilder.MessageGroup{
		Source:   contextbuilder.Source{Kind: contextbuilder.SourceHistory, ID: runID},
		Messages: make([]contextbuilder.Message, len(messages)),
	}
	for i, message := range messages {
		group.Messages[i] = contextbuilder.Message{
			Role:       contextbuilder.Role(message.Role),
			Content:    message.Content,
			ToolCalls:  contextToolCalls(message.ToolCalls),
			ToolCallID: message.ToolCallID,
		}
	}
	if err := contextbuilder.ValidateGroup(group.Messages); err != nil {
		return contextbuilder.MessageGroup{}, false
	}
	for index := range group.Messages {
		message := &group.Messages[index]
		if message.ToolCallID != "" {
			message.ToolCallID = runID + ":" + message.ToolCallID
		}
		for callIndex := range message.ToolCalls {
			message.ToolCalls[callIndex].ID = runID + ":" + message.ToolCalls[callIndex].ID
		}
	}
	return group, true
}

// legacyHistory keeps the flat user/final view consumed by the current worker.
// A group without a final assistant reply contributes nothing to the legacy
// view; tool calls and tool results live only in ContextSnapshot until the
// worker adapter switches to building model input from that snapshot.
func legacyHistory(groups []contextbuilder.MessageGroup) []execution.Message {
	var history []execution.Message
	for _, group := range groups {
		var user *execution.Message
		var final *execution.Message
		for _, message := range group.Messages {
			switch {
			case message.Role == contextbuilder.RoleUser:
				value := execution.Message{Role: string(message.Role), Content: message.Content}
				user = &value
			case message.Role == contextbuilder.RoleAssistant && len(message.ToolCalls) == 0:
				value := execution.Message{Role: string(message.Role), Content: message.Content}
				final = &value
			}
		}
		if user != nil && final != nil {
			history = append(history, *user, *final)
		}
	}
	return history
}

func freezeContext(spec execution.Spec, history []contextbuilder.MessageGroup) (execution.ContextSnapshot, error) {
	budget := contextbuilder.Budget{MaxInputBytes: 128 * 1024, MaxToolResultBytes: 16 * 1024}
	if spec.Model.ContextWindow > 0 {
		if spec.Model.MaxTokens == nil {
			return execution.ContextSnapshot{}, ErrInvalid
		}
		budget.ContextWindow = spec.Model.ContextWindow
		budget.OutputReserve = int64(*spec.Model.MaxTokens)
		budget.MaxInputBytes = 0
		budget.ProtocolOverheadTokens = 256
		budget.CounterVersion = contextbuilder.ByteEstimateVersion
		budget.MaxToolResultBytes = max(128, min(budget.MaxToolResultBytes, budget.InputTokenLimit()/4))
	}
	snapshot := contextbuilder.Snapshot{
		MemoryMode: contextbuilder.MemoryOff,
		Materials: contextbuilder.FrozenMaterials{
			SystemInstruction: spec.SystemPrompt,
			Tools:             contextTools(spec.Tools),
			History:           history,
		},
		Policy: contextbuilder.Policy{
			Version:               contextbuilder.PolicyVersionV3,
			KeepRecentToolResults: contextbuilder.DefaultKeepRecentToolResults,
		},
		Budget: budget,
	}
	counter, err := contextbuilder.ResolveCounter(budget.CounterVersion)
	if err != nil {
		return execution.ContextSnapshot{}, err
	}
	if _, err = contextbuilder.Build(contextbuilder.BuildInput{
		FrozenMaterials: snapshot.Materials,
		CurrentTurn:     []contextbuilder.Message{{Role: contextbuilder.RoleUser, Content: spec.Query}},
		Policy:          snapshot.Policy,
		Budget:          budget,
		Counter:         counter,
	}); err != nil {
		return execution.ContextSnapshot{}, err
	}
	envelope, err := contextbuilder.EncodeSnapshot(snapshot)
	if err != nil {
		return execution.ContextSnapshot{}, err
	}
	return execution.ContextSnapshot{
		Format:        envelope.Format,
		PolicyVersion: envelope.PolicyVersion,
		Digest:        envelope.Digest,
		Payload:       envelope.Payload,
	}, nil
}

func contextTools(tools []agent.ToolSpec) []contextbuilder.ToolDefinition {
	definitions := make([]contextbuilder.ToolDefinition, len(tools))
	for i, tool := range tools {
		definitions[i] = contextbuilder.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		}
	}
	return definitions
}

func contextToolCalls(calls []execution.ToolCall) []contextbuilder.ToolCall {
	converted := make([]contextbuilder.ToolCall, len(calls))
	for i, call := range calls {
		converted[i] = contextbuilder.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		}
	}
	return converted
}

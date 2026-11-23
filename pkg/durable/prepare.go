package durable

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// MemoryRecaller reads active memory entries inside the submission
// transaction. Implementations live outside pkg/durable; a nil recaller
// keeps every submission MemoryOff.
type MemoryRecaller interface {
	RecallTx(
		ctx context.Context,
		tx *gorm.DB,
		scope memory.Scope,
		audience memory.OutputAudience,
		agentID string,
		query string,
		filter memory.RecallFilter,
	) ([]memory.Entry, error)
}

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil &&
		u.Fragment == ""
}

// PrepareChatSubmission compiles a chat run snapshot from a prefetched
// published config; the closure only reads conversation history inside the
// caller transaction and never queries agent tables or remote stores. The
// given recaller is used when the submission selects Recall; nil keeps every
// submission MemoryOff.
func PrepareChatSubmission(
	config agent.AgentConfig,
	agentID string,
	version int64,
	recaller MemoryRecaller,
) PrepareSubmission {
	return func(
		ctx context.Context,
		tx *gorm.DB,
		facts execution.SubmissionFacts,
	) (execution.PreparedSubmission, error) {
		if len(config.Toolsets) > 0 {
			return execution.PreparedSubmission{}, ErrInvalid
		}
		spec := execution.CompileRunSpec(config, agentID, version, facts.Query, nil, nil)
		spec.PricingSnapshot = facts.PricingSnapshot
		history, baseSummary, err := loadConversationHistory(tx, facts.Owner, facts.SessionID)
		if err != nil {
			return execution.PreparedSubmission{}, err
		}
		spec.History = legacyHistory(history)
		snapshot, err := freezeContext(ctx, tx, recaller, facts, spec, history, baseSummary)
		if err != nil {
			return execution.PreparedSubmission{}, err
		}
		return execution.PreparedSubmission{
			Spec:            spec,
			UserMessage:     execution.Message{Role: "user", Content: facts.Query},
			ContextSnapshot: snapshot,
		}, nil
	}
}

// PrepareApplicationSubmission compiles an application run snapshot from a
// prefetched config; toolset/filesystem consistency depends only on the spec.
// The given recaller is used when the submission selects Recall; nil keeps
// every submission MemoryOff.
func PrepareApplicationSubmission(
	config agent.AgentConfig,
	agentID string,
	version int64,
	recaller MemoryRecaller,
) PrepareSubmission {
	return func(
		ctx context.Context,
		tx *gorm.DB,
		facts execution.SubmissionFacts,
	) (execution.PreparedSubmission, error) {
		input := facts.ExecutionSpec
		spec := execution.CompileRunSpec(config, agentID, version, input.Query, input.Scope, input.Filesystem)
		spec.PricingSnapshot = facts.PricingSnapshot
		if len(spec.Toolsets) > 0 {
			if spec.Filesystem == nil ||
				(spec.Filesystem.Provider != "AIO" && spec.Filesystem.Provider != "Sandbox") ||
				!validEndpoint(spec.Filesystem.URL) ||
				spec.Filesystem.TokenEnv == "" ||
				!strings.HasPrefix(spec.Filesystem.Workspace, "/") {
				return execution.PreparedSubmission{}, ErrInvalid
			}
			if spec.Filesystem.Provider == "AIO" &&
				(spec.Filesystem.ProfileID != "" || spec.Filesystem.RestoreFromSandboxID != "") {
				return execution.PreparedSubmission{}, ErrInvalid
			}
		} else if spec.Filesystem != nil {
			return execution.PreparedSubmission{}, ErrInvalid
		}
		history, baseSummary, err := loadConversationHistory(tx, facts.Owner, facts.SessionID)
		if err != nil {
			return execution.PreparedSubmission{}, err
		}
		spec.History = legacyHistory(history)
		snapshot, err := freezeContext(ctx, tx, recaller, facts, spec, history, baseSummary)
		if err != nil {
			return execution.PreparedSubmission{}, err
		}
		return execution.PreparedSubmission{
			Spec:            spec,
			UserMessage:     execution.Message{Role: "user", Content: input.Query},
			ContextSnapshot: snapshot,
		}, nil
	}
}

// loadConversationHistory returns complete stored turns oldest first. A Run is
// one turn: its User message, assistant decisions and paired tool results stay
// together. Incomplete turns are omitted rather than injecting orphan calls.
func loadConversationHistory(tx *gorm.DB, owner, session string) ([]contextbuilder.MessageGroup, string, error) {
	var sessionRow table.Session
	ctx := tx.Statement.Context
	if err := table.TenantQuery(ctx, tx).
		Select("compacted_summary", "compacted_through_seq").
		Where("id = ? AND owner_id = ?", session, owner).
		Take(&sessionRow).Error; err != nil {
		return nil, "", recordError(err)
	}

	var runs []table.Run
	if err := table.TenantQuery(ctx, tx).Model(&table.Run{}).
		Select("id").
		Where("owner_id = ? AND session_id = ?", owner, session).
		Order("created_at, id").
		Find(&runs).
		Error; err != nil {
		return nil, "", err
	}
	if len(runs) == 0 {
		return nil, sessionRow.CompactedSummary, nil
	}
	ids := make([]string, len(runs))
	for i := range runs {
		ids[i] = runs[i].ID
	}
	var stored []table.Message
	if err := tx.Where("session_id = ? AND run_id IN ? AND seq > ?", session, ids, sessionRow.CompactedThroughSeq).
		Order("seq").
		Find(&stored).
		Error; err != nil {
		return nil, "", err
	}
	var boundaryRun string
	if sessionRow.CompactedThroughSeq > 0 && len(stored) > 0 &&
		stored[0].Seq == sessionRow.CompactedThroughSeq+1 {
		// CompactRun keeps an assistant boundary, including the final answer
		// appended at completion when the earlier turn was fully compacted.
		boundaryRun = stored[0].RunID
	}
	byRun := make(map[string][]execution.Message, len(runs))
	for _, message := range stored {
		byRun[message.RunID] = append(byRun[message.RunID], message.Message)
	}
	groups := make([]contextbuilder.MessageGroup, 0, len(runs))
	for _, run := range runs {
		if group, ok := completeHistoryGroup(run.ID, byRun[run.ID], run.ID == boundaryRun); ok {
			groups = append(groups, group)
		}
	}
	return groups, sessionRow.CompactedSummary, nil
}

func completeHistoryGroup(
	runID string,
	messages []execution.Message,
	allowAssistantStart bool,
) (contextbuilder.MessageGroup, bool) {
	if len(messages) == 0 {
		return contextbuilder.MessageGroup{}, false
	}
	role := messages[0].Role
	if role != "user" && !(allowAssistantStart && role == "assistant") {
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

func memoryModeFromSelection(
	selection memory.Selection,
	materials []contextbuilder.ContextBlock,
) contextbuilder.MemoryMode {
	if selection != memory.SelectionRecall {
		return contextbuilder.MemoryOff
	}
	if len(materials) == 0 {
		return contextbuilder.MemoryEmpty
	}
	return contextbuilder.MemoryRecall
}

func freezeContextWithoutRecall(
	spec execution.Spec,
	history []contextbuilder.MessageGroup,
	selection memory.Selection,
) (execution.ContextSnapshot, error) {
	return freezeContextMaterials(spec, history, "", selection, nil)
}

func freezeContext(
	ctx context.Context,
	tx *gorm.DB,
	recaller MemoryRecaller,
	facts execution.SubmissionFacts,
	spec execution.Spec,
	history []contextbuilder.MessageGroup,
	baseSummary string,
) (execution.ContextSnapshot, error) {
	materials, err := recallMaterials(ctx, tx, recaller, facts)
	if err != nil {
		return execution.ContextSnapshot{}, err
	}
	return freezeContextMaterials(spec, history, baseSummary, facts.Memory, materials)
}

// recallMaterials reads the active projection for a Recall submission inside
// the accept transaction. An empty result is a distinct Empty fact, not a
// fallback; Off never reads memory.
func recallMaterials(
	ctx context.Context,
	tx *gorm.DB,
	recaller MemoryRecaller,
	facts execution.SubmissionFacts,
) ([]contextbuilder.ContextBlock, error) {
	if facts.Memory != memory.SelectionRecall {
		return nil, nil
	}
	if recaller == nil {
		return nil, ErrInvalid
	}
	agentID := facts.SessionAgentID
	audience := memory.AudiencePrivate
	if facts.Kind == execution.SubmitApplication {
		if facts.ExecutionSpec.Scope == nil || facts.ExecutionSpec.Scope.TenantID == "" ||
			facts.ExecutionSpec.Scope.ActorID == "" {
			return nil, memory.ErrForbidden
		}
		agentID = facts.ExecutionSpec.AgentID
		if facts.ExecutionSpec.Scope.ProjectID != "" {
			audience = memory.AudienceProject
		}
	}
	scope := memoryScopeFromFacts(ctx, facts)
	entries, err := recaller.RecallTx(ctx, tx, scope, audience, agentID, facts.Query, facts.MemoryFilter)
	if err != nil {
		return nil, err
	}
	blocks := make([]contextbuilder.ContextBlock, len(entries))
	for i := range entries {
		blocks[i] = memoryBlock(&entries[i])
	}
	return blocks, nil
}

func memoryScopeFromFacts(ctx context.Context, facts execution.SubmissionFacts) memory.Scope {
	requestScope, _ := access.FromContext(ctx)
	ownerScope := memory.Scope{
		Kind: memory.ScopeOwnerPrivate, Owner: facts.Owner,
		Tenant: requestScope.TenantID, Project: requestScope.ProjectID,
	}
	if facts.Kind != execution.SubmitApplication {
		return ownerScope
	}
	scope := facts.ExecutionSpec.Scope
	if scope.ProjectID == "" {
		return memory.Scope{
			Kind:   memory.ScopeActorPrivate,
			Owner:  facts.Owner,
			Tenant: scope.TenantID,
			Actor:  scope.ActorID,
		}
	}
	return memory.Scope{
		Kind:    memory.ScopeActorProject,
		Owner:   facts.Owner,
		Tenant:  scope.TenantID,
		Project: scope.ProjectID,
		Actor:   scope.ActorID,
	}
}

func memoryBlock(entry *memory.Entry) contextbuilder.ContextBlock {
	body := entry.Body
	var text string
	if err := json.Unmarshal(body, &text); err != nil || text == "" {
		text = string(body)
	}
	version := ""
	if ref := entry.ActiveVersionRef; ref != nil {
		version = ref.VersionURI
	}
	return contextbuilder.ContextBlock{
		Source: contextbuilder.Source{
			Kind:     contextbuilder.SourceMemory,
			ID:       entry.ID,
			Revision: entry.Revision,
			Version:  version,
		},
		Content:  "[" + string(entry.Kind) + "] " + entry.Summary + "\n" + text,
		Priority: memoryPriority(entry.Kind),
	}
}

func memoryPriority(kind memory.Kind) int {
	switch kind {
	case memory.KindProcedure:
		return 3
	case memory.KindPreference:
		return 2
	default:
		return 1
	}
}

func freezeContextMaterials(
	spec execution.Spec,
	history []contextbuilder.MessageGroup,
	baseSummary string,
	selection memory.Selection,
	materials []contextbuilder.ContextBlock,
) (execution.ContextSnapshot, error) {
	budget := contextbuilder.Budget{MaxInputBytes: 128 * 1024, MaxToolResultBytes: 16 * 1024}
	if spec.Model.ContextWindow > 0 {
		if spec.Model.MaxTokens == nil {
			return execution.ContextSnapshot{}, ErrInvalid
		}
		budget.ContextWindow = spec.Model.ContextWindow
		budget.OutputReserve = int64(*spec.Model.MaxTokens)
		budget.MaxInputBytes = 0
		budget.ProtocolOverheadTokens = 256
		budget.CounterVersion = contextbuilder.CJKWeightedCounterVersion
		budget.MaxToolResultBytes = max(128, min(budget.MaxToolResultBytes, budget.InputTokenLimit()/2))
	}
	snapshot := contextbuilder.Snapshot{
		MemoryMode: memoryModeFromSelection(selection, materials),
		Materials: contextbuilder.FrozenMaterials{
			SystemInstruction: spec.SystemPrompt,
			BaseSummary:       baseSummary,
			Tools:             contextTools(spec.Tools),
			Memory:            materials,
			History:           history,
		},
		Policy: contextbuilder.Policy{
			Version:               contextbuilder.PolicyVersionV4,
			KeepFullToolResults:   contextbuilder.DefaultKeepFullToolResults,
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

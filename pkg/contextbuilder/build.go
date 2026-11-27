package contextbuilder

import (
	"slices"
	"sort"
	"strings"
)

// Build composes model input from frozen materials and the current turn,
// reducing it toward the requested target before checking the hard limits.
func Build(in BuildInput) (BuildResult, error) {
	if in.Policy.Version != PolicyVersionV4 {
		return BuildResult{}, ErrUnsupportedPolicy
	}
	if in.Policy.KeepFullToolResults < 0 || in.Policy.KeepRecentToolResults < 0 {
		return BuildResult{}, ErrInvalidInput
	}
	if err := validateBudget(in.Budget); err != nil {
		return BuildResult{}, err
	}
	if len(in.CurrentTurn) == 0 {
		return BuildResult{}, ErrInvalidInput
	}
	tokenLimit, byteLimit := in.Budget.InputTokenLimit(), in.Budget.InputByteLimit()
	tokenTarget := minPositive(tokenLimit, in.TargetInputTokens)
	byteTarget := minPositive(byteLimit, in.TargetInputBytes)
	if tokenTarget > 0 {
		if in.Counter == nil {
			return BuildResult{}, ErrCounterRequired
		}
		if in.Budget.CounterVersion == "" {
			return BuildResult{}, ErrCounterVersionRequired
		}
	}
	overTarget := func(m measurement) bool { return overBudget(m, tokenTarget, byteTarget) }

	materials := in.FrozenMaterials
	summary := materials.BaseSummary
	if in.CompactedSummary != "" {
		summary = in.CompactedSummary
		materials.History = nil
	}
	pinned := append([]PinnedBlock(nil), in.PinnedBlocks...)
	if skillsBlock, ok := RenderSkillDirectory(materials.Skills); ok {
		pinned = slices.DeleteFunc(pinned, func(pin PinnedBlock) bool { return pin.Label == "skills" })
		pinned = append(pinned, skillsBlock)
	}
	liveFiles := append(attachmentFileIndex(in.CurrentTurn[0].Attachments), in.FileIndex...)
	filesPin := -1
	if files, ok := RenderFileIndex(materials.FileIndex, liveFiles); ok {
		pinned = slices.DeleteFunc(pinned, func(pin PinnedBlock) bool { return pin.Label == "files" })
		filesPin = len(pinned)
		pinned = append(pinned, files)
	}
	preamble := renderContext(summary, pinned)
	seenCalls := make(map[string]bool)
	if err := validateMaterials(materials, seenCalls); err != nil {
		return BuildResult{}, err
	}
	originalTurn := append([]Message(nil), in.CurrentTurn...)
	for _, steer := range in.Steers {
		if steer != "" {
			originalTurn = append(originalTurn, Message{Role: RoleUser, Content: steer})
		}
	}
	if err := validateTurn(originalTurn, seenCalls); err != nil {
		return BuildResult{}, err
	}
	currentTurn := previewToolMessages(appendMessages(nil, originalTurn), in.Budget.MaxToolResultBytes)
	projectAttachments(currentTurn, true, in.DirectModalities)

	memory := append([]ContextBlock(nil), materials.Memory...)
	sort.SliceStable(memory, func(i, j int) bool {
		if memory[i].Priority != memory[j].Priority {
			return memory[i].Priority > memory[j].Priority
		}
		return memory[i].Source.ID < memory[j].Source.ID
	})
	system := renderSystem(materials.SystemInstruction, memory)
	systemMeasure := measureText(in.Counter, system)

	total := measurement{tokens: in.Budget.ProtocolOverheadTokens}
	for _, tool := range materials.Tools {
		total.add(measureText(in.Counter, toolText(tool)))
	}
	if preamble.Content != "" {
		total.add(measureText(in.Counter, messageText(preamble)))
	}
	total.add(measureMessages(in.Counter, currentTurn))
	total.add(systemMeasure)

	history := append([]MessageGroup(nil), materials.History...)
	historyMeasures := make([]measurement, len(history))
	for i := range history {
		history[i].Messages = previewToolMessages(
			append([]Message(nil), history[i].Messages...),
			in.Budget.MaxToolResultBytes,
		)
		projectAttachments(history[i].Messages, false, nil)
		historyMeasures[i] = measureMessages(in.Counter, history[i].Messages)
		total.add(historyMeasures[i])
	}

	var result BuildResult
	for overTarget(total) && len(memory) > 0 {
		removed := memory[len(memory)-1]
		memory = memory[:len(memory)-1]
		total.sub(systemMeasure)
		system = renderSystem(materials.SystemInstruction, memory)
		systemMeasure = measureText(in.Counter, system)
		total.add(systemMeasure)
		result.OmittedSources = append(
			result.OmittedSources,
			OmittedSource{Source: removed.Source, Reason: OmitBudget},
		)
	}

	collapsed := make([]bool, len(history))
	for i := range history {
		if !overTarget(total) {
			break
		}
		messages := collapseHistory(history[i].Messages)
		if messages == nil {
			continue
		}
		total.sub(historyMeasures[i])
		history[i].Messages = messages
		historyMeasures[i] = measureMessages(in.Counter, messages)
		total.add(historyMeasures[i])
		collapsed[i] = true
	}
	for overTarget(total) && len(history) > 0 {
		result.OmittedSources = append(
			result.OmittedSources,
			OmittedSource{Source: history[0].Source, Reason: OmitBudget},
		)
		total.sub(historyMeasures[0])
		history = history[1:]
		historyMeasures = historyMeasures[1:]
		collapsed = collapsed[1:]
	}
	result.LadderPreviewed, result.LadderStubbed = applyLadder(currentTurn, originalTurn, in, &total, overTarget)
	result.ProjectedToolResults = result.LadderStubbed
	preambleWithoutFiles := func(blocks []PinnedBlock) Message {
		if filesPin < 0 {
			return renderContext(summary, blocks)
		}
		filtered := make([]PinnedBlock, 0, len(blocks)-1)
		filtered = append(filtered, blocks[:filesPin]...)
		filtered = append(filtered, blocks[filesPin+1:]...)
		return renderContext(summary, filtered)
	}
	core := total
	if in.AllowDropCurrentRounds && filesPin >= 0 {
		core.sub(measureText(in.Counter, messageText(preamble)))
		if without := preambleWithoutFiles(pinned); without.Content != "" {
			core.add(measureText(in.Counter, messageText(without)))
		}
	}
	if in.AllowDropCurrentRounds && overTarget(core) {
		var droppedInitialQuery string
		currentTurn, result.DroppedRounds, droppedInitialQuery = dropOldestRounds(
			currentTurn,
			in.Counter,
			&core,
			overTarget,
		)
		if droppedInitialQuery != "" && !hasPinnedQuery(pinned, droppedInitialQuery) {
			if before := preambleWithoutFiles(pinned); before.Content != "" {
				core.sub(measureText(in.Counter, messageText(before)))
			}
			pinned = append(pinned, PinnedBlock{
				Label:   "query",
				Content: droppedInitialQuery,
			})
			after := preambleWithoutFiles(pinned)
			if after.Content != "" {
				core.add(measureText(in.Counter, messageText(after)))
			}
			if overTarget(core) {
				var moreDropped int
				currentTurn, moreDropped, _ = dropOldestRounds(
					currentTurn,
					in.Counter,
					&core,
					overTarget,
				)
				result.DroppedRounds += moreDropped
			}
		}
		preamble = renderContext(summary, pinned)
		total = core
		if filesPin >= 0 {
			if without := preambleWithoutFiles(pinned); without.Content != "" {
				total.sub(measureText(in.Counter, messageText(without)))
			}
			if preamble.Content != "" {
				total.add(measureText(in.Counter, messageText(preamble)))
			}
		}
	}
	// An optional index must not displace a first question that fits.
	// Trim from the end to retain current and recent entries first.
	for filesPin >= 0 && overTarget(total) {
		total.sub(measureText(in.Counter, messageText(preamble)))
		if end := strings.LastIndexByte(pinned[filesPin].Content, '\n'); end >= 0 {
			pinned[filesPin].Content = pinned[filesPin].Content[:end]
		} else {
			pinned = append(pinned[:filesPin], pinned[filesPin+1:]...)
			filesPin = -1
		}
		preamble = renderContext(summary, pinned)
		if preamble.Content != "" {
			total.add(measureText(in.Counter, messageText(preamble)))
		}
	}
	if overBudget(total, tokenLimit, byteLimit) {
		return BuildResult{}, exceeded(total, tokenLimit, byteLimit)
	}

	if system != "" {
		result.Messages = append(result.Messages, Message{Role: RoleSystem, Content: system})
	}
	if summary != "" {
		result.Messages = append(result.Messages, preamble)
	}
	for i, group := range history {
		result.Messages = appendMessages(result.Messages, group.Messages)
		if collapsed[i] {
			result.CollapsedSources = append(result.CollapsedSources, group.Source)
		}
	}
	if summary == "" && preamble.Content != "" {
		result.Messages = append(result.Messages, preamble)
	}
	result.Messages = append(result.Messages, currentTurn...)
	result.Usage = Usage{
		InputTokens:   total.tokens,
		InputBytes:    total.bytes,
		TokenLimit:    tokenLimit,
		ByteLimit:     byteLimit,
		OutputReserve: in.Budget.OutputReserve,
	}
	result.TargetMet = !overTarget(total)
	return result, nil
}

func collapseHistory(messages []Message) []Message {
	if len(messages) < 4 || messages[0].Role != RoleUser {
		return nil
	}
	final := messages[len(messages)-1]
	if final.Role != RoleAssistant || len(final.ToolCalls) != 0 {
		return nil
	}
	hasTools := false
	for _, message := range messages[1 : len(messages)-1] {
		if message.Role == RoleUser {
			return nil
		}
		hasTools = hasTools || message.Role == RoleTool
	}
	if !hasTools {
		return nil
	}
	return []Message{messages[0], final}
}

// ValidateGroup reports whether messages form one complete assistant/tool
// chain. Loaders use it to omit stored history groups that never finished.
func ValidateGroup(messages []Message) error {
	return validateTurn(messages, make(map[string]bool))
}

func validateMaterials(materials FrozenMaterials, seenCalls map[string]bool) error {
	for _, group := range materials.History {
		if group.Source.Kind != SourceHistory || group.Source.ID == "" {
			return ErrInvalidInput
		}
		if err := validateTurn(group.Messages, seenCalls); err != nil {
			return err
		}
	}
	for _, block := range materials.Memory {
		if block.Source.Kind != SourceMemory || block.Source.ID == "" {
			return ErrInvalidInput
		}
	}
	seenTools := make(map[string]bool)
	for _, tool := range materials.Tools {
		if tool.Name == "" || seenTools[tool.Name] {
			return ErrInvalidInput
		}
		seenTools[tool.Name] = true
	}
	return nil
}

// validateTurn checks roles and tool-call pairing. Every call ID is unique
// across the frozen history and the current turn.
func validateTurn(messages []Message, seen map[string]bool) error {
	pending := make(map[string]bool)
	for _, message := range messages {
		if len(pending) > 0 && message.Role != RoleTool {
			return ErrIncompleteGroup
		}
		switch message.Role {
		case RoleUser:
			if len(message.ToolCalls) > 0 || message.ToolCallID != "" {
				return ErrInvalidInput
			}
		case RoleAssistant:
			if message.ToolCallID != "" {
				return ErrInvalidInput
			}
			for _, call := range message.ToolCalls {
				if call.ID == "" || call.Name == "" || seen[call.ID] || pending[call.ID] {
					return ErrInvalidInput
				}
				seen[call.ID] = true
				pending[call.ID] = true
			}
		case RoleTool:
			if message.ToolCallID == "" || len(message.ToolCalls) > 0 {
				return ErrInvalidInput
			}
			if !pending[message.ToolCallID] {
				return ErrIncompleteGroup
			}
			delete(pending, message.ToolCallID)
		default:
			return ErrInvalidInput
		}
	}
	if len(pending) != 0 {
		return ErrIncompleteGroup
	}
	return nil
}

func appendMessages(dst, src []Message) []Message {
	for _, message := range src {
		if len(message.ToolCalls) > 0 {
			message.ToolCalls = append([]ToolCall(nil), message.ToolCalls...)
		}
		dst = append(dst, message)
	}
	return dst
}

func exceeded(m measurement, tokenLimit, byteLimit int64) *BudgetExceededError {
	return &BudgetExceededError{
		InputTokens: m.tokens,
		InputBytes:  m.bytes,
		TokenLimit:  tokenLimit,
		ByteLimit:   byteLimit,
	}
}

package contextbuilder

import "sort"

// Build selects complete history groups and whole memory entries under the
// frozen policy and budget. Required input is the system instruction, tool
// definitions and protocol overhead, and the complete current turn.
func Build(in BuildInput) (BuildResult, error) {
	if in.Policy.Version != PolicyVersionV1 && in.Policy.Version != PolicyVersionV2 {
		return BuildResult{}, ErrUnsupportedPolicy
	}
	if err := validateBudget(in.Budget); err != nil {
		return BuildResult{}, err
	}
	if len(in.CurrentTurn) == 0 {
		return BuildResult{}, ErrInvalidInput
	}
	tokenLimit := in.Budget.InputTokenLimit()
	byteLimit := in.Budget.InputByteLimit()
	if tokenLimit > 0 {
		if in.Counter == nil {
			return BuildResult{}, ErrCounterRequired
		}
		if in.Budget.CounterVersion == "" {
			return BuildResult{}, ErrCounterVersionRequired
		}
	}

	seenCalls := make(map[string]bool)
	if err := validateMaterials(in.FrozenMaterials, seenCalls); err != nil {
		return BuildResult{}, err
	}
	if err := validateTurn(in.CurrentTurn, seenCalls); err != nil {
		return BuildResult{}, err
	}

	history := append([]MessageGroup(nil), in.FrozenMaterials.History...)
	historyMeasures := make([]measurement, len(history))
	for i, group := range history {
		history[i].Messages = previewToolMessages(group.Messages, in.Budget.MaxToolResultBytes)
		for _, message := range history[i].Messages {
			historyMeasures[i].add(measureText(in.Counter, messageText(message)))
		}
	}

	memory := append([]ContextBlock(nil), in.FrozenMaterials.Memory...)
	sort.SliceStable(memory, func(i, j int) bool {
		if memory[i].Priority != memory[j].Priority {
			return memory[i].Priority > memory[j].Priority
		}
		return memory[i].Source.ID < memory[j].Source.ID
	})

	var toolsMeasure measurement
	for _, tool := range in.FrozenMaterials.Tools {
		toolsMeasure.add(measureText(in.Counter, toolText(tool)))
	}

	var currentMeasure measurement
	currentTurn := previewToolMessages(in.CurrentTurn, in.Budget.MaxToolResultBytes)
	for _, message := range currentTurn {
		currentMeasure.add(measureText(in.Counter, messageText(message)))
	}

	systemMeasure := measureText(in.Counter, renderSystem(in.FrozenMaterials.SystemInstruction, memory))
	required := measurement{tokens: in.Budget.ProtocolOverheadTokens}
	required.add(measureText(in.Counter, renderSystem(in.FrozenMaterials.SystemInstruction, nil)))
	required.add(toolsMeasure)
	required.add(currentMeasure)
	if overBudget(required, tokenLimit, byteLimit) {
		return BuildResult{}, exceeded(required, tokenLimit, byteLimit)
	}

	total := measurement{tokens: in.Budget.ProtocolOverheadTokens}
	total.add(systemMeasure)
	total.add(toolsMeasure)
	total.add(currentMeasure)
	for _, measure := range historyMeasures {
		total.add(measure)
	}

	omitted := make([]OmittedSource, 0)
	collapsed := make([]bool, len(history))
	for overBudget(total, tokenLimit, byteLimit) && len(memory) > 0 {
		removed := memory[len(memory)-1]
		memory = memory[:len(memory)-1]
		total.sub(systemMeasure)
		systemMeasure = measureText(in.Counter, renderSystem(in.FrozenMaterials.SystemInstruction, memory))
		total.add(systemMeasure)
		omitted = append(omitted, OmittedSource{Source: removed.Source, Reason: OmitBudget})
	}
	if in.Policy.Version == PolicyVersionV2 {
		for index := range history {
			if !overBudget(total, tokenLimit, byteLimit) {
				break
			}
			messages := collapseHistory(history[index].Messages)
			if messages == nil {
				continue
			}
			var reduced measurement
			for _, message := range messages {
				reduced.add(measureText(in.Counter, messageText(message)))
			}
			total.sub(historyMeasures[index])
			total.add(reduced)
			history[index].Messages = messages
			historyMeasures[index] = reduced
			collapsed[index] = true
		}
	}
	for overBudget(total, tokenLimit, byteLimit) && len(history) > 0 {
		removed := history[0]
		history = history[1:]
		total.sub(historyMeasures[0])
		historyMeasures = historyMeasures[1:]
		collapsed = collapsed[1:]
		omitted = append(omitted, OmittedSource{Source: removed.Source, Reason: OmitBudget})
	}
	if overBudget(total, tokenLimit, byteLimit) {
		return BuildResult{}, exceeded(total, tokenLimit, byteLimit)
	}

	messages := make([]Message, 0, 1+len(in.CurrentTurn))
	var collapsedSources []Source
	for index, group := range history {
		messages = appendMessages(messages, group.Messages)
		if collapsed[index] {
			collapsedSources = append(collapsedSources, group.Source)
		}
	}
	messages = appendMessages(messages, currentTurn)
	if in.FrozenMaterials.SystemInstruction != "" || len(memory) > 0 {
		messages = append([]Message{{
			Role:    RoleSystem,
			Content: renderSystem(in.FrozenMaterials.SystemInstruction, memory),
		}}, messages...)
	}

	return BuildResult{
		Messages: messages,
		Usage: Usage{
			InputTokens:   total.tokens,
			InputBytes:    total.bytes,
			TokenLimit:    tokenLimit,
			ByteLimit:     byteLimit,
			OutputReserve: in.Budget.OutputReserve,
		},
		OmittedSources:   omitted,
		CollapsedSources: collapsedSources,
	}, nil
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

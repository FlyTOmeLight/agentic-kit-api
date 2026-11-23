package contextbuilder

// groupRounds splits before every assistant message, keeping its tool results
// and any following user messages in the same round.
func groupRounds(messages []Message) [][]Message {
	var rounds [][]Message
	start := 0
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == RoleAssistant {
			rounds = append(rounds, messages[start:i])
			start = i
		}
	}
	if len(messages) > 0 {
		rounds = append(rounds, messages[start:])
	}
	return rounds
}

func dropOldestRounds(
	messages []Message,
	counter Counter,
	total *measurement,
	overTarget func(measurement) bool,
) ([]Message, int, string) {
	rounds := groupRounds(messages)
	dropped := 0
	droppedInitialQuery := ""
	for len(rounds) > 1 && overTarget(*total) {
		if dropped == 0 && len(rounds[0]) > 0 && rounds[0][0].Role == RoleUser && rounds[0][0].Content != "" {
			droppedInitialQuery = rounds[0][0].Content
		}
		total.sub(measureMessages(counter, rounds[0]))
		messages = messages[len(rounds[0]):]
		rounds = rounds[1:]
		dropped++
	}
	clean := dropOrphanToolMessages(messages)
	if len(clean) != len(messages) {
		total.sub(measureMessages(counter, messages))
		total.add(measureMessages(counter, clean))
	}
	return clean, dropped, droppedInitialQuery
}

func hasPinnedQuery(pinned []PinnedBlock, query string) bool {
	for _, block := range pinned {
		if block.Label == "query" || block.Content == query {
			return true
		}
	}
	return false
}

// dropOrphanToolMessages removes stray tool results and discards incomplete
// assistant/tool chains. Valid chains pass through unchanged.
func dropOrphanToolMessages(messages []Message) []Message {
	var clean []Message
	for i := 0; i < len(messages); {
		message := messages[i]
		i++
		if message.Role == RoleTool {
			continue
		}
		if len(message.ToolCalls) == 0 {
			clean = append(clean, message)
			continue
		}

		pending := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			pending[call.ID] = true
		}
		start := len(clean)
		clean = append(clean, message)
		for i < len(messages) && messages[i].Role == RoleTool {
			tool := messages[i]
			if pending[tool.ToolCallID] {
				clean = append(clean, tool)
				delete(pending, tool.ToolCallID)
			}
			i++
		}
		if len(pending) != 0 {
			clean = clean[:start]
		}
	}
	return clean
}

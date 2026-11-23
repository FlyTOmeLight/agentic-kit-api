package contextbuilder

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type toolCallPosition struct {
	assistant int
	call      *ToolCall
}

type toolResultPosition struct {
	message int
	toolCallPosition
}

func applyLadder(
	messages, original []Message,
	in BuildInput,
	total *measurement,
	overTarget func(measurement) bool,
) (previewed, stubbed int) {
	if !overTarget(*total) {
		return 0, 0
	}

	var results []toolResultPosition
	calls := make(map[string]toolCallPosition)
	for i := range messages {
		message := &messages[i]
		for j := range message.ToolCalls {
			call := &message.ToolCalls[j]
			calls[call.ID] = toolCallPosition{assistant: i, call: call}
		}
		if message.Role == RoleTool {
			results = append(
				results,
				toolResultPosition{message: i, toolCallPosition: calls[message.ToolCallID]},
			)
		}
	}

	keepFull := in.Policy.KeepFullToolResults
	if keepFull == 0 {
		keepFull = DefaultKeepFullToolResults
	}
	keepRecent := in.Policy.KeepRecentToolResults
	if keepRecent == 0 {
		keepRecent = DefaultKeepRecentToolResults
	}
	previewLimit := max(128, minPositive(DefaultLadderPreviewBytes, in.Budget.MaxToolResultBytes))

	// Count the entire changed assistant message so rounding stays identical
	// to the original measurement even when it contains multiple tool calls.
	for _, result := range results[:max(0, len(results)-keepFull)] {
		message := &messages[result.message]
		if message.Exempt {
			continue
		}
		preview := previewToolResult(original[result.message].Content, previewLimit)
		if len(preview) < len(message.Content) {
			replaceToolContent(message, preview, in.Counter, total)
			previewed++
		}
		if args := truncateToolArguments(result.call.Arguments); args != result.call.Arguments {
			assistant := messages[result.assistant]
			total.sub(measureText(in.Counter, messageText(assistant)))
			result.call.Arguments = args
			total.add(measureText(in.Counter, messageText(assistant)))
		}
		if !overTarget(*total) {
			return previewed, 0
		}
	}

	for _, failed := range []bool{false, true} {
		for _, result := range results[:max(0, len(results)-keepRecent)] {
			message := &messages[result.message]
			if message.Exempt || message.ToolFailed != failed {
				continue
			}
			stub := toolResultStub(original[result.message], result.call.Name)
			if len(stub) >= len(message.Content) {
				continue
			}
			replaceToolContent(message, stub, in.Counter, total)
			stubbed++
			if !overTarget(*total) {
				return previewed, stubbed
			}
		}
	}
	return previewed, stubbed
}

func replaceToolContent(message *Message, content string, counter Counter, total *measurement) {
	total.sub(measureText(counter, messageText(*message)))
	message.Content = content
	total.add(measureText(counter, messageText(*message)))
}

func toolResultStub(original Message, callName string) string {
	name := original.ToolName
	if name == "" {
		name = callName
	}
	if strings.ContainsAny(name, "=\"[]") || strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		name = strconv.Quote(name)
	}
	status := "ok"
	if original.ToolFailed {
		status = "error"
	}
	head := original.Content
	seen := 0
	for i := range head {
		if seen == DefaultStubHeadRunes {
			head = head[:i] + "..."
			break
		}
		seen++
	}
	return fmt.Sprintf(
		"[Tool result stub: tool=%s status=%s original_bytes=%d head=%s restore=read_step_result(step_id=%s)]",
		name,
		status,
		len(original.Content),
		strconv.Quote(head),
		strconv.Quote(original.ToolCallID),
	)
}

func truncateToolArguments(arguments string) string {
	if len(arguments) <= DefaultArgFieldPreviewBytes || !json.Valid([]byte(arguments)) {
		return arguments
	}
	var fields map[string]any
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil || !truncateArgumentFields(fields) {
		return arguments
	}
	data, err := json.Marshal(fields)
	if err != nil || len(data) >= len(arguments) {
		return arguments
	}
	return string(data)
}

func truncateArgumentFields(value any) bool {
	changed := false
	switch value := value.(type) {
	case map[string]any:
		for key, field := range value {
			if str, ok := field.(string); ok && len(str) > DefaultArgFieldPreviewBytes {
				end := DefaultArgFieldPreviewBytes - len("...")
				for end > 0 && !utf8.RuneStart(str[end]) {
					end--
				}
				value[key] = str[:end] + "..."
				changed = true
			} else if truncateArgumentFields(field) {
				changed = true
			}
		}
	case []any:
		for _, field := range value {
			if truncateArgumentFields(field) {
				changed = true
			}
		}
	}
	return changed
}

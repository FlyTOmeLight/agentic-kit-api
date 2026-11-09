package contextbuilder

import (
	"fmt"
	"unicode/utf8"
)

func previewToolMessages(messages []Message, limit int64) []Message {
	if limit == 0 {
		return messages
	}
	projected := append([]Message(nil), messages...)
	for index := range projected {
		message := &projected[index]
		if message.Role != RoleTool || int64(len(message.Content)) <= limit {
			continue
		}
		marker := fmt.Sprintf(
			"\n[Tool result preview: middle omitted; original %d bytes retained in the stored step.]\n",
			len(message.Content),
		)
		available := int(limit) - len(marker)
		head := available / 2
		tail := len(message.Content) - (available - head)
		for head > 0 && !utf8.RuneStart(message.Content[head]) {
			head--
		}
		for tail < len(message.Content) && !utf8.RuneStart(message.Content[tail]) {
			tail++
		}
		message.Content = message.Content[:head] + marker + message.Content[tail:]
	}
	return projected
}

// projectToolResults replaces all but the newest keep tool results with a
// short marker. Tool call/result pairing stays intact; only the model input is
// projected, while stored steps keep the full content. It returns the projected
// messages, the number of projected results, and leaves the input untouched.
func projectToolResults(messages []Message, keep int) ([]Message, int) {
	total := 0
	for _, message := range messages {
		if message.Role == RoleTool {
			total++
		}
	}
	if keep < 0 {
		keep = 0
	}
	if total <= keep {
		return messages, 0
	}
	fold := total - keep
	projected := append([]Message(nil), messages...)
	for index := range projected {
		if fold == 0 {
			break
		}
		message := &projected[index]
		if message.Role != RoleTool {
			continue
		}
		message.Content = fmt.Sprintf(
			"[Tool result projected: %d bytes omitted from model input; full result retained in the stored step.]",
			len(message.Content),
		)
		fold--
	}
	return projected, total - keep
}

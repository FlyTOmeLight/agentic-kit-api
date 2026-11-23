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
		if message.Role == RoleTool && !message.Exempt {
			message.Content = previewToolResult(message.Content, limit)
		}
	}
	return projected
}

func previewToolResult(content string, limit int64) string {
	if int64(len(content)) <= limit {
		return content
	}
	marker := fmt.Sprintf(
		"\n[Tool result preview: middle omitted; original %d bytes retained in the stored step.]\n",
		len(content),
	)
	available := int(limit) - len(marker)
	head := available / 2
	tail := len(content) - (available - head)
	for head > 0 && !utf8.RuneStart(content[head]) {
		head--
	}
	for tail < len(content) && !utf8.RuneStart(content[tail]) {
		tail++
	}
	return content[:head] + marker + content[tail:]
}

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

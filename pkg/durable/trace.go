package durable

import (
	"context"
	"database/sql"
	"encoding/json"
	"unicode/utf8"

	"gorm.io/gorm"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// Trace reads a consistent public projection. Engine state, prompts and credentials
// are deliberately excluded. Historical calls come from public assistant messages.
func (s *Store) Trace(ctx context.Context, owner, id string, after int64) (*sdk.RunTrace, error) {
	if after < 0 {
		return nil, ErrInvalid
	}
	var trace sdk.RunTrace
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := readRun(tx.Model(&runRecord{}).Where("id = ? AND owner_id = ?", id, owner))
		if err != nil {
			return err
		}
		trace.Run = run.Response()
		trace.NextSeq = after
		var messages []messageRecord
		if err = tx.Where("run_id = ? AND kind = ?", id, "Assistant").Find(&messages).Error; err != nil {
			return err
		}
		calls := map[string]sdk.ToolCall{}
		for _, m := range messages {
			for _, call := range m.Message.ToolCalls {
				calls[call.ID] = call
			}
		}
		var events []eventRecord
		if err = tx.Where("run_id = ? AND seq > ?", id, after).
			Order("seq").
			Limit(50).
			Find(&events).
			Error; err != nil {
			return err
		}
		trace.Events = make([]sdk.Event, 0, len(events))
		for _, record := range events {
			trace.Events = append(trace.Events, traceEvent(record, calls))
			trace.NextSeq = record.Seq
		}
		if run.EventSeq > 0 {
			var latest eventRecord
			if err = tx.Where("run_id = ? AND seq = ?", id, run.EventSeq).Take(&latest).Error; err != nil {
				return err
			}
			event := traceEvent(latest, calls)
			trace.LatestEvent = &event
		}
		trace.HasMore = trace.NextSeq < run.EventSeq
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return &trace, err
}

func traceEvent(record eventRecord, calls map[string]sdk.ToolCall) sdk.Event {
	event := record.event()
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return event
	}
	if step, ok := payload["stepID"].(string); ok {
		if call, ok := calls[step]; ok {
			payload["name"] = call.Name
			if event.Kind == "ToolStarted" {
				payload["arguments"] = call.Arguments
			}
		}
	}
	// Tool transcripts can be large; previews are explicitly marked and originals
	// remain in durable storage. Limit text by UTF-8 byte length, not characters.
	for _, key := range []string{"arguments", "result", "output", "detail"} {
		if value, ok := payload[key].(string); ok && len(value) > 32<<10 {
			n := 32 << 10
			for !utf8.ValidString(value[:n]) {
				n--
			}
			payload[key] = value[:n]
			payload[key+"Truncated"] = true
		}
	}
	event.Payload, _ = json.Marshal(payload)
	return event
}

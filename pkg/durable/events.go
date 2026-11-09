package durable

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// StreamKey names the notification stream for one run. The database stays the
// replay source; the stream only accelerates live delivery.
func StreamKey(id string) string { return "durable:run:" + id + ":events" }

func emit(ctx context.Context, tx *gorm.DB, id, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var updated table.Run
	result := tx.WithContext(ctx).Model(&updated).Where("id = ?", id).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "event_seq"}}}).
		Updates(map[string]any{"event_seq": gorm.Expr("event_seq + 1"), "updated_at": gorm.Expr("clock_timestamp()")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return tx.WithContext(ctx).
		Create(&table.Event{RunID: id, Seq: updated.EventSeq, Kind: kind, Payload: json.RawMessage(data)}).
		Error
}

func (s *Store) Events(ctx context.Context, owner, id string, after int64) ([]Event, error) {
	if _, err := s.Get(ctx, owner, id); err != nil {
		return nil, err
	}
	var records []table.Event
	err := s.db.WithContext(ctx).Model(&table.Event{}).
		Select("run_id", "seq", "kind", "payload", "created_at").
		Where("run_id = ? AND seq > ?", id, after).Order("seq").Limit(200).Find(&records).Error
	events := make([]Event, len(records))
	for i, record := range records {
		events[i] = eventFromRow(record)
	}
	return events, err
}

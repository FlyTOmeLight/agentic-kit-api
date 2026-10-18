package durable

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// ClaimOutbox returns the oldest unpublished event of one run and holds its row
// lock until the caller commits or rolls back the returned transaction. Only the
// head of each run is eligible, so concurrent publishers advance unrelated runs
// without reordering one run's events. A false result means the outbox is empty.
func (s *Store) ClaimOutbox(ctx context.Context) (*gorm.DB, *Event, bool, error) {
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, nil, false, err
	}
	var record table.Event
	earlier := tx.Model(&table.Event{}).Table("durable_event AS prior").Select("1").
		Where("prior.run_id = e.run_id AND prior.seq < e.seq AND NOT prior.published")
	err := tx.Model(&table.Event{}).Table("durable_event AS e").Select("e.*").
		Where("NOT e.published").Where("NOT EXISTS (?)", earlier).
		Order("e.created_at, e.run_id, e.seq").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "e"}, Options: "SKIP LOCKED"}).
		Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		tx.Rollback()
		return nil, nil, false, nil
	}
	if err != nil {
		tx.Rollback()
		return nil, nil, false, err
	}
	event := eventFromRow(record)
	return tx, &event, true, nil
}

// MarkPublished records that the claimed event reached the notification channel.
// The caller commits the transaction returned by ClaimOutbox afterwards.
func MarkPublished(tx *gorm.DB, event Event) error {
	return tx.Model(&table.Event{}).
		Where("run_id = ? AND seq = ?", event.RunID, event.Seq).
		Update("published", true).
		Error
}

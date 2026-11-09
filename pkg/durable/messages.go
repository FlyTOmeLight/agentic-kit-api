package durable

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// Caller holds the run lock. The session counter serializes all displayed messages.
func appendMessage(tx *gorm.DB, run *Run, kind string, message Message) error {
	var session table.Session
	result := tx.Model(&session).Where("id = ? AND owner_id = ?", run.SessionID, run.OwnerID).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "message_seq"}}}).
		Update("message_seq", gorm.Expr("message_seq + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return tx.Create(
		&table.Message{
			SessionID: run.SessionID,
			Seq:       session.MessageSeq,
			RunID:     run.ID,
			Kind:      kind,
			Message:   message,
		},
	).Error
}

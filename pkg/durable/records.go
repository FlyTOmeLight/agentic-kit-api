package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func Hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func (s *Store) Get(ctx context.Context, owner, id string) (*Run, error) {
	return readRun(s.db.WithContext(ctx).Model(&table.Run{}).Where("id = ? AND owner_id = ?", id, owner))
}

func runFromRow(r table.Run) *Run {
	return &Run{
		Scope:         r.Spec.Scope,
		AgentID:       r.Spec.AgentID,
		ConfigVersion: r.Spec.ConfigVersion,
		ID:            r.ID,
		OwnerID:       r.OwnerID,
		SessionID:     r.SessionID,
		Spec:          Spec(r.Spec),
		State:         r.State,
		Status:        r.Status,
		WorkerID:      r.WorkerID,
		Epoch:         r.Epoch,
		LeaseUntil:    r.LeaseUntil,
		Deadline:      r.Deadline,
		Attempt:       r.Attempt,
		MaxAttempts:   r.MaxAttempts,
		EventSeq:      r.EventSeq,
		Output:        r.Output,
		Error:         r.Error,
		WaitingStep:   r.WaitingStep,
		WaitingKind:   r.WaitingKind,
	}
}

func eventFromRow(e table.Event) Event {
	return Event{RunID: e.RunID, Seq: e.Seq, Kind: e.Kind, Payload: e.Payload, CreatedAt: e.CreatedAt.UTC()}
}

func readRun(query *gorm.DB) (*Run, error) {
	var r table.Run
	if err := query.Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return runFromRow(r), nil
}

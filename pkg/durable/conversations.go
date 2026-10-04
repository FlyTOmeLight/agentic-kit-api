package durable

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Session struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agentID"`
	Title      string    `json:"title"`
	MessageSeq int64     `json:"messageSeq"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}
type SessionMessage struct {
	SessionID string `json:"sessionID"`
	Seq       int64  `json:"seq"`
	RunID     string `json:"runID"`
	Kind      string `json:"kind"`
	Message
	CreatedAt time.Time `json:"createdAt"`
}

func (r sessionRecord) session() *Session {
	return &Session{ID: r.ID, AgentID: r.AgentID, Title: r.Title, MessageSeq: r.MessageSeq, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}
func (s *Store) CreateSession(ctx context.Context, owner, agent, title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if len(title) > 200 {
		return nil, ErrInvalid
	}
	a, err := s.Agent(ctx, owner, agent)
	if err != nil {
		return nil, err
	}
	if a.PublishedVersion == 0 {
		return nil, ErrConflict
	}
	r := sessionRecord{ID: NewID(), OwnerID: owner, AgentID: agent, Title: title}
	if err = s.db.WithContext(ctx).Create(&r).Error; err != nil {
		return nil, err
	}
	return r.session(), nil
}
func (s *Store) Session(ctx context.Context, owner, id string) (*Session, error) {
	var r sessionRecord
	if err := s.db.WithContext(ctx).Where("owner_id = ? AND id = ?", owner, id).Take(&r).Error; err != nil {
		return nil, recordError(err)
	}
	return r.session(), nil
}
func (s *Store) Sessions(ctx context.Context, owner, agent, after string, limit int) ([]Session, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	q := s.db.WithContext(ctx).Where("owner_id = ? AND id > ?", owner, after)
	if agent != "" {
		q = q.Where("agent_id = ?", agent)
	}
	var records []sessionRecord
	err := q.Order("id").Limit(limit).Find(&records).Error
	out := make([]Session, len(records))
	for i, r := range records {
		out[i] = *r.session()
	}
	return out, err
}
func (s *Store) Messages(ctx context.Context, owner, session string, after int64, limit int) ([]SessionMessage, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	if _, err := s.Session(ctx, owner, session); err != nil {
		return nil, err
	}
	var records []messageRecord
	err := s.db.WithContext(ctx).Where("session_id = ? AND seq > ?", session, after).Order("seq").Limit(limit).Find(&records).Error
	out := make([]SessionMessage, len(records))
	for i, r := range records {
		out[i] = SessionMessage{SessionID: r.SessionID, Seq: r.Seq, RunID: r.RunID, Kind: r.Kind, Message: r.Message, CreatedAt: r.CreatedAt.UTC()}
	}
	return out, err
}
func (s *Store) SessionRuns(ctx context.Context, owner, session, after string, limit int) ([]Run, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	if _, err := s.Session(ctx, owner, session); err != nil {
		return nil, err
	}
	var records []runRecord
	err := s.db.WithContext(ctx).Where("owner_id = ? AND session_id = ? AND id > ?", owner, session, after).Order("id").Limit(limit).Find(&records).Error
	out := make([]Run, len(records))
	for i, r := range records {
		out[i] = *r.run()
	}
	return out, err
}

// Caller holds the run lock. The session counter serializes all displayed messages.
func appendMessage(tx *gorm.DB, run *Run, kind string, message Message) error {
	var session sessionRecord
	result := tx.Model(&session).Where("id = ? AND owner_id = ?", run.SessionID, run.OwnerID).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "message_seq"}}}).Update("message_seq", gorm.Expr("message_seq + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return tx.Create(&messageRecord{SessionID: run.SessionID, Seq: session.MessageSeq, RunID: run.ID, Kind: kind, Message: message}).Error
}

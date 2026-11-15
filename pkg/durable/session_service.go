package durable

import (
	"context"
	"strings"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func sessionFromRow(r table.Session) *Session {
	return &Session{
		ID:         r.ID,
		AgentID:    r.AgentID,
		Title:      r.Title,
		MessageSeq: r.MessageSeq,
		CreatedAt:  r.CreatedAt.UTC(),
		UpdatedAt:  r.UpdatedAt.UTC(),
	}
}

func (s *Store) CreateSession(ctx context.Context, owner, agent, title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if len(title) > 200 {
		return nil, ErrInvalid
	}
	agentRow, err := table.GetAgent(s.db.WithContext(ctx), owner, agent)
	if err != nil {
		return nil, recordError(err)
	}
	if agentRow.PublishedVersion == 0 {
		return nil, ErrConflict
	}
	r := table.Session{ID: NewID(), OwnerID: owner, AgentID: agent, Title: title}
	if err = s.db.WithContext(ctx).Create(&r).Error; err != nil {
		return nil, err
	}
	return sessionFromRow(r), nil
}

func (s *Store) Session(ctx context.Context, owner, id string) (*Session, error) {
	var r table.Session
	if err := s.db.WithContext(ctx).Where("owner_id = ? AND id = ?", owner, id).Take(&r).Error; err != nil {
		return nil, recordError(err)
	}
	return sessionFromRow(r), nil
}

func (s *Store) Sessions(ctx context.Context, owner, agent, after string, limit int) ([]Session, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	q := s.db.WithContext(ctx).Where("owner_id = ? AND id > ?", owner, after)
	if agent != "" {
		q = q.Where("agent_id = ?", agent)
	}
	var records []table.Session
	err := q.Order("id").Limit(limit).Find(&records).Error
	out := make([]Session, len(records))
	for i, r := range records {
		out[i] = *sessionFromRow(r)
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
	var records []table.Message
	err := s.db.WithContext(ctx).
		Where("session_id = ? AND seq > ?", session, after).
		Order("seq").
		Limit(limit).
		Find(&records).
		Error
	out := make([]SessionMessage, len(records))
	for i, r := range records {
		out[i] = SessionMessage{
			SessionID: r.SessionID,
			Seq:       r.Seq,
			RunID:     r.RunID,
			Kind:      r.Kind,
			Message:   r.Message,
			CreatedAt: r.CreatedAt.UTC(),
		}
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
	var records []table.Run
	err := s.db.WithContext(ctx).
		Where("owner_id = ? AND session_id = ? AND id > ?", owner, session, after).
		Order("id").
		Limit(limit).
		Find(&records).
		Error
	out := make([]Run, len(records))
	for i, r := range records {
		out[i] = *runFromRow(r)
	}
	return out, err
}

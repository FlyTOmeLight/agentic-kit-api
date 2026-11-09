package durable

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
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
	a, err := s.Agent(ctx, owner, agent)
	if err != nil {
		return nil, err
	}
	if a.PublishedVersion == 0 {
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

// Submit freezes the published configuration and conversation history in the same
// transaction as the user message, run and queued event. A retry hashes only the
// caller's input, so publishing a new version cannot invalidate an accepted request.
func (s *Store) Submit(
	ctx context.Context,
	owner, session, key, query string,
	ttl time.Duration,
	maxAttempts int,
) (*Run, bool, error) {
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session, Query: query})
	return s.accept(ctx, execution.SubmitCommand{
		Owner:       owner,
		RequestKey:  key,
		RequestHash: Hash(input),
		TTL:         ttl,
		MaxAttempts: maxAttempts,
		Kind:        execution.SubmitChat,
		SessionID:   session,
		Query:       query,
	}, PrepareChatSubmission)
}

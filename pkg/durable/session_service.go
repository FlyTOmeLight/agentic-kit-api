package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/sdk"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Session = sdk.Session
type SessionMessage = sdk.SessionMessage

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

// Submit freezes the published configuration and conversation history in the same
// transaction as the user message, run and queued event. A retry hashes only the
// caller's input, so publishing a new version cannot invalidate an accepted request.
func (s *Store) Submit(ctx context.Context, owner, session, key, query string, ttl time.Duration, maxAttempts int) (*Run, bool, error) {
	if owner == "" || session == "" || strings.TrimSpace(key) == "" || len(key) > 128 || strings.TrimSpace(query) == "" || ttl <= 0 || maxAttempts < 1 {
		return nil, false, ErrInvalid
	}
	input, _ := json.Marshal(struct{ Session, Query string }{session, query})
	hash := Hash(input)
	var run *Run
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, owner+":"+key).Error; err != nil {
			return err
		}
		var existing runRecord
		err := tx.Where("owner_id = ? AND request_key = ?", owner, key).Take(&existing).Error
		if err == nil {
			if existing.RequestHash != hash {
				return ErrConflict
			}
			run = existing.run()
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var conversation sessionRecord
		if err = tx.Where("owner_id = ? AND id = ?", owner, session).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&conversation).Error; err != nil {
			return recordError(err)
		}
		var active int64
		if err = tx.Model(&runRecord{}).Where("owner_id = ? AND session_id = ? AND status IN ?", owner, session, []Status{Queued, Running, WaitingClarification, NeedsReview}).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrConflict
		}
		var agent agentRecord
		if err = tx.Where("owner_id = ? AND id = ?", owner, conversation.AgentID).Take(&agent).Error; err != nil {
			return recordError(err)
		}
		if agent.PublishedVersion == 0 {
			return ErrConflict
		}
		var version versionRecord
		if err = tx.Where("agent_id = ? AND number = ?", agent.ID, agent.PublishedVersion).Take(&version).Error; err != nil {
			return err
		}
		if len(version.Config.Toolsets) > 0 {
			return ErrInvalid
		}
		spec := runSpec(version.Config, agent.ID, version.Number, query)
		if err = loadConversationHistory(tx, owner, session, &spec); err != nil {
			return err
		}
		data, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		id := NewID()
		if err = tx.Model(&runRecord{}).Create(map[string]any{
			"id": id, "owner_id": owner, "session_id": session, "request_key": key, "request_hash": hash, "spec": string(data), "status": Queued,
			"deadline": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", ttl.Milliseconds()), "max_attempts": maxAttempts,
		}).Error; err != nil {
			return recordError(err)
		}
		run, err = readRun(tx.Where("id = ?", id))
		if err != nil {
			return err
		}
		if err = appendMessage(tx, run, "User", Message{Role: "user", Content: query}); err != nil {
			return err
		}
		if err = emit(ctx, tx, id, "RunQueued", map[string]any{"status": Queued}); err != nil {
			return err
		}
		run.EventSeq = 1
		created = true
		return nil
	})
	return run, created, err
}

// Tool history remains in the message log. Model context includes the last
// twenty completed user/final pairs, shared by chat and application execution.
func loadConversationHistory(tx *gorm.DB, owner, session string, spec *Spec) error {
	prior := tx.Model(&runRecord{}).Select("id").Where("owner_id = ? AND session_id = ? AND status = ?", owner, session, Completed).Order("created_at DESC, id DESC").Limit(20)
	var history []messageRecord
	if err := tx.Where("session_id = ? AND run_id IN (?) AND kind IN ?", session, prior, []string{"User", "Final"}).Order("seq").Find(&history).Error; err != nil {
		return err
	}
	for _, m := range history {
		spec.History = append(spec.History, m.Message)
	}
	return nil
}

package durable

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

// SubmitExecution accepts an immutable application snapshot. Its key is scoped to
// the authenticated service owner. It creates the session graph atomically.
func (s *Store) SubmitExecution(ctx context.Context, owner, key string, spec Spec, ttl time.Duration) (*Run, bool, error) {
	if owner == "" || strings.TrimSpace(key) == "" || len(key) > 128 || spec.Scope == nil || spec.Scope.TenantID == "" || spec.Scope.ProjectID == "" || spec.Scope.ActorID == "" || spec.Scope.TaskID == "" || spec.Scope.RoundID == "" || strings.TrimSpace(spec.Query) == "" || ttl < time.Second || ttl > 24*time.Hour || len(spec.History) > 0 {
		return nil, false, ErrInvalid
	}
	config := AgentConfig{SystemPrompt: spec.SystemPrompt, Model: spec.Model, Tools: spec.Tools, MaxTurns: spec.MaxTurns}
	if e := ValidateAgentConfig(config); e != nil {
		return nil, false, e
	}
	input, _ := json.Marshal(struct {
		Spec Spec
		TTL  time.Duration
	}{spec, ttl})
	hash := Hash(input)
	var result *Run
	created := false
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, owner+":"+key).Error; e != nil {
			return e
		}
		var old runRecord
		e := tx.Where("owner_id = ? AND request_key = ?", owner, key).Take(&old).Error
		if e == nil {
			if old.RequestHash != hash {
				return ErrConflict
			}
			result = old.run()
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		agent := agentRecord{ID: NewID(), OwnerID: owner, Name: "Application execution", Draft: config, Revision: 1, PublishedRevision: 1, PublishedVersion: 1}
		if e = tx.Create(&agent).Error; e != nil {
			return e
		}
		if e = tx.Create(&versionRecord{AgentID: agent.ID, Number: 1, Config: config}).Error; e != nil {
			return e
		}
		session := sessionRecord{ID: NewID(), OwnerID: owner, AgentID: agent.ID, Title: spec.Scope.TaskID}
		if e = tx.Create(&session).Error; e != nil {
			return e
		}
		spec.AgentID = agent.ID
		spec.ConfigVersion = "1"
		data, _ := json.Marshal(spec)
		id := NewID()
		if e = tx.Model(&runRecord{}).Create(map[string]any{"id": id, "owner_id": owner, "session_id": session.ID, "request_key": key, "request_hash": hash, "spec": string(data), "status": Queued, "deadline": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", ttl.Milliseconds()), "max_attempts": 5}).Error; e != nil {
			return recordError(e)
		}
		result, e = readRun(tx.Where("id = ?", id))
		if e != nil {
			return e
		}
		if e = appendMessage(tx, result, "User", Message{Role: "user", Content: spec.Query}); e != nil {
			return e
		}
		if e = emit(ctx, tx, id, "RunQueued", map[string]any{"status": Queued}); e != nil {
			return e
		}
		result.EventSeq = 1
		created = true
		return nil
	})
	return result, created, e
}

// CheckExecutionLease uses the database clock and rejects canceled or old attempts.
func (s *Store) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	var n int64
	e := s.db.WithContext(ctx).Model(&runRecord{}).Where("owner_id = ? AND id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp()", owner, id, epoch, Running).Count(&n).Error
	return n == 1, e
}

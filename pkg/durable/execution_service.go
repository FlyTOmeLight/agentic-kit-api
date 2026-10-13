package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// SubmitExecution freezes a published Agent version and business input.
// A task reuses its conversation; retries reuse the originally accepted run.
func (s *Store) SubmitExecution(
	ctx context.Context,
	owner, key string,
	inputSpec sdk.ExecutionSpec,
	ttl time.Duration,
) (*Run, bool, error) {
	var result *Run
	var created bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, created, err = submitExecution(ctx, tx, owner, key, inputSpec, ttl)
		return err
	})
	return result, created, err
}

// submitExecution allows experiment and Run creation to commit together.
func submitExecution(
	ctx context.Context,
	tx *gorm.DB,
	owner, key string,
	inputSpec sdk.ExecutionSpec,
	ttl time.Duration,
) (*Run, bool, error) {
	if owner == "" || strings.TrimSpace(key) == "" || len(key) > 128 || inputSpec.Scope == nil ||
		inputSpec.Scope.TenantID == "" ||
		inputSpec.Scope.ActorID == "" ||
		inputSpec.Scope.TaskID == "" ||
		inputSpec.Scope.RoundID == "" ||
		strings.TrimSpace(inputSpec.Query) == "" ||
		ttl < time.Second ||
		ttl > 24*time.Hour ||
		inputSpec.AgentID == "" ||
		inputSpec.AgentVersion < 1 {
		return nil, false, ErrInvalid
	}
	input, _ := json.Marshal(struct {
		Spec sdk.ExecutionSpec
		TTL  time.Duration
	}{Spec: inputSpec, TTL: ttl})
	hash := Hash(input)
	var result *Run
	created := false
	e := func() error {
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
		// A business task owns one conversation across rounds. The scope lock also
		// serializes first creation, before a session row exists to lock.
		identity, _ := json.Marshal(
			[]string{owner, inputSpec.Scope.TenantID, inputSpec.Scope.ProjectID, inputSpec.Scope.TaskID},
		)
		sessionID := "application_" + Hash(identity)
		if e = tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", sessionID).Error; e != nil {
			return e
		}
		var agent agentRecord
		if e = tx.Where("owner_id = ? AND id = ?", owner, inputSpec.AgentID).Take(&agent).Error; e != nil {
			return recordError(e)
		}
		var version versionRecord
		if e = tx.Where("agent_id = ? AND number = ?", agent.ID, inputSpec.AgentVersion).
			Take(&version).
			Error; e != nil {
			return recordError(e)
		}
		if inputSpec.Scope.ProjectID == "" &&
			(len(version.Config.Tools) != 0 || len(version.Config.Toolsets) != 0 || inputSpec.Filesystem != nil) {
			return ErrInvalid
		}
		var session sessionRecord
		e = tx.Where("owner_id = ? AND id = ?", owner, sessionID).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&session).
			Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			session = sessionRecord{
				ID:      sessionID,
				OwnerID: owner,
				AgentID: agent.ID,
				Title:   inputSpec.Scope.TaskID,
			}
			if e = tx.Create(&session).Error; e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else if session.AgentID != agent.ID {
			return ErrConflict
		}
		var active int64
		if e = tx.Model(&runRecord{}).
			Where("owner_id = ? AND session_id = ? AND status IN ?", owner, session.ID, []Status{Queued, Running, WaitingClarification, NeedsReview}).
			Count(&active).
			Error; e != nil {
			return e
		}
		if active > 0 {
			return ErrConflict
		}
		spec := runSpec(version.Config, agent.ID, version.Number, inputSpec.Query)
		spec.Scope = inputSpec.Scope
		spec.Filesystem = inputSpec.Filesystem
		if len(spec.Toolsets) > 0 {
			if spec.Filesystem == nil || spec.Filesystem.Provider != "AIO" ||
				!validEndpoint(spec.Filesystem.URL) ||
				spec.Filesystem.TokenEnv == "" ||
				!strings.HasPrefix(spec.Filesystem.Workspace, "/") {
				return ErrInvalid
			}
		} else if spec.Filesystem != nil {
			return ErrInvalid
		}
		if e = loadConversationHistory(tx, owner, session.ID, &spec); e != nil {
			return e
		}
		data, _ := json.Marshal(spec)
		id := NewID()
		if e = tx.Model(&runRecord{}).
			Create(map[string]any{"id": id, "owner_id": owner, "session_id": session.ID, "request_key": key, "request_hash": hash, "spec": string(data), "status": Queued, "deadline": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", ttl.Milliseconds()), "max_attempts": 5}).
			Error; e != nil {
			return recordError(e)
		}
		result, e = readRun(tx.Where("id = ?", id))
		if e != nil {
			return e
		}
		if e = appendMessage(tx, result, "User", Message{Role: "user", Content: inputSpec.Query}); e != nil {
			return e
		}
		if e = emit(ctx, tx, id, "RunQueued", map[string]any{"status": Queued}); e != nil {
			return e
		}
		result.EventSeq = 1
		created = true
		return nil
	}()
	return result, created, e
}

// CheckExecutionLease uses the database clock and rejects canceled or old attempts.
func (s *Store) CheckExecutionLease(ctx context.Context, owner, id string, epoch int64) (bool, error) {
	var n int64
	e := s.db.WithContext(ctx).
		Model(&runRecord{}).
		Where("owner_id = ? AND id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp()", owner, id, epoch, Running).
		Count(&n).
		Error
	return n == 1, e
}

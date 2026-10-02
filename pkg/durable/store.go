package durable

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

//go:embed schema.sql
var schemaSQL string

// Store owns one GORM database handle and its database/sql connection pool.
type Store struct{ db *gorm.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		TranslateError:       true,
		// Domain errors are reported by callers; avoid logging prompts and tool results as SQL.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(20)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	if err = pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() { pool, _ := s.db.DB(); _ = pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	// Keep the reviewed PostgreSQL constraints and partial indexes in schema.sql.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7412101)").Error; err != nil {
			return err
		}
		return tx.Exec(schemaSQL).Error
	})
}

const columns = `id,owner_id,session_id,spec,state,status,worker_id,epoch,lease_until,deadline,attempt,max_attempts,event_seq,output,error`

func readRun(query *gorm.DB) (*Run, error) {
	var r Run
	if err := query.Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.WaitingStep = waitingStep(r.State)
	return &r, nil
}

func Hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (s *Store) Create(ctx context.Context, owner, session, key string, spec Spec, ttl time.Duration, maxAttempts int) (*Run, bool, error) {
	if owner == "" || session == "" || key == "" || ttl <= 0 || maxAttempts < 1 {
		return nil, false, ErrConflict
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, false, err
	}
	// The hash excludes neither prompt nor model configuration: replaying a key with changed input is a conflict.
	hash := Hash(append([]byte(session+"\x00"), b...))
	tx := s.db.WithContext(ctx).Begin()
	err = tx.Error
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// Serialize duplicate submissions without taking a global lock.
	if err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, owner+":"+key).Error; err != nil {
		return nil, false, err
	}
	var existing struct{ ID, RequestHash string }
	err = tx.Table("durable_run").Select("id", "request_hash").Where("owner_id = ? AND request_key = ?", owner, key).Take(&existing).Error
	if err == nil {
		if existing.RequestHash != hash {
			return nil, false, ErrConflict
		}
		r, err := readRun(tx.Table("durable_run").Where("id = ?", existing.ID))
		return r, false, err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	// Serialize a session's history snapshot with new submissions.
	if err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,1))`, owner+":"+session).Error; err != nil {
		return nil, false, err
	}
	// A run may finish concurrently. Reject an active predecessor BEFORE reading history,
	// otherwise it could finish between the snapshot and the unique-index check.
	var active bool
	if err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM durable_run WHERE owner_id=? AND session_id=? AND status IN ('Queued','Running','WaitingClarification','NeedsReview'))`, owner, session).Row().Scan(&active); err != nil {
		return nil, false, err
	}
	if active {
		return nil, false, ErrConflict
	}
	var previous []struct{ Query, Output, AgentID string }
	if err = tx.Table("durable_run").Select("spec->>'query' AS query, output, spec->>'agentID' AS agent_id").
		Where("owner_id = ? AND session_id = ? AND status = ?", owner, session, Completed).
		Order("created_at DESC, id DESC").Limit(20).Find(&previous).Error; err != nil {
		return nil, false, err
	}
	var history []Message
	for _, item := range previous {
		if item.AgentID != spec.AgentID {
			return nil, false, ErrConflict
		}
		history = append(history, Message{"assistant", item.Output}, Message{"user", item.Query})
	}

	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	spec.History = history
	b, err = json.Marshal(spec)
	if err != nil {
		return nil, false, err
	}
	id := NewID()
	err = tx.Table("durable_run").Create(map[string]any{
		"id": id, "owner_id": owner, "session_id": session, "request_key": key,
		"request_hash": hash, "spec": string(b), "status": Queued,
		"deadline": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", ttl.Milliseconds()), "max_attempts": maxAttempts,
	}).Error
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, false, ErrConflict
		}
		return nil, false, err
	}
	if err = emit(ctx, tx, id, "RunQueued", map[string]any{"status": Queued}); err != nil {
		return nil, false, err
	}
	r, err := readRun(tx.Table("durable_run").Where("id = ?", id))
	if err != nil {
		return nil, false, err
	}
	return r, true, tx.Commit().Error
}
func (s *Store) Get(ctx context.Context, owner, id string) (*Run, error) {
	return readRun(s.db.WithContext(ctx).Table("durable_run").Where("id = ? AND owner_id = ?", id, owner))
}
func emit(ctx context.Context, tx *gorm.DB, id, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	err = tx.Exec(`WITH n AS (UPDATE durable_run SET event_seq=event_seq+1,updated_at=clock_timestamp() WHERE id=? RETURNING event_seq) INSERT INTO durable_event(run_id,seq,kind,payload) SELECT ?,event_seq,?,? FROM n`, id, id, kind, data).Error
	return err
}
func (s *Store) Claim(ctx context.Context, worker string, lease time.Duration) (*Run, error) {
	if worker == "" || lease <= 0 {
		return nil, ErrConflict
	}
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := readRun(tx.Table("durable_run").Where("(status='Queued' AND available_at<=clock_timestamp()) OR (status='Running' AND lease_until<=clock_timestamp()) OR (status IN ('WaitingClarification','NeedsReview') AND deadline<=clock_timestamp())").Order("available_at,created_at").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var expired bool
	if err = tx.Raw(`SELECT deadline<=clock_timestamp() FROM durable_run WHERE id=?`, r.ID).Row().Scan(&expired); err != nil {
		return nil, err
	}
	if expired || r.Attempt >= r.MaxAttempts {
		reason := "execution deadline exceeded"
		if !expired {
			reason = "execution attempts exhausted"
		}
		err = tx.Exec(`UPDATE durable_run SET status='Failed',epoch=epoch+1,lease_until=NULL,error=? WHERE id=?`, reason, r.ID).Error
		if err != nil {
			return nil, err
		}
		if err = emit(ctx, tx, r.ID, "RunFailed", map[string]any{"error": reason}); err != nil {
			return nil, err
		}
		if err = tx.Commit().Error; err != nil {
			return nil, err
		}
		return nil, ErrNoWork
	}
	r, err = readRun(tx.Raw(`UPDATE durable_run SET status='Running',worker_id=?,epoch=epoch+1,attempt=attempt+1,lease_until=clock_timestamp()+?*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=? RETURNING `+columns, worker, lease.Milliseconds(), r.ID))
	if err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, r.ID, "RunStarted", map[string]any{"epoch": r.Epoch, "attempt": r.Attempt}); err != nil {
		return nil, err
	}
	return r, tx.Commit().Error
}
func lockLease(ctx context.Context, tx *gorm.DB, l Lease) (*Run, error) {
	r, err := readRun(tx.Table("durable_run").Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).Clauses(clause.Locking{Strength: "UPDATE"}))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseLost
	}
	return r, err
}
func (s *Store) Renew(ctx context.Context, l Lease, d time.Duration) error {
	tag := s.db.WithContext(ctx).Exec(`UPDATE durable_run SET lease_until=clock_timestamp()+?*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=? AND worker_id=? AND epoch=? AND status='Running' AND lease_until>clock_timestamp() AND deadline>clock_timestamp()`, d.Milliseconds(), l.RunID, l.WorkerID, l.Epoch)
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Save commits a complete execution boundary and its event atomically.
func (s *Store) Save(ctx context.Context, l Lease, state json.RawMessage, status Status, output, detail, kind string) error {
	if status != Running && status != WaitingClarification && status != NeedsReview && status != Completed && status != Failed {
		return ErrConflict
	}
	if !json.Valid(state) {
		return fmt.Errorf("invalid execution state")
	}
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = lockLease(ctx, tx, l); err != nil {
		return err
	}
	err = tx.Exec(`UPDATE durable_run SET state=?,status=?,output=?,error=?,lease_until=CASE WHEN ?='Running' THEN lease_until ELSE NULL END WHERE id=?`, []byte(state), status, output, detail, status, l.RunID).Error
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, kind, map[string]any{"status": status, "output": output, "detail": detail, "waitingStep": waitingStep(state)}); err != nil {
		return err
	}
	return tx.Commit().Error
}

// BeginStep records intent BEFORE invoking a remote tool. Unsafe incomplete calls are never automatically repeated.
func (s *Store) BeginStep(ctx context.Context, l Lease, id, input string, policy Policy) (string, bool, error) {
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if _, err = lockLease(ctx, tx, l); err != nil {
		return "", false, err
	}
	var step struct {
		InputHash, Result, Status string
		Policy                    Policy
	}
	err = tx.Table("durable_step").Where("run_id = ? AND step_id = ?", l.RunID, id).Take(&step).Error
	if err == nil {
		if step.InputHash != Hash([]byte(input)) || policy != step.Policy {
			return "", false, ErrConflict
		}
		if step.Status == "Completed" {
			return step.Result, true, nil
		}
		if policy == Unsafe {
			return "", false, ErrUncertain
		}
		return "", false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, err
	}
	err = tx.Table("durable_step").Create(map[string]any{"run_id": l.RunID, "step_id": id, "input_hash": Hash([]byte(input)), "policy": policy, "status": "Started"}).Error
	if err != nil {
		return "", false, err
	}
	if err = emit(ctx, tx, l.RunID, "ToolStarted", map[string]any{"stepID": id, "policy": policy}); err != nil {
		return "", false, err
	}
	return "", false, tx.Commit().Error
}
func (s *Store) FinishStep(ctx context.Context, l Lease, id, result string, state json.RawMessage) error {
	if !json.Valid(state) {
		return ErrConflict
	}
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = lockLease(ctx, tx, l); err != nil {
		return err
	}
	tag := tx.Table("durable_step").Where("run_id = ? AND step_id = ? AND status = ?", l.RunID, id, "Started").Updates(map[string]any{"status": "Completed", "result": result})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	if err = tx.Table("durable_run").Where("id = ?", l.RunID).Update("state", string(state)).Error; err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "ToolCompleted", map[string]any{"stepID": id, "result": result}); err != nil {
		return err
	}
	return tx.Commit().Error
}
func (s *Store) Retry(ctx context.Context, l Lease, state json.RawMessage, reason string, delay time.Duration) error {
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	status := Queued
	if r.Attempt >= r.MaxAttempts {
		status = Failed
	}
	err = tx.Exec(`UPDATE durable_run SET state=?,status=?,error=?,lease_until=NULL,available_at=clock_timestamp()+?*interval '1 millisecond' WHERE id=?`, []byte(state), status, reason, delay.Milliseconds(), l.RunID).Error
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "RunRetry", map[string]any{"status": status, "detail": reason}); err != nil {
		return err
	}
	return tx.Commit().Error
}
func (s *Store) Cancel(ctx context.Context, owner, id string) error {
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readRun(tx.Table("durable_run").Where("id = ? AND owner_id = ?", id, owner).Clauses(clause.Locking{Strength: "UPDATE"}))
	if err != nil {
		return err
	}
	if r.Status.Terminal() {
		return nil
	}
	if err = tx.Exec(`UPDATE durable_run SET status='Canceled',epoch=epoch+1,lease_until=NULL WHERE id=?`, id).Error; err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunCanceled", map[string]any{"status": Canceled}); err != nil {
		return err
	}
	return tx.Commit().Error
}

// SupplyResult resumes a paused step. A review result must come from a human-confirmed external outcome.
func (s *Store) SupplyResult(ctx context.Context, owner, id, step, result string, expected Status) error {
	if expected != WaitingClarification && expected != NeedsReview {
		return ErrConflict
	}
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readRun(tx.Table("durable_run").Where("id = ? AND owner_id = ?", id, owner).Clauses(clause.Locking{Strength: "UPDATE"}))
	if err != nil {
		return err
	}
	var saved struct{ Result, Status string }
	err = tx.Table("durable_step").Select("result", "status").Where("run_id = ? AND step_id = ?", id, step).Take(&saved).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if saved.Status == "Completed" {
		if saved.Result == result {
			return nil
		}
		return ErrConflict
	}
	if r.Status != expected {
		return ErrConflict
	}
	// State is JSON owned by the executor; the waitingStep field is part of the API/worker contract.
	var wait struct {
		WaitingStep string `json:"waitingStep"`
	}
	if err = json.Unmarshal(r.State, &wait); err != nil {
		return err
	}
	if wait.WaitingStep != step {
		return ErrConflict
	}
	var expired bool
	if err = tx.Raw(`SELECT deadline<=clock_timestamp() FROM durable_run WHERE id=?`, id).Row().Scan(&expired); err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if err = tx.Table("durable_step").Where("run_id = ? AND step_id = ?", id, step).Updates(map[string]any{"status": "Completed", "result": result}).Error; err != nil {
		return err
	}
	if err = tx.Exec(`UPDATE durable_run SET status='Queued',attempt=0,available_at=clock_timestamp(),error='' WHERE id=?`, id).Error; err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunResumed", map[string]any{"stepID": step}); err != nil {
		return err
	}
	return tx.Commit().Error
}
func (s *Store) Events(ctx context.Context, owner, id string, after int64) ([]Event, error) {
	if _, err := s.Get(ctx, owner, id); err != nil {
		return nil, err
	}
	events := []Event{}
	err := s.db.WithContext(ctx).Table("durable_event").
		Select("run_id", "seq", "kind", "payload").
		Where("run_id = ? AND seq > ?", id, after).Order("seq").Limit(200).Find(&events).Error
	return events, err
}

func waitingStep(state json.RawMessage) string {
	var v struct {
		WaitingStep string `json:"waitingStep"`
	}
	_ = json.Unmarshal(state, &v)
	return v.WaitingStep
}

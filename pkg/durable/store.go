package durable

import (
	"context"
	"crypto/sha256"
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
	// Serialize schema changes across concurrently started migration commands.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7412101)").Error; err != nil {
			return err
		}
		return tx.AutoMigrate(&runRecord{}, &stepRecord{}, &eventRecord{})
	})
}

func readRun(query *gorm.DB) (*Run, error) {
	var r runRecord
	if err := query.Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r.run(), nil
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
	err = tx.Model(&runRecord{}).Select("id", "request_hash").Where("owner_id = ? AND request_key = ?", owner, key).Take(&existing).Error
	if err == nil {
		if existing.RequestHash != hash {
			return nil, false, ErrConflict
		}
		r, err := readRun(tx.Model(&runRecord{}).Where("id = ?", existing.ID))
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
	var active int64
	if err = tx.Model(&runRecord{}).Where("owner_id = ? AND session_id = ? AND status IN ?", owner, session, []Status{Queued, Running, WaitingClarification, NeedsReview}).Count(&active).Error; err != nil {
		return nil, false, err
	}
	if active > 0 {
		return nil, false, ErrConflict
	}
	var previous []runRecord
	if err = tx.Model(&runRecord{}).Select("spec", "output").
		Where("owner_id = ? AND session_id = ? AND status = ?", owner, session, Completed).
		Order("created_at DESC, id DESC").Limit(20).Find(&previous).Error; err != nil {
		return nil, false, err
	}
	var history []Message
	for _, item := range previous {
		if item.Spec.AgentID != spec.AgentID {
			return nil, false, ErrConflict
		}
		history = append(history, Message{"assistant", item.Output}, Message{"user", item.Spec.Query})
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
	err = tx.Model(&runRecord{}).Create(map[string]any{
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
	r, err := readRun(tx.Model(&runRecord{}).Where("id = ?", id))
	if err != nil {
		return nil, false, err
	}
	return r, true, tx.Commit().Error
}
func (s *Store) Get(ctx context.Context, owner, id string) (*Run, error) {
	return readRun(s.db.WithContext(ctx).Model(&runRecord{}).Where("id = ? AND owner_id = ?", id, owner))
}
func emit(ctx context.Context, tx *gorm.DB, id, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var updated runRecord
	result := tx.WithContext(ctx).Model(&updated).Where("id = ?", id).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "event_seq"}}}).
		Updates(map[string]any{"event_seq": gorm.Expr("event_seq + 1"), "updated_at": gorm.Expr("clock_timestamp()")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return tx.WithContext(ctx).Create(&eventRecord{RunID: id, Seq: updated.EventSeq, Kind: kind, Payload: json.RawMessage(data)}).Error
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
	r, err := readRun(tx.Model(&runRecord{}).Where("(status='Queued' AND available_at<=clock_timestamp()) OR (status='Running' AND lease_until<=clock_timestamp()) OR (status IN ('WaitingClarification','NeedsReview') AND deadline<=clock_timestamp())").Order("available_at,created_at").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var expired bool
	if err = tx.Model(&runRecord{}).Select("deadline <= clock_timestamp()").Where("id = ?", r.ID).Scan(&expired).Error; err != nil {
		return nil, err
	}
	if expired || r.Attempt >= r.MaxAttempts {
		reason := "execution deadline exceeded"
		if !expired {
			reason = "execution attempts exhausted"
		}
		err = tx.Model(&runRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": Failed, "epoch": gorm.Expr("epoch + 1"), "lease_until": nil, "error": reason}).Error
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
	var claimed runRecord
	err = tx.Model(&claimed).Where("id = ?", r.ID).Clauses(clause.Returning{}).Updates(map[string]any{
		"status": Running, "worker_id": worker, "epoch": gorm.Expr("epoch + 1"), "attempt": gorm.Expr("attempt + 1"),
		"lease_until": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", lease.Milliseconds()), "updated_at": gorm.Expr("clock_timestamp()"),
	}).Error
	r = claimed.run()
	if err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, r.ID, "RunStarted", map[string]any{"epoch": r.Epoch, "attempt": r.Attempt}); err != nil {
		return nil, err
	}
	return r, tx.Commit().Error
}
func lockLease(ctx context.Context, tx *gorm.DB, l Lease) (*Run, error) {
	r, err := readRun(tx.Model(&runRecord{}).Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).Clauses(clause.Locking{Strength: "UPDATE"}))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseLost
	}
	return r, err
}
func (s *Store) Renew(ctx context.Context, l Lease, d time.Duration) error {
	tag := s.db.WithContext(ctx).Model(&runRecord{}).Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).
		Updates(map[string]any{"lease_until": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", d.Milliseconds()), "updated_at": gorm.Expr("clock_timestamp()")})
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
	updates := map[string]any{"state": string(state), "status": status, "output": output, "error": detail}
	if status != Running {
		updates["lease_until"] = nil
	}
	err = tx.Model(&runRecord{}).Where("id = ?", l.RunID).Updates(updates).Error
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
	var step stepRecord
	err = tx.Model(&stepRecord{}).Where("run_id = ? AND step_id = ?", l.RunID, id).Take(&step).Error
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
	err = tx.Create(&stepRecord{RunID: l.RunID, StepID: id, InputHash: Hash([]byte(input)), Policy: policy, Status: "Started"}).Error
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
	tag := tx.Model(&stepRecord{}).Where("run_id = ? AND step_id = ? AND status = ?", l.RunID, id, "Started").Updates(map[string]any{"status": "Completed", "result": result})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	if err = tx.Model(&runRecord{}).Where("id = ?", l.RunID).Update("state", string(state)).Error; err != nil {
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
	err = tx.Model(&runRecord{}).Where("id = ?", l.RunID).Updates(map[string]any{"state": string(state), "status": status, "error": reason, "lease_until": nil, "available_at": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", delay.Milliseconds())}).Error
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
	r, err := readRun(tx.Model(&runRecord{}).Where("id = ? AND owner_id = ?", id, owner).Clauses(clause.Locking{Strength: "UPDATE"}))
	if err != nil {
		return err
	}
	if r.Status.Terminal() {
		return nil
	}
	if err = tx.Model(&runRecord{}).Where("id = ?", id).Updates(map[string]any{"status": Canceled, "epoch": gorm.Expr("epoch + 1"), "lease_until": nil}).Error; err != nil {
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
	r, err := readRun(tx.Model(&runRecord{}).Where("id = ? AND owner_id = ?", id, owner).Clauses(clause.Locking{Strength: "UPDATE"}))
	if err != nil {
		return err
	}
	var saved struct{ Result, Status string }
	err = tx.Model(&stepRecord{}).Select("result", "status").Where("run_id = ? AND step_id = ?", id, step).Take(&saved).Error
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
	if err = tx.Model(&runRecord{}).Select("deadline <= clock_timestamp()").Where("id = ?", id).Scan(&expired).Error; err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if err = tx.Model(&stepRecord{}).Where("run_id = ? AND step_id = ?", id, step).Updates(map[string]any{"status": "Completed", "result": result}).Error; err != nil {
		return err
	}
	if err = tx.Model(&runRecord{}).Where("id = ?", id).Updates(map[string]any{"status": Queued, "attempt": 0, "available_at": gorm.Expr("clock_timestamp()"), "error": ""}).Error; err != nil {
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
	var records []eventRecord
	err := s.db.WithContext(ctx).Model(&eventRecord{}).
		Select("run_id", "seq", "kind", "payload").
		Where("run_id = ? AND seq > ?", id, after).Order("seq").Limit(200).Find(&records).Error
	events := make([]Event, len(records))
	for i, record := range records {
		events[i] = record.event()
	}
	return events, err
}

func waitingStep(state json.RawMessage) string {
	var v struct {
		WaitingStep string `json:"waitingStep"`
	}
	_ = json.Unmarshal(state, &v)
	return v.WaitingStep
}

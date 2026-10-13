package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func Hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

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
	return tx.WithContext(ctx).
		Create(&eventRecord{RunID: id, Seq: updated.EventSeq, Kind: kind, Payload: json.RawMessage(data)}).
		Error
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
	r, err := readRun(
		tx.Model(&runRecord{}).
			Where("(status='Queued' AND available_at<=clock_timestamp()) OR (status='Running' AND lease_until<=clock_timestamp()) OR (status IN ('WaitingClarification','NeedsReview') AND deadline<=clock_timestamp())").
			Order("available_at,created_at").
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}),
	)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var expired bool
	if err = tx.Model(&runRecord{}).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", r.ID).
		Scan(&expired).
		Error; err != nil {
		return nil, err
	}
	if expired || r.Attempt >= r.MaxAttempts {
		reason := "execution deadline exceeded"
		if !expired {
			reason = "execution attempts exhausted"
		}
		err = tx.Model(&runRecord{}).
			Where("id = ?", r.ID).
			Updates(map[string]any{"status": Failed, "epoch": gorm.Expr("epoch + 1"), "lease_until": nil, "error": reason}).
			Error
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
		"status":    Running,
		"worker_id": worker,
		"epoch":     gorm.Expr("epoch + 1"),
		"attempt":   gorm.Expr("attempt + 1"),
		"lease_until": gorm.Expr(
			"clock_timestamp()+?*interval '1 millisecond'",
			lease.Milliseconds(),
		),
		"updated_at": gorm.Expr("clock_timestamp()"),
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
	r, err := readRun(
		tx.Model(&runRecord{}).
			Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseLost
	}
	return r, err
}

func (s *Store) Renew(ctx context.Context, l Lease, d time.Duration) error {
	tag := s.db.WithContext(ctx).
		Model(&runRecord{}).
		Where("id = ? AND worker_id = ? AND epoch = ? AND status = ? AND lease_until > clock_timestamp() AND deadline > clock_timestamp()", l.RunID, l.WorkerID, l.Epoch, Running).
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
func (s *Store) Save(
	ctx context.Context,
	l Lease,
	state json.RawMessage,
	status Status,
	output, detail, kind string,
) error {
	var message *Message
	if status == Completed {
		message = &Message{Role: "assistant", Content: output}
	}
	return s.save(ctx, l, state, status, output, detail, kind, message)
}

// SaveDecision commits the assistant's tool calls before any tool can execute.
func (s *Store) SaveDecision(ctx context.Context, l Lease, state json.RawMessage, message Message) error {
	if message.Role != "assistant" || len(message.ToolCalls) == 0 {
		return ErrInvalid
	}
	return s.save(ctx, l, state, Running, message.Content, "", "ModelCompleted", &message)
}

func (s *Store) save(
	ctx context.Context,
	l Lease,
	state json.RawMessage,
	status Status,
	output, detail, kind string,
	message *Message,
) error {
	if status != Running && status != WaitingClarification && status != NeedsReview && status != Completed &&
		status != Failed {
		return ErrConflict
	}
	if !json.Valid(state) {
		return errors.New("invalid execution state")
	}
	tx := s.db.WithContext(ctx).Begin()
	err := tx.Error
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, tx, l)
	if err != nil {
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
	if message != nil {
		messageKind := "Assistant"
		if status == Completed {
			messageKind = "Final"
		}
		if err = appendMessage(tx, run, messageKind, *message); err != nil {
			return err
		}
	}
	if err = emit(
		ctx,
		tx,
		l.RunID,
		kind,
		map[string]any{"status": status, "output": output, "detail": detail, "waitingStep": waitingStep(state)},
	); err != nil {
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
	err = tx.Create(
		&stepRecord{
			RunID:     l.RunID,
			StepID:    id,
			InputHash: Hash([]byte(input)),
			Policy:    policy,
			Status:    "Started",
		},
	).Error
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
	run, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	tag := tx.Model(&stepRecord{}).
		Where("run_id = ? AND step_id = ? AND status = ?", l.RunID, id, "Started").
		Updates(map[string]any{"status": "Completed", "result": result})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	if err = tx.Model(&runRecord{}).Where("id = ?", l.RunID).Update("state", string(state)).Error; err != nil {
		return err
	}
	if err = appendMessage(tx, run, "Tool", Message{Role: "tool", Content: result, ToolCallID: id}); err != nil {
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
	err = tx.Model(&runRecord{}).
		Where("id = ?", l.RunID).
		Updates(map[string]any{"state": string(state), "status": status, "error": reason, "lease_until": nil, "available_at": gorm.Expr("clock_timestamp()+?*interval '1 millisecond'", delay.Milliseconds())}).
		Error
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "RunRetry", map[string]any{"status": status, "detail": reason}); err != nil {
		return err
	}
	return tx.Commit().Error
}

func (s *Store) Cancel(ctx context.Context, owner, id string) error {
	return s.db.WithContext(ctx).
		Transaction(func(tx *gorm.DB) error { _, err := cancelRun(ctx, tx, owner, id); return err })
}

func cancelRun(ctx context.Context, tx *gorm.DB, owner, id string) (bool, error) {
	r, err := readRun(
		tx.Model(&runRecord{}).
			Where("id = ? AND owner_id = ?", id, owner).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return false, err
	}
	if r.Status.Terminal() {
		return false, nil
	}
	if err = tx.Model(&runRecord{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": Canceled, "epoch": gorm.Expr("epoch + 1"), "lease_until": nil}).
		Error; err != nil {
		return false, err
	}
	return true, emit(ctx, tx, id, "RunCanceled", map[string]any{"status": Canceled})
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
	r, err := readRun(
		tx.Model(&runRecord{}).
			Where("id = ? AND owner_id = ?", id, owner).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return err
	}
	var saved struct{ Result, Status string }
	err = tx.Model(&stepRecord{}).
		Select("result", "status").
		Where("run_id = ? AND step_id = ?", id, step).
		Take(&saved).
		Error
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
	if err = tx.Model(&runRecord{}).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", id).
		Scan(&expired).
		Error; err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if err = tx.Model(&stepRecord{}).
		Where("run_id = ? AND step_id = ?", id, step).
		Updates(map[string]any{"status": "Completed", "result": result}).
		Error; err != nil {
		return err
	}
	if err = tx.Model(&runRecord{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": Queued, "attempt": 0, "available_at": gorm.Expr("clock_timestamp()"), "error": ""}).
		Error; err != nil {
		return err
	}
	messageKind := "ReviewResult"
	if expected == WaitingClarification {
		messageKind = "ClarificationAnswer"
	}
	if err = appendMessage(
		tx,
		r,
		messageKind,
		Message{Role: "tool", Content: result, ToolCallID: step},
	); err != nil {
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
		Select("run_id", "seq", "kind", "payload", "created_at").
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

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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Store, error) {
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &Store{pool: p}, nil
}
func (s *Store) Close() { s.pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7412101)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schemaSQL); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const columns = `id,owner_id,session_id,spec,state,status,worker_id,epoch,lease_until,deadline,attempt,max_attempts,event_seq,output,error`

func scan(row pgx.Row) (*Run, error) {
	var r Run
	var spec []byte
	err := row.Scan(&r.ID, &r.OwnerID, &r.SessionID, &spec, &r.State, &r.Status, &r.WorkerID, &r.Epoch, &r.LeaseUntil, &r.Deadline, &r.Attempt, &r.MaxAttempts, &r.EventSeq, &r.Output, &r.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(spec, &r.Spec); err != nil {
		return nil, err
	}
	var wait struct {
		WaitingStep string `json:"waitingStep"`
	}
	if err = json.Unmarshal(r.State, &wait); err != nil {
		return nil, err
	}
	r.WaitingStep = wait.WaitingStep
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	// Serialize duplicate submissions without taking a global lock.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, owner+":"+key); err != nil {
		return nil, false, err
	}
	var id, oldHash string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM durable_run WHERE owner_id=$1 AND request_key=$2`, owner, key).Scan(&id, &oldHash)
	if err == nil {
		if oldHash != hash {
			return nil, false, ErrConflict
		}
		r, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE id=$1`, id))
		return r, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	// Serialize a session's history snapshot with new submissions.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, owner+":"+session); err != nil {
		return nil, false, err
	}
	// A run may finish concurrently. Reject an active predecessor BEFORE reading history,
	// otherwise it could finish between the snapshot and the unique-index check.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM durable_run WHERE owner_id=$1 AND session_id=$2 AND status IN ('Queued','Running','WaitingClarification','NeedsReview'))`, owner, session).Scan(&active); err != nil {
		return nil, false, err
	}
	if active {
		return nil, false, ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT spec->>'query',output,spec->>'agentID' FROM durable_run WHERE owner_id=$1 AND session_id=$2 AND status='Completed' ORDER BY created_at DESC,id DESC LIMIT 20`, owner, session)
	if err != nil {
		return nil, false, err
	}
	var history []Message
	for rows.Next() {
		var query, answer, agent string
		if err = rows.Scan(&query, &answer, &agent); err != nil {
			rows.Close()
			return nil, false, err
		}
		if agent != spec.AgentID {
			rows.Close()
			return nil, false, ErrConflict
		}
		history = append(history, Message{"assistant", answer}, Message{"user", query})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	spec.History = history
	b, err = json.Marshal(spec)
	if err != nil {
		return nil, false, err
	}
	id = NewID()
	_, err = tx.Exec(ctx, `INSERT INTO durable_run(id,owner_id,session_id,request_key,request_hash,spec,status,deadline,max_attempts) VALUES($1,$2,$3,$4,$5,$6,'Queued',clock_timestamp()+$7*interval '1 millisecond',$8)`, id, owner, session, key, hash, b, ttl.Milliseconds(), maxAttempts)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return nil, false, ErrConflict
		}
		return nil, false, err
	}
	if err = emit(ctx, tx, id, "RunQueued", map[string]any{"status": Queued}); err != nil {
		return nil, false, err
	}
	r, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE id=$1`, id))
	if err != nil {
		return nil, false, err
	}
	return r, true, tx.Commit(ctx)
}
func (s *Store) Get(ctx context.Context, owner, id string) (*Run, error) {
	return scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE id=$1 AND owner_id=$2`, id, owner))
}
func emit(ctx context.Context, tx pgx.Tx, id, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH n AS (UPDATE durable_run SET event_seq=event_seq+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING event_seq) INSERT INTO durable_event(run_id,seq,kind,payload) SELECT $1,event_seq,$2,$3 FROM n`, id, kind, data)
	return err
}
func (s *Store) Claim(ctx context.Context, worker string, lease time.Duration) (*Run, error) {
	if worker == "" || lease <= 0 {
		return nil, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE (status='Queued' AND available_at<=clock_timestamp()) OR (status='Running' AND lease_until<=clock_timestamp()) OR (status IN ('WaitingClarification','NeedsReview') AND deadline<=clock_timestamp()) ORDER BY available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var expired bool
	if err = tx.QueryRow(ctx, `SELECT deadline<=clock_timestamp() FROM durable_run WHERE id=$1`, r.ID).Scan(&expired); err != nil {
		return nil, err
	}
	if expired || r.Attempt >= r.MaxAttempts {
		reason := "execution deadline exceeded"
		if !expired {
			reason = "execution attempts exhausted"
		}
		_, err = tx.Exec(ctx, `UPDATE durable_run SET status='Failed',epoch=epoch+1,lease_until=NULL,error=$2 WHERE id=$1`, r.ID, reason)
		if err != nil {
			return nil, err
		}
		if err = emit(ctx, tx, r.ID, "RunFailed", map[string]any{"error": reason}); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrNoWork
	}
	r, err = scan(tx.QueryRow(ctx, `UPDATE durable_run SET status='Running',worker_id=$2,epoch=epoch+1,attempt=attempt+1,lease_until=clock_timestamp()+$3*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=$1 RETURNING `+columns, r.ID, worker, lease.Milliseconds()))
	if err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, r.ID, "RunStarted", map[string]any{"epoch": r.Epoch, "attempt": r.Attempt}); err != nil {
		return nil, err
	}
	return r, tx.Commit(ctx)
}
func lockLease(ctx context.Context, tx pgx.Tx, l Lease) (*Run, error) {
	r, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE id=$1 AND worker_id=$2 AND epoch=$3 AND status='Running' AND lease_until>clock_timestamp() AND deadline>clock_timestamp() FOR UPDATE`, l.RunID, l.WorkerID, l.Epoch))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseLost
	}
	return r, err
}
func (s *Store) Renew(ctx context.Context, l Lease, d time.Duration) error {
	tag, err := s.pool.Exec(ctx, `UPDATE durable_run SET lease_until=clock_timestamp()+$4*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=$1 AND worker_id=$2 AND epoch=$3 AND status='Running' AND lease_until>clock_timestamp() AND deadline>clock_timestamp()`, l.RunID, l.WorkerID, l.Epoch, d.Milliseconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockLease(ctx, tx, l); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE durable_run SET state=$2,status=$3,output=$4,error=$5,lease_until=CASE WHEN $3='Running' THEN lease_until ELSE NULL END WHERE id=$1`, l.RunID, []byte(state), status, output, detail)
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, kind, map[string]any{"status": status, "output": output, "detail": detail, "waitingStep": waitingStep(state)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BeginStep records intent BEFORE invoking a remote tool. Unsafe incomplete calls are never automatically repeated.
func (s *Store) BeginStep(ctx context.Context, l Lease, id, input string, policy Policy) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockLease(ctx, tx, l); err != nil {
		return "", false, err
	}
	var hash, result, status string
	var oldPolicy Policy
	err = tx.QueryRow(ctx, `SELECT input_hash,result,status,policy FROM durable_step WHERE run_id=$1 AND step_id=$2`, l.RunID, id).Scan(&hash, &result, &status, &oldPolicy)
	if err == nil {
		if hash != Hash([]byte(input)) || policy != oldPolicy {
			return "", false, ErrConflict
		}
		if status == "Completed" {
			return result, true, nil
		}
		if policy == Unsafe {
			return "", false, ErrUncertain
		}
		return "", false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO durable_step(run_id,step_id,input_hash,policy,status) VALUES($1,$2,$3,$4,'Started')`, l.RunID, id, Hash([]byte(input)), policy)
	if err != nil {
		return "", false, err
	}
	if err = emit(ctx, tx, l.RunID, "ToolStarted", map[string]any{"stepID": id, "policy": policy}); err != nil {
		return "", false, err
	}
	return "", false, tx.Commit(ctx)
}
func (s *Store) FinishStep(ctx context.Context, l Lease, id, result string, state json.RawMessage) error {
	if !json.Valid(state) {
		return ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockLease(ctx, tx, l); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE durable_step SET status='Completed',result=$3 WHERE run_id=$1 AND step_id=$2 AND status='Started'`, l.RunID, id, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE durable_run SET state=$2 WHERE id=$1`, l.RunID, []byte(state)); err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "ToolCompleted", map[string]any{"stepID": id, "result": result}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Retry(ctx context.Context, l Lease, state json.RawMessage, reason string, delay time.Duration) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := lockLease(ctx, tx, l)
	if err != nil {
		return err
	}
	status := Queued
	if r.Attempt >= r.MaxAttempts {
		status = Failed
	}
	_, err = tx.Exec(ctx, `UPDATE durable_run SET state=$2,status=$3,error=$4,lease_until=NULL,available_at=clock_timestamp()+$5*interval '1 millisecond' WHERE id=$1`, l.RunID, []byte(state), status, reason, delay.Milliseconds())
	if err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "RunRetry", map[string]any{"status": status, "detail": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Cancel(ctx context.Context, owner, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return err
	}
	if r.Status.Terminal() {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE durable_run SET status='Canceled',epoch=epoch+1,lease_until=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunCanceled", map[string]any{"status": Canceled}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SupplyResult resumes a paused step. A review result must come from a human-confirmed external outcome.
func (s *Store) SupplyResult(ctx context.Context, owner, id, step, result string, expected Status) error {
	if expected != WaitingClarification && expected != NeedsReview {
		return ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM durable_run WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return err
	}
	var oldResult, status string
	err = tx.QueryRow(ctx, `SELECT result,status FROM durable_step WHERE run_id=$1 AND step_id=$2`, id, step).Scan(&oldResult, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if status == "Completed" {
		if oldResult == result {
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
	if err = tx.QueryRow(ctx, `SELECT deadline<=clock_timestamp() FROM durable_run WHERE id=$1`, id).Scan(&expired); err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE durable_step SET status='Completed',result=$3 WHERE run_id=$1 AND step_id=$2`, id, step, result); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE durable_run SET status='Queued',attempt=0,available_at=clock_timestamp(),error='' WHERE id=$1`, id); err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunResumed", map[string]any{"stepID": step}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Events(ctx context.Context, owner, id string, after int64) ([]Event, error) {
	if _, err := s.Get(ctx, owner, id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT run_id,seq,kind,payload FROM durable_event WHERE run_id=$1 AND seq>$2 ORDER BY seq LIMIT 200`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.RunID, &e.Seq, &e.Kind, &e.Payload); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func waitingStep(state json.RawMessage) string {
	var v struct {
		WaitingStep string `json:"waitingStep"`
	}
	_ = json.Unmarshal(state, &v)
	return v.WaitingStep
}

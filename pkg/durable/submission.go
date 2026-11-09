package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// PrepareSubmission compiles a run snapshot inside the caller transaction.
type PrepareSubmission func(context.Context, *gorm.DB, execution.SubmissionFacts) (execution.PreparedSubmission, error)

func validateSubmitCommand(cmd execution.SubmitCommand) error {
	if cmd.Owner == "" || strings.TrimSpace(cmd.RequestKey) == "" || len(cmd.RequestKey) > 128 ||
		cmd.RequestHash == "" || cmd.TTL <= 0 || cmd.MaxAttempts < 1 {
		return ErrInvalid
	}
	switch cmd.Kind {
	case execution.SubmitChat:
		if cmd.SessionID == "" || strings.TrimSpace(cmd.Query) == "" {
			return ErrInvalid
		}
	case execution.SubmitApplication:
		input := cmd.ExecutionSpec
		if input.Scope == nil || input.Scope.TenantID == "" || input.Scope.ActorID == "" ||
			input.Scope.TaskID == "" || input.Scope.RoundID == "" ||
			strings.TrimSpace(input.Query) == "" ||
			cmd.TTL < time.Second || cmd.TTL > 24*time.Hour ||
			input.AgentID == "" || input.AgentVersion < 1 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Submit accepts a run inside the borrowed transaction.
func (tx *Tx) Submit(
	ctx context.Context,
	cmd execution.SubmitCommand,
	prepare PrepareSubmission,
) (execution.Submission, error) {
	if err := validateSubmitCommand(cmd); err != nil {
		return execution.Submission{}, err
	}
	if prepare == nil {
		return execution.Submission{}, ErrInvalid
	}
	var run *Run
	created := false
	err := func() error {
		if err := tx.db.Exec(
			`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
			cmd.Owner+":"+cmd.RequestKey,
		).Error; err != nil {
			return err
		}
		var existing table.Run
		err := tx.db.Where("owner_id = ? AND request_key = ?", cmd.Owner, cmd.RequestKey).Take(&existing).Error
		if err == nil {
			if existing.RequestHash != cmd.RequestHash {
				return ErrConflict
			}
			run = runFromRow(existing)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		sessionID, sessionAgentID, err := lockSubmitSession(ctx, tx.db, cmd)
		if err != nil {
			return err
		}
		if err = ensureNoActiveRun(tx.db, cmd.Owner, sessionID); err != nil {
			return err
		}
		prepared, err := prepare(ctx, tx.db, execution.SubmissionFacts{
			Owner:          cmd.Owner,
			SessionID:      sessionID,
			SessionAgentID: sessionAgentID,
			Kind:           cmd.Kind,
			Query:          cmd.Query,
			ExecutionSpec:  cmd.ExecutionSpec,
		})
		if err != nil {
			return err
		}
		if err = validateContextSnapshot(prepared.ContextSnapshot); err != nil {
			return err
		}
		snapshotData, err := json.Marshal(prepared.ContextSnapshot)
		if err != nil {
			return err
		}
		data, err := json.Marshal(prepared.Spec)
		if err != nil {
			return err
		}
		id := NewID()
		if err = tx.db.Model(&table.Run{}).Create(map[string]any{
			"id":               id,
			"owner_id":         cmd.Owner,
			"session_id":       sessionID,
			"request_key":      cmd.RequestKey,
			"request_hash":     cmd.RequestHash,
			"spec":             string(data),
			"context_snapshot": string(snapshotData),
			"status":           Queued,
			"deadline": gorm.Expr(
				"clock_timestamp()+?*interval '1 millisecond'",
				cmd.TTL.Milliseconds(),
			),
			"max_attempts": cmd.MaxAttempts,
		}).Error; err != nil {
			return recordError(err)
		}
		run, err = readRun(tx.db.Where("id = ?", id))
		if err != nil {
			return err
		}
		if err = appendMessage(tx.db, run, "User", prepared.UserMessage); err != nil {
			return err
		}
		if err = emit(ctx, tx.db, id, "RunQueued", map[string]any{"status": Queued}); err != nil {
			return err
		}
		run.EventSeq = 1
		created = true
		return nil
	}()
	if err != nil {
		return execution.Submission{}, err
	}
	return execution.Submission{Run: run, Created: created}, nil
}

func lockSubmitSession(ctx context.Context, tx *gorm.DB, cmd execution.SubmitCommand) (string, string, error) {
	switch cmd.Kind {
	case execution.SubmitChat:
		var conversation table.Session
		if err := tx.WithContext(ctx).Where("owner_id = ? AND id = ?", cmd.Owner, cmd.SessionID).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&conversation).
			Error; err != nil {
			return "", "", recordError(err)
		}
		return conversation.ID, conversation.AgentID, nil
	case execution.SubmitApplication:
		input := cmd.ExecutionSpec
		identity, _ := json.Marshal(
			[]string{cmd.Owner, input.Scope.TenantID, input.Scope.ProjectID, input.Scope.TaskID},
		)
		sessionID := "application_" + Hash(identity)
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", sessionID).Error; err != nil {
			return "", "", err
		}
		var agentRow table.Agent
		if err := tx.Where("owner_id = ? AND id = ?", cmd.Owner, input.AgentID).
			Take(&agentRow).
			Error; err != nil {
			return "", "", recordError(err)
		}
		var session table.Session
		err := tx.Where("owner_id = ? AND id = ?", cmd.Owner, sessionID).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&session).
			Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			session = table.Session{
				ID:      sessionID,
				OwnerID: cmd.Owner,
				AgentID: agentRow.ID,
				Title:   input.Scope.TaskID,
			}
			if err = tx.Create(&session).Error; err != nil {
				return "", "", err
			}
		} else if err != nil {
			return "", "", err
		} else if session.AgentID != agentRow.ID {
			return "", "", ErrConflict
		}
		return session.ID, session.AgentID, nil
	default:
		return "", "", ErrInvalid
	}
}

func ensureNoActiveRun(tx *gorm.DB, owner, sessionID string) error {
	var active int64
	if err := tx.Model(&table.Run{}).
		Where("owner_id = ? AND session_id = ? AND status IN ?", owner, sessionID, []Status{
			Queued, Running, WaitingClarification, NeedsReview,
		}).
		Count(&active).
		Error; err != nil {
		return err
	}
	if active > 0 {
		return ErrConflict
	}
	return nil
}

// Accept runs the unified submit algorithm in a new store-owned transaction.
func (s *Store) Accept(
	ctx context.Context,
	cmd execution.SubmitCommand,
	prepare PrepareSubmission,
) (*Run, bool, error) {
	return s.accept(ctx, cmd, prepare)
}

func (s *Store) accept(
	ctx context.Context,
	cmd execution.SubmitCommand,
	prepare PrepareSubmission,
) (*Run, bool, error) {
	var result execution.Submission
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var submitErr error
		result, submitErr = BindTransaction(tx).Submit(ctx, cmd, prepare)
		return submitErr
	})
	if err != nil {
		return nil, false, err
	}
	return result.Run, result.Created, nil
}

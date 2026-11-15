package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func (s *Store) QueueGoalIteration(ctx context.Context) (*Run, error) {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	defer tx.Rollback()
	var goal table.Goal
	err := tx.Where("durable_goal.status = ?", GoalActive).
		Where(`EXISTS (
			SELECT 1 FROM durable_run AS previous
			JOIN durable_evaluation AS receipt ON receipt.run_id = previous.id AND receipt.goal_id = durable_goal.id
			WHERE previous.id = durable_goal.active_run_id AND previous.owner_id = durable_goal.owner_id
			  AND previous.goal_id = durable_goal.id AND previous.status = 'Completed'
			  AND previous.stall_mark <> '' AND receipt.status = 'Failed'
			  AND NOT EXISTS (
				SELECT 1 FROM durable_run_environment AS env
				WHERE env.run_id = previous.id AND env.status IN ('Preparing', 'Active'))
		)`).
		Where(`NOT EXISTS (
			SELECT 1 FROM durable_run AS busy
			WHERE busy.owner_id = durable_goal.owner_id AND busy.session_id = durable_goal.session_id
			  AND busy.status IN ('Queued', 'Running', 'WaitingClarification', 'NeedsReview', 'WaitingExternal')
		)`).
		Order("durable_goal.updated_at, durable_goal.id").
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Take(&goal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	var session table.Session
	err = tx.Where("owner_id = ? AND id = ?", goal.OwnerID, goal.SessionID).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, err
	}
	if err = ensureNoActiveRun(tx, goal.OwnerID, goal.SessionID); err != nil {
		return nil, err
	}
	previous, err := readRun(tx.Where("owner_id = ? AND id = ?", goal.OwnerID, goal.ActiveRunID))
	if err != nil {
		return nil, err
	}
	var now time.Time
	if err = tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return nil, err
	}
	deadline := previous.Deadline
	if goal.Budget.MaxWallClockSeconds > 0 {
		goalDeadline := goal.CreatedAt.Add(time.Duration(goal.Budget.MaxWallClockSeconds) * time.Second)
		if goalDeadline.Before(deadline) {
			deadline = goalDeadline
		}
	}
	reason := ""
	spec := previous.Spec
	switch {
	case previous.SessionID != goal.SessionID || previous.AgentID != session.AgentID:
		reason = "goal and previous run session binding mismatch"
	case !deadline.After(now):
		reason = "goal continuation deadline exceeded"
	case goal.StallCount >= 3:
		reason = "goal stalled for three runs"
	case goal.Budget.MaxTokens > 0 && goal.TokensUsed >= goal.Budget.MaxTokens:
		reason = "goal token budget exhausted"
	case goal.Budget.MaxCostUSD > 0 && goal.CostUSD >= goal.Budget.MaxCostUSD:
		reason = "goal cost budget exhausted"
	}
	if reason == "" && spec.Filesystem != nil {
		filesystem := *spec.Filesystem
		workspace := path.Clean(filesystem.Workspace)
		if filesystem.Provider != "Sandbox" ||
			(workspace != "/workspace" && !strings.HasPrefix(workspace, "/workspace/")) {
			reason = "workspace persistence across runs is not configured"
		} else {
			profileID := filesystem.ProfileID
			if profileID == "" {
				profileID = "coding"
			}
			var environments []table.RunEnvironment
			if err = tx.Where("run_id = ? AND run_epoch = ? AND profile_id = ? AND status = ? AND saved_checkpoint <> ''",
				previous.ID, previous.Epoch, profileID, EnvironmentReleased).
				Limit(2).
				Find(&environments).
				Error; err != nil {
				return nil, err
			}
			if len(environments) != 1 || environments[0].SandboxID == "" {
				reason = "workspace has no confirmed checkpoint for the completed run"
			} else {
				filesystem.RestoreFromSandboxID = environments[0].SandboxID
				spec.Filesystem = &filesystem
			}
		}
	}
	if reason != "" {
		if err = tx.Model(&table.Goal{}).Where("id = ?", goal.ID).
			Updates(map[string]any{"status": GoalBlocked, "updated_at": gorm.Expr("clock_timestamp()")}).
			Error; err != nil {
			return nil, err
		}
		if err = emit(
			ctx,
			tx,
			previous.ID,
			"GoalBlocked",
			map[string]any{"goalID": goal.ID, "reason": reason},
		); err != nil {
			return nil, err
		}
		return nil, tx.Commit().Error
	}
	var receipt table.Evaluation
	if err = tx.Where("goal_id = ? AND run_id = ?", goal.ID, previous.ID).Take(&receipt).Error; err != nil {
		return nil, err
	}
	spec.History = nil
	if err = loadConversationHistory(tx, goal.OwnerID, goal.SessionID, &spec); err != nil {
		return nil, err
	}
	spec.Query = fmt.Sprintf(
		"Continue working toward this goal:\n%s\n\nThe previous run failed acceptance. Address this feedback before completing:\n%s",
		goal.Objective,
		receipt.Output,
	)
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	id := NewID()
	if err = tx.Model(&table.Run{}).Create(map[string]any{
		"id": id, "owner_id": goal.OwnerID, "session_id": goal.SessionID,
		"request_key":  "goal-iteration:" + goal.ID + ":" + previous.ID,
		"request_hash": Hash(data), "spec": string(data), "status": Queued,
		"deadline": deadline, "max_attempts": previous.MaxAttempts, "goal_id": goal.ID,
	}).Error; err != nil {
		return nil, recordError(err)
	}
	next, err := readRun(tx.Where("id = ?", id))
	if err != nil {
		return nil, err
	}
	if err = appendMessage(tx, next, "User", Message{Role: "user", Content: spec.Query}); err != nil {
		return nil, err
	}
	if err = tx.Model(&table.Goal{}).Where("id = ?", goal.ID).
		Updates(map[string]any{"active_run_id": id, "updated_at": gorm.Expr("clock_timestamp()")}).
		Error; err != nil {
		return nil, err
	}
	if err = emit(
		ctx,
		tx,
		previous.ID,
		"GoalIterationQueued",
		map[string]any{"goalID": goal.ID, "nextRunID": id},
	); err != nil {
		return nil, err
	}
	if err = emit(ctx, tx, id, "RunQueued", map[string]any{
		"status": Queued, "goalID": goal.ID, "previousRunID": previous.ID,
	}); err != nil {
		return nil, err
	}
	next.EventSeq = 1
	return next, tx.Commit().Error
}

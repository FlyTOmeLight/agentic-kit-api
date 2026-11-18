package durable

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// CreateGoal inserts a new goal row. The request key is used for idempotent
// replay; a second call with the same key returns the existing goal.
func (s *Store) CreateGoal(ctx context.Context, g Goal) (*Goal, bool, error) {
	if g.OwnerID == "" || g.SessionID == "" || g.Objective == "" || g.RequestKey == "" {
		return nil, false, ErrInvalid
	}
	if g.Evaluator.Kind != EvaluatorNone && g.Evaluator.Kind != EvaluatorCommand &&
		g.Evaluator.Kind != EvaluatorHTTP {
		return nil, false, ErrInvalid
	}
	if g.Status != "" && g.Status != GoalActive {
		return nil, false, ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var sessionRow table.Session
	if err := table.TenantQuery(ctx, tx).
		Where("owner_id = ? AND id = ?", g.OwnerID, g.SessionID).
		Take(&sessionRow).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	if err := tx.Exec(
		`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`,
		sessionRow.TenantID+":"+g.OwnerID+":"+g.RequestKey,
	).Error; err != nil {
		return nil, false, err
	}
	var existing table.Goal
	err := table.TenantQuery(ctx, tx).
		Where("tenant_id = ?", sessionRow.TenantID).
		Where("owner_id = ? AND request_key = ?", g.OwnerID, g.RequestKey).
		Take(&existing).
		Error
	if err == nil {
		if existing.RequestHash != g.RequestHash {
			return nil, false, ErrConflict
		}
		return goalFromRow(existing), false, tx.Commit().Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	goal := table.Goal{
		ID:          NewID(),
		TenantID:    sessionRow.TenantID,
		ProjectID:   sessionRow.ProjectID,
		OwnerID:     g.OwnerID,
		SessionID:   g.SessionID,
		RequestKey:  g.RequestKey,
		RequestHash: g.RequestHash,
		Objective:   g.Objective,
		Evaluator:   g.Evaluator,
		Budget:      g.Budget,
		Status:      GoalActive,
		ActiveRunID: g.ActiveRunID,
		StallCount:  0,
		TokensUsed:  0,
		CostUSD:     0,
	}
	if err = tx.Create(&goal).Error; err != nil {
		return nil, false, err
	}
	return goalFromRow(goal), true, tx.Commit().Error
}

// GetGoal returns the goal owned by owner with the given id.
func (s *Store) GetGoal(ctx context.Context, owner, id string) (*Goal, error) {
	var row table.Goal
	if err := table.TenantQuery(ctx, s.db).
		Where("owner_id = ? AND id = ?", owner, id).
		Take(&row).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return goalFromRow(row), nil
}

// ListGoals returns goals for an owner, optionally filtered by status.
func (s *Store) ListGoals(ctx context.Context, owner string, status GoalStatus, limit int) ([]Goal, error) {
	if limit <= 0 {
		limit = 50
	}
	query := table.TenantQuery(ctx, s.db.Model(&table.Goal{})).
		Where("owner_id = ?", owner)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var rows []table.Goal
	if err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	goals := make([]Goal, len(rows))
	for i, r := range rows {
		goals[i] = *goalFromRow(r)
	}
	return goals, nil
}

// TransitionGoal moves a goal to a terminal or paused state. Only legal
// transitions are accepted; the run must not be active.
func (s *Store) TransitionGoal(ctx context.Context, owner, id string, to GoalStatus) error {
	switch to {
	case GoalActive, GoalPaused, GoalBlocked, GoalComplete, GoalFailed:
	default:
		return ErrInvalid
	}
	var from []GoalStatus
	switch to {
	case GoalActive:
		from = []GoalStatus{GoalPaused, GoalBlocked}
	case GoalPaused:
		from = []GoalStatus{GoalActive}
	default:
		from = []GoalStatus{GoalActive, GoalBlocked}
	}
	tag := table.TenantQuery(ctx, s.db).
		Model(&table.Goal{}).
		Where("owner_id = ? AND id = ? AND status IN ?", owner, id, from).
		Updates(map[string]any{"status": to, "updated_at": gorm.Expr("clock_timestamp()")})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// CheckGoalProgress evaluates a finished run against its goal and advances
// the goal state machine. It is called by the control plane after a run
// reaches a terminal status.
//
// Evaluation is deterministic: a run is accepted when it completed without
// error, and the evaluator (if any) is not yet implemented at the storage
// layer. The budget and stall counters are updated here; the evaluator
// command itself is executed by the worker in a sandbox before this call.
func (s *Store) CheckGoalProgress(ctx context.Context, owner, goalID string) (*Goal, error) {
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var goalRow table.Goal
	if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
		Where("owner_id = ? AND id = ?", owner, goalID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&goalRow).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if goalRow.Status != GoalActive {
		return goalFromRow(goalRow), tx.Commit().Error
	}
	if goalRow.ActiveRunID == "" {
		return goalFromRow(goalRow), tx.Commit().Error
	}
	var runRow table.Run
	if err := table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ? AND owner_id = ?", goalRow.ActiveRunID, owner).
		Take(&runRow).
		Error; err != nil {
		return nil, err
	}
	if !runRow.Status.Terminal() || runRow.StallMark != "" {
		return goalFromRow(goalRow), tx.Commit().Error
	}
	// Stall detection: hash the finished run's observable state. A repeated
	// mark means the new run made no progress over the previous one.
	var steps []table.Step
	if err := tx.Model(&table.Step{}).Where("run_id = ?", runRow.ID).Find(&steps).Error; err != nil {
		return nil, err
	}
	currentMark := stallMark(runFromRow(runRow), steps)
	// A run only advances the goal when it completed and, if the goal has an
	// evaluator, that evaluator passed. A failed acceptance counts as no
	// progress so repeated identical failures cannot loop forever.
	passed := runRow.Status == Completed
	undetermined := false
	if runRow.Status == Completed && goalRow.Evaluator.Kind != EvaluatorNone {
		passed = false
		var evaluation table.Evaluation
		err := tx.Where("goal_id = ? AND run_id = ?", goalRow.ID, runRow.ID).
			Take(&evaluation).
			Error
		switch {
		case err == nil:
			passed = evaluation.Status == EvaluationPassed
			// Infrastructure failure is not evidence about the work: keep the
			// goal Active without charging a stall until a real verdict lands.
			undetermined = evaluation.Status == EvaluationError
		case errors.Is(err, gorm.ErrRecordNotFound):
			undetermined = true
		default:
			return nil, err
		}
	}
	switch {
	case passed:
		goalRow.StallCount = 0
		goalRow.LastStallMark = currentMark
	case undetermined:
	default:
		if currentMark == goalRow.LastStallMark {
			goalRow.StallCount++
		} else {
			goalRow.StallCount = 1
		}
		goalRow.LastStallMark = currentMark
	}
	var newStatus GoalStatus
	var now time.Time
	if goalRow.Budget.MaxWallClockSeconds > 0 {
		if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return nil, err
		}
	}
	switch {
	case passed:
		newStatus = GoalComplete
	case goalRow.StallCount >= 3:
		newStatus = GoalBlocked
	case goalRow.Budget.MaxWallClockSeconds > 0 &&
		now.Sub(goalRow.CreatedAt).Seconds() > float64(goalRow.Budget.MaxWallClockSeconds):
		newStatus = GoalBlocked
	case goalRow.Budget.MaxTokens > 0 && goalRow.TokensUsed >= goalRow.Budget.MaxTokens:
		newStatus = GoalBlocked
	case goalRow.Budget.MaxCostUSD > 0 && goalRow.CostUSD >= goalRow.Budget.MaxCostUSD:
		newStatus = GoalBlocked
	default:
		newStatus = GoalActive
	}
	if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
		Where("id = ?", goalRow.ID).
		Updates(map[string]any{
			"status":          newStatus,
			"stall_count":     goalRow.StallCount,
			"last_stall_mark": goalRow.LastStallMark,
			"updated_at":      gorm.Expr("clock_timestamp()"),
		}).
		Error; err != nil {
		return nil, err
	}
	if !undetermined {
		if err := table.TenantQuery(ctx, tx.Model(&table.Run{})).
			Where("id = ?", goalRow.ActiveRunID).
			Update("stall_mark", currentMark).
			Error; err != nil {
			return nil, err
		}
	}
	goalRow.Status = newStatus
	goalRow.UpdatedAt = time.Now().UTC()
	return goalFromRow(goalRow), tx.Commit().Error
}

// BindGoalToRun associates a run with a goal in both directions. It is called
// once when the run is created; subsequent calls are idempotent.
func (s *Store) BindGoalToRun(ctx context.Context, owner, goalID, runID string) error {
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	tag := table.TenantQuery(ctx, tx).
		Model(&table.Goal{}).
		Where("owner_id = ? AND id = ? AND status = ?", owner, goalID, GoalActive).
		Updates(map[string]any{"active_run_id": runID, "updated_at": gorm.Expr("clock_timestamp()")})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	tag = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("owner_id = ? AND id = ?", owner, runID).
		Update("goal_id", goalID)
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrNotFound
	}
	return tx.Commit().Error
}

func goalFromRow(r table.Goal) *Goal {
	return &Goal{
		ID:            r.ID,
		OwnerID:       r.OwnerID,
		SessionID:     r.SessionID,
		RequestKey:    r.RequestKey,
		Objective:     r.Objective,
		Evaluator:     r.Evaluator,
		Budget:        r.Budget,
		Status:        r.Status,
		ActiveRunID:   r.ActiveRunID,
		StallCount:    r.StallCount,
		LastStallMark: r.LastStallMark,
		TokensUsed:    r.TokensUsed,
		CostUSD:       r.CostUSD,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// SteerGoal queues a steering message for the goal's active run. The run must
// be Running; multiple steers before the next model turn are merged in order.
func (s *Store) SteerGoal(ctx context.Context, owner, goalID, content string) error {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 4096 {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	var goalRow table.Goal
	if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
		Where("owner_id = ? AND id = ?", owner, goalID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&goalRow).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if goalRow.Status != GoalActive || goalRow.ActiveRunID == "" {
		return ErrConflict
	}
	var runRow table.Run
	if err := table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ? AND owner_id = ?", goalRow.ActiveRunID, owner).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&runRow).
		Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrConflict
		}
		return err
	}
	if runRow.Status != Running {
		return ErrConflict
	}
	merged := content
	if runRow.PendingSteer != "" {
		merged = runRow.PendingSteer + "\n" + content
	}
	if len(merged) > 4096 {
		return ErrInvalid
	}
	if err := table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", runRow.ID).
		Updates(map[string]any{
			"pending_steer": merged,
			"updated_at":    gorm.Expr("clock_timestamp()"),
		}).Error; err != nil {
		return err
	}
	// Steering resets the stall counter: the human is actively redirecting.
	if err := table.TenantQuery(ctx, tx.Model(&table.Goal{})).
		Where("id = ?", goalRow.ID).
		Update("stall_count", 0).
		Error; err != nil {
		return err
	}
	if err := emit(ctx, tx, goalRow.ActiveRunID, "SteeringQueued", map[string]any{
		"goalID":  goalID,
		"content": content,
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

// stallMark computes a deterministic fingerprint of a finished run. Two
// consecutive runs with the same mark made no observable progress.
func stallMark(r *Run, steps []table.Step) string {
	completed := make([]string, 0, len(steps))
	for _, s := range steps {
		if s.Status == "Completed" {
			completed = append(completed, s.StepID)
		}
	}
	sort.Strings(completed)
	payload, _ := json.Marshal(struct {
		CompletedSteps []string `json:"completedSteps"`
		ConfigVersion  string   `json:"configVersion"`
		OutputHash     string   `json:"outputHash"`
	}{
		CompletedSteps: completed,
		ConfigVersion:  r.ConfigVersion,
		OutputHash:     fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(r.Output))),
	})
	return fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
}

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
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const childExternalRefPrefix = "child:"

// SpawnChildRun creates a child with the legacy inherited spec and leaves
// suspension to the caller.
func (s *Store) SpawnChildRun(ctx context.Context, l Lease, stepID, query string, ttl time.Duration) (*Run, error) {
	return s.spawnChild(ctx, l, ChildSpawnRequest{StepID: stepID, Query: query, TTL: ttl}, nil, "")
}

// SpawnChildAndAwait queues a child and suspends the parent in the same transaction.
func (s *Store) SpawnChildAndAwait(
	ctx context.Context,
	l Lease,
	req ChildSpawnRequest,
	state json.RawMessage,
	detail string,
) (*Run, error) {
	instruction := strings.TrimSpace(req.Instruction)
	if !json.Valid(state) || strings.TrimSpace(detail) == "" || len(req.Instruction) > 4096 ||
		(req.SubAgent != "" && instruction != "") {
		return nil, ErrInvalid
	}
	req.Instruction = instruction
	return s.spawnChild(ctx, l, req, state, detail)
}

func (s *Store) spawnChild(
	ctx context.Context,
	l Lease,
	req ChildSpawnRequest,
	state json.RawMessage,
	detail string,
) (*Run, error) {
	if req.StepID == "" || strings.TrimSpace(req.Query) == "" || req.TTL <= 0 {
		return nil, ErrInvalid
	}
	var child *Run
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parentQuery := table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", l.RunID)
		var binding table.Run
		if err := parentQuery.Session(&gorm.Session{}).Select("tenant_id", "project_id", "owner_id", "goal_id").
			Take(&binding).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseLost
			}
			return err
		}
		goalDeadline, err := lockChildSpawnGoal(ctx, tx, binding)
		if err != nil {
			return err
		}
		parent, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
		if errors.Is(err, ErrLeaseLost) {
			var expired bool
			if deadlineErr := parentQuery.Session(&gorm.Session{}).
				Where("status = ? AND worker_id = ? AND epoch = ?", Running, l.WorkerID, l.Epoch).
				Select("deadline <= clock_timestamp()").Scan(&expired).Error; deadlineErr != nil {
				return deadlineErr
			}
			if expired {
				return ErrConflict
			}
		}
		if err != nil {
			return err
		}
		if parent.ParentRunID != "" {
			return ErrInvalid
		}
		if parent.GoalID != binding.GoalID {
			return ErrConflict
		}
		var step table.Step
		if err = tx.Where("run_id = ? AND step_id = ?", parent.ID, req.StepID).
			Clauses(clause.Locking{Strength: "UPDATE"}).Take(&step).Error; err != nil {
			return recordError(err)
		}
		if step.Status != "Started" || step.ExternalRef != "" {
			return ErrConflict
		}

		spec, err := childSpec(parent.Spec, req, state != nil)
		if err != nil {
			return err
		}
		snapshot, err := freezeContextMaterials(spec, nil, "", memory.SelectionOff, nil)
		if err != nil {
			return err
		}
		if err = validateContextSnapshot(snapshot); err != nil {
			return err
		}
		var now time.Time
		if err = tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return err
		}
		deadline := now.Add(req.TTL)
		if parent.Deadline.Before(deadline) {
			deadline = parent.Deadline
		}
		if !goalDeadline.IsZero() && goalDeadline.Before(deadline) {
			deadline = goalDeadline
		}
		if !deadline.After(now) {
			return ErrConflict
		}
		session := table.Session{
			ID:        NewID(),
			TenantID:  binding.TenantID,
			ProjectID: binding.ProjectID,
			OwnerID:   binding.OwnerID,
			AgentID:   spec.AgentID,
			Title:     "child of " + parent.ID,
		}
		if err = tx.Create(&session).Error; err != nil {
			return recordError(err)
		}
		row := table.Run{
			ID:              NewID(),
			TenantID:        session.TenantID,
			ProjectID:       session.ProjectID,
			OwnerID:         session.OwnerID,
			SessionID:       session.ID,
			RequestKey:      parent.ID + ":" + req.StepID,
			RequestHash:     Hash([]byte(spec.Query)),
			Spec:            table.ExecutionSnapshot(spec),
			ContextSnapshot: table.ContextSnapshot(snapshot),
			Status:          Queued,
			Deadline:        deadline,
			MaxAttempts:     parent.MaxAttempts,
			GoalID:          parent.GoalID,
			ParentRunID:     parent.ID,
			ParentStepID:    req.StepID,
		}
		if err = tx.Create(&row).Error; err != nil {
			return recordError(err)
		}
		child = runFromRow(row)
		if err = appendMessage(tx, child, "User", Message{Role: "user", Content: spec.Query}); err != nil {
			return err
		}
		ref := childExternalRefPrefix + child.ID
		if err = tx.Model(&step).Update("external_ref", ref).Error; err != nil {
			return err
		}
		if state != nil {
			if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", parent.ID).
				Updates(map[string]any{
					"state":                string(state),
					"status":               WaitingExternal,
					"output":               "",
					"error":                detail,
					"waiting_step":         req.StepID,
					"waiting_kind":         "",
					"waiting_external_ref": ref,
					"lease_until":          nil,
					"worker_id":            "",
				}).Error; err != nil {
				return err
			}
		}
		if err = emit(ctx, tx, parent.ID, "ChildRunSpawned", map[string]any{
			"stepID":   req.StepID,
			"childID":  child.ID,
			"subAgent": req.SubAgent,
			"agentID":  child.AgentID,
		}); err != nil {
			return err
		}
		if state != nil {
			if err = emit(ctx, tx, parent.ID, "ExternalWaitRequested", map[string]any{
				"status":      WaitingExternal,
				"output":      "",
				"detail":      detail,
				"waitingStep": req.StepID,
				"waitingKind": "",
			}); err != nil {
				return err
			}
		}
		return emit(ctx, tx, child.ID, "RunQueued", map[string]any{"status": Queued})
	})
	if err != nil {
		return nil, err
	}
	child.EventSeq = 1
	return child, nil
}

func lockChildSpawnGoal(ctx context.Context, tx *gorm.DB, binding table.Run) (time.Time, error) {
	if binding.GoalID == "" {
		return time.Time{}, nil
	}
	var goal table.Goal
	if err := table.TenantQuery(ctx, tx).
		Where("tenant_id = ? AND owner_id = ? AND id = ?", binding.TenantID, binding.OwnerID, binding.GoalID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&goal).Error; err != nil {
		return time.Time{}, recordError(err)
	}
	if goal.Budget.MaxWallClockSeconds <= 0 {
		return time.Time{}, nil
	}
	deadline := goal.CreatedAt.Add(time.Duration(goal.Budget.MaxWallClockSeconds) * time.Second)
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return time.Time{}, err
	}
	if !deadline.After(now) {
		return time.Time{}, ErrConflict
	}
	return deadline, nil
}

func cloneChildFilesystem(fs *FilesystemBinding) *FilesystemBinding {
	if fs == nil {
		return nil
	}
	copied := *fs
	copied.RestoreFromSandboxID = ""
	return &copied
}

func childSpec(parent Spec, req ChildSpawnRequest, await bool) (Spec, error) {
	if req.SubAgent != "" {
		for _, sub := range parent.SubAgents {
			if sub.Name != req.SubAgent {
				continue
			}
			spec := Spec{
				AgentID:         sub.AgentID,
				ConfigVersion:   sub.ConfigVersion,
				Query:           req.Query,
				SystemPrompt:    sub.SystemPrompt,
				Model:           sub.Model,
				Tools:           sub.Tools,
				MCPServers:      sub.MCPServers,
				Toolsets:        sub.Toolsets,
				MaxTurns:        sub.MaxTurns,
				PricingFrozen:   sub.PricingFrozen,
				PricingSnapshot: sub.PricingSnapshot,
				Scope:           parent.Scope,
			}
			if len(spec.Toolsets) > 0 {
				spec.Filesystem = cloneChildFilesystem(parent.Filesystem)
			}
			return spec, nil
		}
		return Spec{}, ErrInvalid
	}
	if await && !parent.SelfDelegate {
		return Spec{}, ErrInvalid
	}
	parent.Query = req.Query
	if req.Instruction != "" {
		parent.Query = "[Subtask Instructions]\n" + req.Instruction + "\n\n[Task]\n" + req.Query
	}
	parent.Filesystem = cloneChildFilesystem(parent.Filesystem)
	parent.History = nil
	parent.SubAgents = nil
	parent.SelfDelegate = false
	return parent, nil
}

func lockParentRun(ctx context.Context, tx *gorm.DB, binding table.Run) error {
	if binding.ParentRunID == "" {
		return nil
	}
	var parent table.Run
	err := table.TenantQuery(ctx, tx).Select("id").
		Where("tenant_id = ? AND id = ?", binding.TenantID, binding.ParentRunID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&parent).Error
	return recordError(err)
}

func lockTerminalLease(ctx context.Context, tx *gorm.DB, l Lease) (*Run, error) {
	var binding table.Run
	if err := table.TenantQuery(ctx, tx).Select("tenant_id", "parent_run_id").
		Where("id = ?", l.RunID).Take(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrLeaseLost
		}
		return nil, err
	}
	if err := lockParentRun(ctx, tx, binding); err != nil {
		return nil, err
	}
	return lockLease(ctx, table.TenantQuery(ctx, tx), l)
}

func finishTerminalRun(ctx context.Context, tx *gorm.DB, run *table.Run) error {
	if err := cascadeCancelChildren(ctx, tx, run); err != nil {
		return err
	}
	return handleChildTerminal(ctx, tx, run)
}

// handleChildTerminal wakes a matched waiting parent or records an orphan.
func handleChildTerminal(ctx context.Context, tx *gorm.DB, child *table.Run) error {
	if child.ParentRunID == "" {
		return nil
	}
	parent, err := readRun(
		table.TenantQuery(ctx, tx.Model(&table.Run{})).
			Where("tenant_id = ? AND id = ?", child.TenantID, child.ParentRunID).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return err
	}
	orphan := func(reason string) error {
		return emit(ctx, tx, child.ID, "ChildOrphaned", map[string]any{
			"parentRunID":  child.ParentRunID,
			"parentStepID": child.ParentStepID,
			"parentStatus": parent.Status,
			"reason":       reason,
		})
	}
	result := child.Output
	if child.Status != Completed {
		reason := child.Error
		if reason == "" {
			reason = string(child.Status)
		}
		result = "subtask " + child.ID + " " + string(child.Status) + ": " + reason
	}
	var step table.Step
	err = tx.Where("run_id = ? AND step_id = ?", parent.ID, child.ParentStepID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&step).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return orphan("parent step not found")
	}
	if err != nil {
		return err
	}
	ref := childExternalRefPrefix + child.ID
	if step.Status == "Completed" && step.Result == result && step.ExternalRef == ref {
		return nil
	}
	if parent.Status != WaitingExternal || parent.WaitingStep != child.ParentStepID ||
		parent.WaitingExternalRef != ref || step.Status != "Started" || step.ExternalRef != ref {
		return orphan("parent is no longer waiting for this child step")
	}
	var expired bool
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", parent.ID).
		Scan(&expired).Error; err != nil {
		return err
	}
	if expired {
		return orphan("parent deadline exceeded")
	}
	completed := tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ? AND status = 'Started' AND external_ref = ?", parent.ID, child.ParentStepID, ref).
		Updates(map[string]any{"status": "Completed", "result": result})
	if completed.Error != nil {
		return completed.Error
	}
	if completed.RowsAffected != 1 {
		return orphan("parent step no longer matches")
	}
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", parent.ID).
		Updates(map[string]any{
			"status":               Queued,
			"attempt":              0,
			"available_at":         gorm.Expr("clock_timestamp()"),
			"error":                "",
			"waiting_step":         "",
			"waiting_kind":         "",
			"waiting_external_ref": "",
		}).Error; err != nil {
		return err
	}
	if err = appendMessage(
		tx,
		parent,
		"Tool",
		Message{Role: "tool", Content: result, ToolCallID: child.ParentStepID},
	); err != nil {
		return err
	}
	if err = emit(ctx, tx, child.ID, "ChildRunCompleted", map[string]any{
		"parentRunID":  child.ParentRunID,
		"parentStepID": child.ParentStepID,
		"status":       child.Status,
	}); err != nil {
		return err
	}
	return emit(ctx, tx, parent.ID, "RunQueued", map[string]any{"status": Queued})
}

func cascadeCancelChildren(ctx context.Context, tx *gorm.DB, parent *table.Run) error {
	active := []Status{Queued, Running, WaitingClarification, NeedsReview, WaitingExternal, Blocked}
	var children []table.Run
	if err := table.TenantQuery(ctx, tx).
		Where("tenant_id = ? AND parent_run_id = ? AND status IN ?", parent.TenantID, parent.ID, active).
		Order("id ASC").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&children).Error; err != nil {
		return err
	}
	const reason = "parent run terminated"
	for _, child := range children {
		tag := table.TenantQuery(ctx, tx.Model(&table.Run{})).
			Where("id = ? AND status IN ?", child.ID, active).
			Updates(map[string]any{
				"status":               Canceled,
				"epoch":                gorm.Expr("epoch + 1"),
				"lease_until":          nil,
				"worker_id":            "",
				"error":                reason,
				"waiting_step":         "",
				"waiting_kind":         "",
				"waiting_external_ref": "",
			})
		if tag.Error != nil {
			return tag.Error
		}
		if tag.RowsAffected != 1 {
			continue
		}
		if err := emit(
			ctx,
			tx,
			child.ID,
			"RunCanceled",
			map[string]any{"status": Canceled, "reason": reason},
		); err != nil {
			return err
		}
		child.Status = Canceled
		child.Error = reason
		if err := handleChildTerminal(ctx, tx, &child); err != nil {
			return err
		}
	}
	return nil
}

package durable

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// SupplyResult resumes a paused step. A review result must come from a
// human-confirmed external outcome. The waiting metadata recorded at the pause
// is validated here and cleared only after the answer is committed.
func (s *Store) SupplyResult(ctx context.Context, owner, id, step, result string, kind InteractionKind) error {
	expected, messageKind := Status(""), ""
	switch kind {
	case InteractionClarification:
		expected, messageKind = WaitingClarification, "ClarificationAnswer"
	case InteractionReview:
		expected, messageKind = NeedsReview, "ReviewResult"
	default:
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readRun(
		table.TenantQuery(ctx, tx.Model(&table.Run{})).
			Where("id = ? AND owner_id = ?", id, owner).
			Clauses(clause.Locking{Strength: "UPDATE"}),
	)
	if err != nil {
		return err
	}
	var saved struct{ Result, Status string }
	err = tx.Model(&table.Step{}).
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
	if saved.Status != "Started" || r.Status != expected || r.WaitingStep != step || r.WaitingKind != kind {
		return ErrConflict
	}
	var expired bool
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", id).
		Scan(&expired).
		Error; err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if err = tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ?", id, step).
		Updates(map[string]any{"status": "Completed", "result": result}).
		Error; err != nil {
		return err
	}
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":               Queued,
			"attempt":              0,
			"available_at":         gorm.Expr("clock_timestamp()"),
			"error":                "",
			"worker_id":            "",
			"waiting_step":         "",
			"waiting_kind":         "",
			"waiting_external_ref": "",
		}).
		Error; err != nil {
		return err
	}
	if err = appendMessage(
		tx,
		r,
		messageKind,
		Message{Role: "tool", Content: result, ToolCallID: step},
	); err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunResumed", map[string]any{"stepID": step, "kind": kind}); err != nil {
		return err
	}
	return tx.Commit().Error
}

// SupplyExternalResult delivers the outcome of an external system callback
// (CI result, approval flow, etc.) and requeues the run. The run must be in
// WaitingExternal with a matching external reference; otherwise the call is
// rejected with ErrConflict.
func (s *Store) SupplyExternalResult(ctx context.Context, owner, id, step, externalRef, result string) error {
	return s.supplyExternalResult(ctx, &owner, id, step, externalRef, result)
}

// SupplyExternalResultByRunID delivers an authenticated external callback
// result using the run's persisted owner. Callers must not derive the owner
// from transport identity; the HTTP layer is responsible for authenticating
// the internal service before reaching this method.
func (s *Store) SupplyExternalResultByRunID(ctx context.Context, id, step, externalRef, result string) error {
	return s.supplyExternalResult(ctx, nil, id, step, externalRef, result)
}

func (s *Store) supplyExternalResult(
	ctx context.Context,
	owner *string,
	id, step, externalRef, result string,
) error {
	if strings.HasPrefix(externalRef, childExternalRefPrefix) {
		return ErrConflict
	}
	if externalRef == "" {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	query := table.TenantQuery(ctx, tx.Model(&table.Run{})).Where("id = ?", id)
	if owner != nil {
		query = query.Where("owner_id = ?", *owner)
	}
	r, err := readRun(query.Clauses(clause.Locking{Strength: "UPDATE"}))
	if err != nil {
		return err
	}
	if strings.HasPrefix(r.WaitingExternalRef, childExternalRefPrefix) {
		return ErrConflict
	}
	var saved struct{ Result, Status, ExternalRef string }
	err = tx.Model(&table.Step{}).
		Select("result", "status", "external_ref").
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
		if saved.ExternalRef == externalRef && saved.Result == result {
			return nil
		}
		return ErrConflict
	}
	if saved.Status != "Started" || r.Status != WaitingExternal || r.WaitingStep != step ||
		r.WaitingExternalRef != externalRef {
		return ErrConflict
	}
	var expired bool
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Select("deadline <= clock_timestamp()").
		Where("id = ?", id).
		Scan(&expired).
		Error; err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if err = tx.Model(&table.Step{}).
		Where("run_id = ? AND step_id = ?", id, step).
		Updates(map[string]any{"status": "Completed", "result": result}).
		Error; err != nil {
		return err
	}
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":               Queued,
			"attempt":              0,
			"available_at":         gorm.Expr("clock_timestamp()"),
			"error":                "",
			"waiting_step":         "",
			"waiting_kind":         "",
			"waiting_external_ref": "",
		}).
		Error; err != nil {
		return err
	}
	if err = appendMessage(
		tx,
		r,
		"Tool",
		Message{Role: "tool", Content: result, ToolCallID: step},
	); err != nil {
		return err
	}
	if err = emit(ctx, tx, id, "RunResumed", map[string]any{"stepID": step, "kind": "External"}); err != nil {
		return err
	}
	return tx.Commit().Error
}

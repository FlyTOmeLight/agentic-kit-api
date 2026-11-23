package durable

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

type ContextProjection struct {
	Summary      string
	FirstKeptSeq int64
	Messages     []SessionMessage
}

func (s *Store) CompactionContext(ctx context.Context, l Lease) (ContextProjection, error) {
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return ContextProjection{}, err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return ContextProjection{}, err
	}
	var rows []table.Message
	if err = tx.Where("run_id = ? AND session_id = ? AND seq >= ?", run.ID, run.SessionID, run.FirstKeptSeq).
		Order("seq").Find(&rows).Error; err != nil {
		return ContextProjection{}, err
	}
	projection := ContextProjection{Summary: run.CompactedSummary, FirstKeptSeq: run.FirstKeptSeq}
	for _, row := range rows {
		projection.Messages = append(projection.Messages, SessionMessage{
			SessionID: row.SessionID,
			Seq:       row.Seq,
			RunID:     row.RunID,
			Kind:      row.Kind,
			Message:   row.Message,
			CreatedAt: row.CreatedAt,
		})
	}
	return projection, tx.Commit().Error
}

// CompactRun persists a structured summary and advances the projection
// boundary. Durable messages and events are never deleted; the summary only
// changes what the next model input includes.
func (s *Store) CompactRun(ctx context.Context, l Lease, summary string, firstKeptSeq int64) error {
	if strings.TrimSpace(summary) == "" || firstKeptSeq <= 0 {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := lockLease(ctx, table.TenantQuery(ctx, tx), l)
	if err != nil {
		return err
	}
	if firstKeptSeq == run.FirstKeptSeq && summary == run.CompactedSummary {
		return tx.Commit().Error
	}
	if firstKeptSeq < run.FirstKeptSeq {
		return ErrConflict
	}
	if firstKeptSeq > run.FirstKeptSeq {
		if err = validateCompactionBoundary(tx, run, firstKeptSeq); err != nil {
			return err
		}
	}
	if err = table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", l.RunID).
		Updates(map[string]any{
			"compacted_summary": summary,
			"first_kept_seq":    firstKeptSeq,
		}).
		Error; err != nil {
		return err
	}
	if err = emit(ctx, tx, l.RunID, "RunCompacted", map[string]any{
		"firstKeptSeq":  firstKeptSeq,
		"summaryLength": len(summary),
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

func validateCompactionBoundary(tx *gorm.DB, run *Run, firstKeptSeq int64) error {
	var rows []table.Message
	if err := tx.Where("run_id = ? AND session_id = ? AND seq >= ?", run.ID, run.SessionID, run.FirstKeptSeq).
		Order("seq").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 || firstKeptSeq > rows[len(rows)-1].Seq+1 {
		return ErrConflict
	}
	pending := map[string]bool{}
	boundaryFound := false
	for _, row := range rows {
		if row.Seq == firstKeptSeq {
			boundaryFound = row.Message.Role == "assistant"
			break
		}
		if row.Seq > firstKeptSeq {
			return ErrConflict
		}
		switch row.Message.Role {
		case "assistant":
			if len(pending) != 0 {
				return ErrConflict
			}
			for _, call := range row.Message.ToolCalls {
				if call.ID == "" || pending[call.ID] {
					return ErrConflict
				}
				pending[call.ID] = true
			}
		case "tool":
			if !pending[row.Message.ToolCallID] {
				return ErrConflict
			}
			delete(pending, row.Message.ToolCallID)
		}
	}
	if len(pending) != 0 || (!boundaryFound && firstKeptSeq != rows[len(rows)-1].Seq+1) {
		return ErrConflict
	}
	return nil
}

// RecordCompactionFailure keeps a failed summarization observable without
// blocking execution; the worker retries on a later turn.
func (s *Store) RecordCompactionFailure(ctx context.Context, l Lease, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := lockLease(ctx, table.TenantQuery(ctx, tx), l); err != nil {
		return err
	}
	if err := emit(ctx, tx, l.RunID, "CompactionFailed", map[string]any{
		"reason": reason,
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

// RecordModelOverflow saves the recovery checkpoint and its rejection event
// together while the worker still owns the run.
func (s *Store) RecordModelOverflow(
	ctx context.Context,
	l Lease,
	attempt int,
	detail string,
	state json.RawMessage,
) error {
	if attempt < 1 || strings.TrimSpace(detail) == "" || !json.Valid(state) {
		return ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := lockLease(ctx, table.TenantQuery(ctx, tx), l); err != nil {
		return err
	}
	if err := table.TenantQuery(ctx, tx.Model(&table.Run{})).
		Where("id = ?", l.RunID).
		Updates(map[string]any{"state": string(state), "updated_at": gorm.Expr("clock_timestamp()")}).
		Error; err != nil {
		return err
	}
	if err := emit(ctx, tx, l.RunID, "ModelOverflowRejected", map[string]any{
		"attempt": attempt,
		"detail":  detail,
	}); err != nil {
		return err
	}
	return tx.Commit().Error
}

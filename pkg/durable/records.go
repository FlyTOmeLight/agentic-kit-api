package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func Hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func recordError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}

func (s *Store) Get(ctx context.Context, owner, id string) (*Run, error) {
	return readRun(table.TenantQuery(ctx, s.db).Model(&table.Run{}).Where("id = ? AND owner_id = ?", id, owner))
}

// RunFromRow exposes the row mapping for replay prechecks in the execution
// application service.
func RunFromRow(r table.Run) *Run {
	return runFromRow(r)
}

func runFromRow(r table.Run) *Run {
	return &Run{
		Scope:                   r.Spec.Scope,
		AgentID:                 r.Spec.AgentID,
		ConfigVersion:           r.Spec.ConfigVersion,
		ID:                      r.ID,
		OwnerID:                 r.OwnerID,
		SessionID:               r.SessionID,
		Spec:                    Spec(r.Spec),
		ContextSnapshot:         execution.ContextSnapshot(r.ContextSnapshot),
		State:                   r.State,
		Status:                  r.Status,
		WorkerID:                r.WorkerID,
		Epoch:                   r.Epoch,
		LeaseUntil:              r.LeaseUntil,
		EvaluationDeadline:      r.EvaluationDeadline,
		EvaluationWorkerID:      r.EvaluationWorkerID,
		EvaluationClaimWorkerID: r.EvaluationClaimWorkerID,
		EvaluationEpoch:         r.EvaluationEpoch,
		EvaluationLeaseUntil:    r.EvaluationLeaseUntil,
		Deadline:                r.Deadline,
		Attempt:                 r.Attempt,
		MaxAttempts:             r.MaxAttempts,
		EventSeq:                r.EventSeq,
		Output:                  r.Output,
		Error:                   r.Error,
		WaitingStep:             r.WaitingStep,
		WaitingKind:             r.WaitingKind,
		WaitingExternalRef:      r.WaitingExternalRef,
		GoalID:                  r.GoalID,
		ParentRunID:             r.ParentRunID,
		ParentStepID:            r.ParentStepID,
		PendingSteer:            r.PendingSteer,
		CompactedSummary:        r.CompactedSummary,
		FirstKeptSeq:            r.FirstKeptSeq,
		StallMark:               r.StallMark,
		TokensUsed:              r.TokensUsed,
		CostUSD:                 r.CostUSD,
	}
}

func eventFromRow(e table.Event) Event {
	return Event{RunID: e.RunID, Seq: e.Seq, Kind: e.Kind, Payload: e.Payload, CreatedAt: e.CreatedAt.UTC()}
}

func readRun(query *gorm.DB) (*Run, error) {
	var r table.Run
	if err := query.Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return runFromRow(r), nil
}

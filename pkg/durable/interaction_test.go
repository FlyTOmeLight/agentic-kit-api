package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func pauseRun(t *testing.T, s *Store, kind InteractionKind, step string) *Run {
	t.Helper()
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "a")
	if _, err := s.BeginStep(ctx, r.Lease(), StepIntent{ID: step, Input: "q", Policy: ReadOnly}); err != nil {
		t.Fatal(err)
	}
	boundaryKind := BoundaryAwaitReview
	if kind == InteractionClarification {
		boundaryKind = BoundaryAwaitClarification
	}
	if err := s.CommitBoundary(ctx, r.Lease(), Boundary{
		Kind:        boundaryKind,
		State:       []byte(`{"checkpoint":"opaque"}`),
		Detail:      "waiting",
		WaitingStep: step,
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWaitingMetadataLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := pauseRun(t, s, InteractionClarification, "ask")
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WaitingStep != "ask" || got.WaitingKind != InteractionClarification ||
		got.Status != WaitingClarification {
		t.Fatalf("waiting metadata: %+v", got)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "ask", "answer", InteractionReview); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong kind: %v", err)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "other", "answer", InteractionClarification); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("wrong step: %v", err)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "ask", "answer", InteractionClarification); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Queued || got.WaitingStep != "" || got.WaitingKind != "" || got.Attempt != 0 {
		t.Fatalf("after resume: %+v", got)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "ask", "answer", InteractionClarification); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if err := s.SupplyResult(ctx, "owner", r.ID, "ask", "different", InteractionClarification); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("different result: %v", err)
	}
	messages, err := s.Messages(ctx, "owner", got.SessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	answers := 0
	for _, m := range messages {
		if m.Kind == "ClarificationAnswer" {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("answers=%d want 1", answers)
	}
}

func TestWaitingMetadataRejectsExpiredDeadline(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := pauseRun(t, s, InteractionReview, "unsafe")
	if err := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id = ?", r.ID).
		Update("deadline", gorm.Expr("clock_timestamp()-interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SupplyResult(
		ctx,
		"owner",
		r.ID,
		"unsafe",
		"receipt",
		InteractionReview,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("expired deadline: %v", err)
	}
}

func TestCancelAndClaimExpiryClearWaitingMetadata(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := pauseRun(t, s, InteractionClarification, "ask")
	if err := s.Cancel(ctx, "owner", r.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Canceled || got.WaitingStep != "" || got.WaitingKind != "" {
		t.Fatalf("cancel: %+v", got)
	}

	expired := pauseRun(t, s, InteractionClarification, "ask")
	if err = s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id = ?", expired.ID).
		Update("deadline", gorm.Expr("clock_timestamp()-interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, "b", time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatalf("expired waiting run: %v", err)
	}
	got, err = s.Get(ctx, "owner", expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Failed || got.WaitingStep != "" || got.WaitingKind != "" {
		t.Fatalf("claim expiry: %+v", got)
	}
}

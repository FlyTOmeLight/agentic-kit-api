package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func completedRunForGoal(t *testing.T, s *Store, goalID string) *Run {
	t.Helper()
	ctx := context.Background()
	r := createRun(t, s)
	if err := s.BindGoalToRun(ctx, "owner", goalID, r.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, s, "worker")
	if claimed.ID != r.ID {
		t.Fatalf("claimed run %s, want %s", claimed.ID, r.ID)
	}
	if err := s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "work done",
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEvaluationGatesGoalCompletion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "eval-gate-key", "ship it")
	goal.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "go test ./..."}
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	r := completedRunForGoal(t, s, g.ID)

	// A completed run without an acceptance receipt is not completion.
	updated, err := s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != GoalActive || updated.StallCount != 0 {
		t.Fatalf(
			"missing receipt must stay Active without stall, got %s/%d",
			updated.Status,
			updated.StallCount,
		)
	}

	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r.ID, EvaluationFailed, "tests failed"); err != nil {
		t.Fatal(err)
	}
	updated, err = s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != GoalActive || updated.StallCount != 1 {
		t.Fatalf(
			"failed evaluation must stay Active with one stall, got %s/%d",
			updated.Status,
			updated.StallCount,
		)
	}

	// Same run, same verdict: idempotent. Different verdict: conflict.
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r.ID, EvaluationFailed, "tests failed"); err != nil {
		t.Fatalf("expected idempotent replay, got %v", err)
	}
	if _, err = s.RecordEvaluation(
		ctx,
		"owner",
		g.ID,
		r.ID,
		EvaluationPassed,
		"tests passed",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("expected ErrConflict for changed verdict, got %v", err)
	}

	r2 := completedRunForGoal(t, s, g.ID)
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r2.ID, EvaluationPassed, "tests passed"); err != nil {
		t.Fatal(err)
	}
	updated, err = s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != GoalComplete {
		t.Fatalf("expected Complete after passing acceptance, got %s", updated.Status)
	}
}

func TestEvaluationErrorIsNotAStall(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "eval-error-key", "ship it")
	goal.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"}
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	r := completedRunForGoal(t, s, g.ID)
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r.ID, EvaluationError, "sandbox unavailable"); err != nil {
		t.Fatal(err)
	}
	updated, err := s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != GoalActive || updated.StallCount != 0 {
		t.Fatalf("infrastructure error must not charge a stall, got %s/%d", updated.Status, updated.StallCount)
	}
}

func TestEvaluationRequiresBoundRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goalA, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, "eval-bound-a", "a"))
	if err != nil {
		t.Fatal(err)
	}
	goalB, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, "eval-bound-b", "b"))
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.BindGoalToRun(ctx, "owner", goalA.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordEvaluation(
		ctx,
		"owner",
		goalB.ID,
		r.ID,
		EvaluationPassed,
		"ok",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("expected ErrConflict for unbound goal, got %v", err)
	}
	if _, err = s.RecordEvaluation(ctx, "owner", goalA.ID, r.ID, "Bogus", "ok"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unknown status, got %v", err)
	}
}

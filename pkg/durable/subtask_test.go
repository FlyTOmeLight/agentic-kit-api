package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// spawnWaitingParent creates a parent run, begins a spawn step, spawns the
// child and suspends the parent in WaitingExternal.
func spawnWaitingParent(t *testing.T, s *Store, ctx context.Context, query string) (*Run, *Run) {
	t.Helper()
	parent := createRun(t, s)
	claimed := claim(t, s, "worker")
	if claimed.ID != parent.ID {
		t.Fatalf("claimed run %s, want parent %s", claimed.ID, parent.ID)
	}
	intent := StepIntent{ID: "spawn-1", Input: query, Policy: Idempotent}
	if _, err := s.BeginStep(ctx, claimed.Lease(), intent); err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChildRun(ctx, claimed.Lease(), intent.ID, query, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID != parent.ID || child.ParentStepID != intent.ID {
		t.Fatalf("child parent refs = %s/%s, want %s/%s",
			child.ParentRunID, child.ParentStepID, parent.ID, intent.ID)
	}
	if child.Status != Queued {
		t.Fatalf("child status = %s, want Queued", child.Status)
	}
	if err = s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:        BoundaryAwaitExternal,
		State:       json.RawMessage(`{}`),
		Detail:      "awaiting subtask",
		WaitingStep: intent.ID,
		ExternalRef: childExternalRefPrefix + child.ID,
	}); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.Get(ctx, "owner", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != WaitingExternal {
		t.Fatalf("parent status = %s, want WaitingExternal", waiting.Status)
	}
	return parent, child
}

func TestSubtaskCompletionWakesParent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parent, child := spawnWaitingParent(t, s, ctx, "child question")

	claimed := claim(t, s, "worker")
	if claimed.ID != child.ID {
		t.Fatalf("claimed run %s, want child %s", claimed.ID, child.ID)
	}
	if err := s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "child answer",
	}); err != nil {
		t.Fatal(err)
	}

	woke, err := s.Get(ctx, "owner", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if woke.Status != Queued {
		t.Fatalf("parent status = %s, want Queued after child completion", woke.Status)
	}
	var step table.Step
	if err = s.DB().WithContext(ctx).
		Where("run_id = ? AND step_id = ?", parent.ID, "spawn-1").
		Take(&step).Error; err != nil {
		t.Fatal(err)
	}
	if step.Status != "Completed" || step.Result != "child answer" {
		t.Fatalf("parent step = %s %q, want Completed child answer", step.Status, step.Result)
	}

	reclaimed := claim(t, s, "worker")
	if reclaimed.ID != parent.ID {
		t.Fatalf("claimed run %s, want parent %s", reclaimed.ID, parent.ID)
	}
	decision, err := s.BeginStep(ctx, reclaimed.Lease(), StepIntent{
		ID:     "spawn-1",
		Input:  "child question",
		Policy: Idempotent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != StepReplay || decision.Result != "child answer" {
		t.Fatalf("expected StepReplay with child result, got %s %q", decision.Outcome, decision.Result)
	}
}

func TestSubtaskOrphanedWhenParentCanceled(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parent, child := spawnWaitingParent(t, s, ctx, "child question")

	if err := s.Cancel(ctx, "owner", parent.ID); err != nil {
		t.Fatal(err)
	}
	// The active child is cascade-canceled with the parent.
	canceledChild, err := s.Get(ctx, "owner", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceledChild.Status != Canceled {
		t.Fatalf("child status = %s, want Canceled after parent cancel", canceledChild.Status)
	}
	var step table.Step
	if err = s.DB().WithContext(ctx).
		Where("run_id = ? AND step_id = ?", parent.ID, "spawn-1").
		Take(&step).Error; err != nil {
		t.Fatal(err)
	}
	if step.Status != "Started" {
		t.Fatalf("parent step = %s, want untouched Started", step.Status)
	}
	events, err := s.Events(ctx, "owner", child.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "ChildOrphaned" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ChildOrphaned event on canceled child")
	}
}

func TestSubtaskBudgetChargesGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal, _, err := s.CreateGoal(ctx, Goal{
		OwnerID:     "owner",
		SessionID:   session.ID,
		Objective:   "obj",
		RequestKey:  "goal-key",
		RequestHash: "goal-hash",
		Evaluator:   EvaluatorSpec{Kind: EvaluatorNone},
		Budget:      BudgetSpec{MaxTokens: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, child := spawnWaitingParent(t, s, ctx, "child question")
	if err = s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id IN ?", []string{parent.ID, child.ID}).
		Update("goal_id", goal.ID).Error; err != nil {
		t.Fatal(err)
	}

	claimed := claim(t, s, "worker")
	if claimed.ID != child.ID {
		t.Fatalf("claimed run %s, want child %s", claimed.ID, child.ID)
	}
	if err = s.RecordUsageWithCost(
		ctx,
		claimed.Lease(),
		1,
		"child-model",
		42,
		0.5,
		json.RawMessage(`{}`),
	); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "done",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetGoal(ctx, "owner", goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokensUsed != 42 || got.CostUSD != 0.5 {
		t.Fatalf("goal usage = %d/%v, want 42/0.5", got.TokensUsed, got.CostUSD)
	}
	if got.Status != GoalBlocked {
		t.Fatalf("goal status = %s, want Blocked after token budget exceeded", got.Status)
	}
}

func TestSubtaskTerminalChildResolvesParent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	parent, child := spawnWaitingParent(t, s, ctx, "child question")

	claimed := claim(t, s, "worker")
	if claimed.ID != child.ID {
		t.Fatalf("claimed run %s, want child %s", claimed.ID, child.ID)
	}
	if err := s.Cancel(ctx, "owner", child.ID); err != nil {
		t.Fatal(err)
	}
	woke, err := s.Get(ctx, "owner", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if woke.Status != Queued {
		t.Fatalf("parent status = %s, want Queued after child cancel", woke.Status)
	}
	var step table.Step
	if err = s.DB().WithContext(ctx).
		Where("run_id = ? AND step_id = ?", parent.ID, "spawn-1").
		Take(&step).Error; err != nil {
		t.Fatal(err)
	}
	if step.Status != "Completed" {
		t.Fatalf("parent step = %s, want Completed with failure summary", step.Status)
	}
	if step.Result == "" || step.Result == "child question" {
		t.Fatalf("parent step result = %q, want child failure summary", step.Result)
	}
	events, err := s.Events(ctx, "owner", parent.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	queued := false
	for _, e := range events {
		if e.Kind == "RunQueued" {
			queued = true
		}
	}
	if !queued {
		t.Fatalf("expected RunQueued event on parent")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("unexpected ErrNotFound")
	}
}

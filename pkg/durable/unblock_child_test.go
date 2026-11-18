package durable

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestUnblockChildRequiresGoalResume(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	parent, goal := claimedRunWithGoal(t, store, BudgetSpec{MaxTokens: 100})
	step := StepIntent{ID: "spawn-recovery", Input: "child work", Policy: Idempotent}
	if _, err := store.BeginStep(ctx, parent.Lease(), step); err != nil {
		t.Fatal(err)
	}
	child, err := store.SpawnChildRun(ctx, parent.Lease(), step.ID, step.Input, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CommitBoundary(ctx, parent.Lease(), Boundary{
		Kind: BoundaryAwaitExternal, State: json.RawMessage(`{"version":3}`),
		Detail:      "awaiting child recovery",
		WaitingStep: step.ID, ExternalRef: childExternalRefPrefix + child.ID,
	}); err != nil {
		t.Fatal(err)
	}
	childClaim := claim(t, store, "child-worker")
	if childClaim.ID != child.ID {
		t.Fatalf("claimed %s, want child %s", childClaim.ID, child.ID)
	}
	parkDispatch(t, store, childClaim)
	if err = store.Unblock(ctx, "owner", child.ID, childClaim.Epoch, "accept child unknown consumption"); err != nil {
		t.Fatal(err)
	}
	if status := getGoalStatus(t, store, goal.ID); status != GoalBlocked {
		t.Fatalf("child unblock resumed goal: %s", status)
	}
	childClaim = claim(t, store, "child-replacement")
	if childClaim.ID != child.ID {
		t.Fatalf("claimed %s, want child %s", childClaim.ID, child.ID)
	}
	if err = store.CommitBoundary(ctx, childClaim.Lease(), Boundary{
		Kind: BoundaryComplete, State: childClaim.State, Output: "child result",
	}); err != nil {
		t.Fatal(err)
	}
	woke, err := store.Get(ctx, "owner", parent.ID)
	if err != nil || woke.Status != Queued {
		t.Fatalf("parent=%+v err=%v", woke, err)
	}
	parentClaim := claim(t, store, "parent-replacement")
	if parentClaim.ID != parent.ID {
		t.Fatalf("claimed %s, want parent %s", parentClaim.ID, parent.ID)
	}
	decision, err := store.BeginStep(ctx, parentClaim.Lease(), step)
	if err != nil || decision.Outcome != StepReplay || decision.Result != "child result" {
		t.Fatalf("parent replay=%+v err=%v", decision, err)
	}
	if err = store.CommitBoundary(ctx, parentClaim.Lease(), Boundary{
		Kind: BoundaryComplete, State: parentClaim.State, Output: "parent result",
	}); err != nil {
		t.Fatal(err)
	}
	progress, err := store.CheckGoalProgress(ctx, "owner", goal.ID)
	if err != nil || progress.Status != GoalBlocked {
		t.Fatalf("before explicit resume: goal=%+v err=%v", progress, err)
	}
	if err = store.TransitionGoal(ctx, "owner", goal.ID, GoalActive); err != nil {
		t.Fatal(err)
	}
	progress, err = store.CheckGoalProgress(ctx, "owner", goal.ID)
	if err != nil || progress.Status != GoalComplete {
		t.Fatalf("after explicit resume: goal=%+v err=%v", progress, err)
	}
}

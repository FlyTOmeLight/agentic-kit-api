package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func newTestGoal(sessionID, key, objective string) Goal {
	return Goal{
		OwnerID:     "owner",
		SessionID:   sessionID,
		RequestKey:  key,
		RequestHash: "hash-" + key,
		Objective:   objective,
		Evaluator:   EvaluatorSpec{Kind: EvaluatorNone},
	}
}

func TestCreateGoalIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	g := newTestGoal(session.ID, "goal-key-1", "deploy the service")
	g.Budget = BudgetSpec{MaxWallClockSeconds: 3600}

	created, isNew, err := s.CreateGoal(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || created.Status != GoalActive {
		t.Fatalf("unexpected create result: new=%v status=%s", isNew, created.Status)
	}

	replayed, isNew, err := s.CreateGoal(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if isNew || replayed.ID != created.ID {
		t.Fatalf("expected idempotent replay of %s", created.ID)
	}

	conflicting := g
	conflicting.RequestHash = "different-hash"
	if _, _, err = s.CreateGoal(ctx, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for changed request hash, got %v", err)
	}

	other := newTestGoal(session.ID, "goal-key-2", "second goal")
	if _, _, err = s.CreateGoal(ctx, other); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitRejectsPausedGoalBind(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, cfg := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, NewID(), "deploy the service")
	created, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionGoal(ctx, "owner", created.ID, GoalPaused); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(struct{ Session, Query string }{Session: session.ID, Query: "late request"})
	agentID, version := sessionPublishedFixture(t, s, session.ID)
	cmd := execution.SubmitCommand{
		Owner:       "owner",
		RequestKey:  NewID(),
		RequestHash: Hash(input),
		TTL:         time.Hour,
		MaxAttempts: 3,
		Kind:        execution.SubmitChat,
		SessionID:   session.ID,
		Query:       "late request",
		GoalID:      created.ID,
	}
	if _, _, err = s.Accept(
		ctx,
		cmd,
		PrepareChatSubmission(cfg, agentID, version, nil),
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("expected ErrNotFound for paused goal, got %v", err)
	}
	var goalRow struct{ ActiveRunID string }
	if err = s.DB().Table("durable_goal").Select("active_run_id").
		Where("id = ?", created.ID).Take(&goalRow).Error; err != nil {
		t.Fatal(err)
	}
	if goalRow.ActiveRunID != "" {
		t.Fatalf("paused goal must not gain a run: %s", goalRow.ActiveRunID)
	}
	var orphanCount int64
	if err = s.DB().Table("durable_run").Where("goal_id = ?", created.ID).
		Count(&orphanCount).Error; err != nil {
		t.Fatal(err)
	}
	if orphanCount != 0 {
		t.Fatalf("rejected bind must not leave a run: %d", orphanCount)
	}
}

func TestStallMarkDetectsNoProgress(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "stall-mark-key", "deploy the service")
	goal.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"}
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}

	// First run completes with output "same".
	r1 := completedRunForGoal(t, s, g.ID)
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r1.ID, EvaluationFailed, "not yet"); err != nil {
		t.Fatal(err)
	}
	updated, err := s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StallCount != 1 || updated.LastStallMark == "" {
		t.Fatalf("expected stall=1 with a mark, got %d %q", updated.StallCount, updated.LastStallMark)
	}
	updated, err = s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil || updated.StallCount != 1 {
		t.Fatalf("same run charged twice: goal=%+v err=%v", updated, err)
	}

	// Second run with identical output and steps produces the same mark.
	r2 := completedRunForGoal(t, s, g.ID)
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r2.ID, EvaluationFailed, "not yet"); err != nil {
		t.Fatal(err)
	}
	updated, err = s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StallCount != 2 {
		t.Fatalf("expected stall=2 for identical mark, got %d", updated.StallCount)
	}

	// Third run with different output resets the streak.
	r3 := createRun(t, s)
	if err := s.BindGoalToRun(ctx, "owner", g.ID, r3.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, s, "worker")
	if err := s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "different",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r3.ID, EvaluationFailed, "still not"); err != nil {
		t.Fatal(err)
	}
	updated, err = s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.StallCount != 1 {
		t.Fatalf("expected stall=1 after mark changed, got %d", updated.StallCount)
	}
}

func TestGetGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	g, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, "get-goal-key", "test get"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGoal(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != "test get" {
		t.Fatalf("unexpected objective: %s", got.Objective)
	}
	if _, err = s.GetGoal(ctx, "owner", "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err = s.GetGoal(ctx, "other-owner", g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected owner isolation, got %v", err)
	}
}

func TestListGoals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	for i := 0; i < 3; i++ {
		key := "list-key-" + string(rune('a'+i))
		if _, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, key, "objective")); err != nil {
			t.Fatal(err)
		}
	}
	goals, err := s.ListGoals(ctx, "owner", GoalActive, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 3 {
		t.Fatalf("expected 3 goals, got %d", len(goals))
	}
	goals, err = s.ListGoals(ctx, "owner", GoalComplete, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 0 {
		t.Fatalf("expected 0 complete goals, got %d", len(goals))
	}
}

func TestTransitionGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	g, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, "transition-key", "test transition"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionGoal(ctx, "owner", g.ID, GoalPaused); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGoal(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != GoalPaused {
		t.Fatalf("expected Paused, got %s", got.Status)
	}
	if err = s.TransitionGoal(ctx, "owner", g.ID, GoalActive); err != nil {
		t.Fatalf("expected resume to Active, got %v", err)
	}
	if err = s.TransitionGoal(ctx, "owner", g.ID, GoalPaused); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionGoal(ctx, "owner", g.ID, GoalComplete); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected Paused->Complete to be rejected, got %v", err)
	}
}

func TestBindGoalToRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	g, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, "bind-key", "test bind"))
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.BindGoalToRun(ctx, "owner", g.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGoal(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveRunID != r.ID {
		t.Fatalf("expected active run %s, got %s", r.ID, got.ActiveRunID)
	}
}

func TestCheckGoalProgressCompletesOnSuccess(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	g, _, err := s.CreateGoal(ctx, newTestGoal(session.ID, "progress-complete-key", "complete on success"))
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.BindGoalToRun(ctx, "owner", g.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, s, "worker")
	if claimed.ID != r.ID {
		t.Fatalf("claimed run %s, want %s", claimed.ID, r.ID)
	}
	if err = s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "done",
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != GoalComplete {
		t.Fatalf("expected Complete, got %s", updated.Status)
	}
}

func TestCheckGoalProgressStallBlocks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "progress-stall-key", "stall test")
	goal.Budget = BudgetSpec{MaxWallClockSeconds: 3600}
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r := createRun(t, s)
		if err = s.BindGoalToRun(ctx, "owner", g.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		claimed := claim(t, s, "worker")
		if claimed.ID != r.ID {
			t.Fatalf("claimed run %s, want %s", claimed.ID, r.ID)
		}
		if err = s.CommitBoundary(ctx, claimed.Lease(), Boundary{
			Kind:   BoundaryFail,
			State:  json.RawMessage(`{}`),
			Detail: "no progress",
		}); err != nil {
			t.Fatal(err)
		}
		updated, err := s.CheckGoalProgress(ctx, "owner", g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && updated.Status != GoalActive {
			t.Fatalf("iteration %d: expected Active, got %s", i, updated.Status)
		}
		if i == 2 && updated.Status != GoalBlocked {
			t.Fatalf("iteration %d: expected Blocked, got %s", i, updated.Status)
		}
	}
}

func TestCheckGoalProgressBudgetExceeded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "progress-budget-key", "budget test")
	goal.Budget = BudgetSpec{MaxTokens: 100}
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.BindGoalToRun(ctx, "owner", g.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, s, "worker")
	if claimed.ID != r.ID {
		t.Fatalf("claimed run %s, want %s", claimed.ID, r.ID)
	}
	if err = s.RecordUsage(
		ctx,
		claimed.Lease(),
		1,
		"token-limit-model",
		200,
		json.RawMessage(`{}`),
	); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionGoal(ctx, "owner", g.ID, GoalActive); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryFail,
		State:  json.RawMessage(`{}`),
		Detail: "token limit",
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != GoalBlocked {
		t.Fatalf("expected Blocked, got %s", updated.Status)
	}
	if updated.TokensUsed != 200 {
		t.Fatalf("expected 200 tokens over limit, got %d", updated.TokensUsed)
	}
	if err = s.TransitionGoal(ctx, "owner", g.ID, GoalActive); err != nil {
		t.Fatal(err)
	}
	updated, err = s.CheckGoalProgress(ctx, "owner", g.ID)
	if err != nil || updated.TokensUsed != 200 {
		t.Fatalf("same run charged twice after resume: goal=%+v err=%v", updated, err)
	}
}

func TestSteerGoalAndTakePendingSteer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "steer-goal-key", "fix tests")
	goal.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "go test ./..."}
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}

	// One failed run increments StallCount to 1.
	r1 := completedRunForGoal(t, s, g.ID)
	if _, err = s.RecordEvaluation(ctx, "owner", g.ID, r1.ID, EvaluationFailed, "fail"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CheckGoalProgress(ctx, "owner", g.ID); err != nil {
		t.Fatal(err)
	}

	// Steer without an active running run returns ErrConflict.
	if err = s.SteerGoal(ctx, "owner", g.ID, "focus on pkg/durable"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when no active running run, got %v", err)
	}

	// Bind a second run and claim it so it enters Running.
	r2 := createRun(t, s)
	if err = s.BindGoalToRun(ctx, "owner", g.ID, r2.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SteerGoal(ctx, "owner", g.ID, "still queued"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict while run is Queued, got %v", err)
	}
	claimed := claim(t, s, "worker")
	if claimed.ID != r2.ID {
		t.Fatalf("claimed %s, want %s", claimed.ID, r2.ID)
	}

	if err = s.SteerGoal(ctx, "owner", g.ID, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for blank steer, got %v", err)
	}
	if err = s.SteerGoal(ctx, "owner", g.ID, "focus on pkg/durable"); err != nil {
		t.Fatal(err)
	}
	if err = s.SteerGoal(ctx, "owner", g.ID, "skip mock generation"); err != nil {
		t.Fatal(err)
	}

	// StallCount must be reset to 0 after steering.
	reloaded, err := s.GetGoal(ctx, "owner", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.StallCount != 0 {
		t.Fatalf("expected StallCount reset to 0, got %d", reloaded.StallCount)
	}

	// Worker atomically takes merged steering once; second take returns empty.
	checkpoint := func(steer string) json.RawMessage {
		state, marshalErr := json.Marshal(map[string]any{"steers": []string{steer}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return state
	}
	stale := claimed.Lease()
	stale.Epoch--
	if _, err = s.TakePendingSteer(ctx, stale, checkpoint); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale worker consumed steering: %v", err)
	}
	if _, err = s.TakePendingSteer(ctx, claimed.Lease(), func(string) json.RawMessage {
		return json.RawMessage(`{`)
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid checkpoint consumed steering: %v", err)
	}
	steer, err := s.TakePendingSteer(ctx, claimed.Lease(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	want := "focus on pkg/durable\nskip mock generation"
	if steer != want {
		t.Fatalf("TakePendingSteer = %q, want %q", steer, want)
	}
	saved, err := s.Get(ctx, "owner", claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	var recovered struct {
		Steers []string `json:"steers"`
	}
	if err = json.Unmarshal(saved.State, &recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Steers) != 1 || recovered.Steers[0] != want {
		t.Fatalf("consumed steering missing from durable checkpoint: %+v", recovered)
	}
	again, err := s.TakePendingSteer(ctx, claimed.Lease(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if again != "" {
		t.Fatalf("second TakePendingSteer = %q, want empty", again)
	}
}

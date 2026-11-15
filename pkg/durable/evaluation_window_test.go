package durable

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

func evaluationWindowGoalRun(
	t *testing.T,
	s *Store,
	evaluator EvaluatorSpec,
) (*Goal, *Run) {
	t.Helper()
	ctx := context.Background()
	session, _ := testConversation(t, s, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, "evaluation-window-"+NewID(), "evaluation window")
	goal.Evaluator = evaluator
	g, _, err := s.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.BindGoalToRun(ctx, "owner", g.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	return g, r
}

func activateEvaluationEnvironment(t *testing.T, s *Store, claimed *Run) {
	t.Helper()
	ctx := context.Background()
	const requestKey = "evaluation-acquire"
	if _, err := s.PrepareEnvironment(ctx, claimed.Lease(), requestKey, "coding", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateEnvironment(
		ctx,
		claimed.Lease(),
		requestKey,
		"sandbox-1",
		"sandbox-lease-1",
		1,
	); err != nil {
		t.Fatal(err)
	}
}

func evaluationWindowRow(t *testing.T, s *Store, runID string) table.Run {
	t.Helper()
	var row table.Run
	if err := s.DB().WithContext(context.Background()).
		Select(
			"status",
			"evaluation_deadline",
			"evaluation_worker_id",
			"worker_id",
			"lease_until",
			"epoch",
			"deadline",
		).
		Where("id = ?", runID).
		Take(&row).
		Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func assertNoEvaluationWindow(t *testing.T, s *Store, runID string) {
	t.Helper()
	row := evaluationWindowRow(t, s, runID)
	if row.EvaluationDeadline != nil || row.EvaluationWorkerID != "" {
		t.Fatalf(
			"unexpected evaluation window: deadline=%v worker=%q",
			row.EvaluationDeadline,
			row.EvaluationWorkerID,
		)
	}
}

func completeEvaluationWindowRun(t *testing.T, s *Store, l Lease) {
	t.Helper()
	if err := s.CommitBoundary(context.Background(), l, Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "done",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommitBoundaryCompleteStartsEvaluationWindow(t *testing.T) {
	t.Run("caps deadline at database clock plus ten minutes", func(t *testing.T) {
		s := testStore(t)
		ctx := context.Background()
		_, r := evaluationWindowGoalRun(
			t,
			s,
			EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
		)
		claimed := claim(t, s, "worker")
		activateEvaluationEnvironment(t, s, claimed)
		completeEvaluationWindowRun(t, s, claimed.Lease())

		row := evaluationWindowRow(t, s, r.ID)
		if row.Status != Completed {
			t.Fatalf("status=%s want %s", row.Status, Completed)
		}
		if row.EvaluationDeadline == nil {
			t.Fatal("evaluation deadline not set")
		}
		if row.EvaluationWorkerID != claimed.WorkerID {
			t.Fatalf("evaluation worker=%q want %q", row.EvaluationWorkerID, claimed.WorkerID)
		}
		if row.WorkerID != "" || row.LeaseUntil != nil {
			t.Fatalf("ordinary worker lease not cleared: worker=%q lease=%v", row.WorkerID, row.LeaseUntil)
		}
		if row.Epoch != claimed.Epoch {
			t.Fatalf("epoch=%d want %d", row.Epoch, claimed.Epoch)
		}

		var bounded int64
		if err := s.DB().WithContext(ctx).Raw(`
			SELECT COUNT(*)
			FROM durable_run
			WHERE id = ?
			  AND evaluation_deadline BETWEEN
			      clock_timestamp() + interval '9 minutes 50 seconds'
			      AND clock_timestamp() + interval '10 minutes 10 seconds'
		`, r.ID).Scan(&bounded).Error; err != nil {
			t.Fatal(err)
		}
		if bounded != 1 {
			t.Fatalf("evaluation deadline is not bounded by the database clock: %v", row.EvaluationDeadline)
		}
	})

	t.Run("keeps earlier run deadline", func(t *testing.T) {
		s := testStore(t)
		ctx := context.Background()
		_, r := evaluationWindowGoalRun(
			t,
			s,
			EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
		)
		if err := s.DB().WithContext(ctx).Exec(`
			UPDATE durable_run
			SET deadline = clock_timestamp() + interval '4 minutes'
			WHERE id = ?
		`, r.ID).Error; err != nil {
			t.Fatal(err)
		}
		claimed := claim(t, s, "worker")
		activateEvaluationEnvironment(t, s, claimed)
		completeEvaluationWindowRun(t, s, claimed.Lease())

		row := evaluationWindowRow(t, s, r.ID)
		if row.EvaluationDeadline == nil {
			t.Fatal("evaluation deadline not set")
		}
		var exact int64
		if err := s.DB().WithContext(ctx).Raw(`
			SELECT COUNT(*)
			FROM durable_run
			WHERE id = ? AND evaluation_deadline = deadline
		`, r.ID).Scan(&exact).Error; err != nil {
			t.Fatal(err)
		}
		if exact != 1 {
			t.Fatalf(
				"evaluation deadline=%v must equal earlier run deadline=%v",
				row.EvaluationDeadline,
				row.Deadline,
			)
		}
	})
}

func TestCommitBoundaryCompleteSkipsEvaluationWindow(t *testing.T) {
	tests := []struct {
		name       string
		complete   func(t *testing.T, s *Store) string
		wantStatus Status
	}{
		{
			name: "no goal",
			complete: func(t *testing.T, s *Store) string {
				r := createRun(t, s)
				claimed := claim(t, s, "worker")
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "paused goal",
			complete: func(t *testing.T, s *Store) string {
				g, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
				)
				if err := s.TransitionGoal(
					context.Background(),
					"owner",
					g.ID,
					GoalPaused,
				); err != nil {
					t.Fatal(err)
				}
				claimed := claim(t, s, "worker")
				activateEvaluationEnvironment(t, s, claimed)
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "goal points at another active run",
			complete: func(t *testing.T, s *Store) string {
				g, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
				)
				claimed := claim(t, s, "worker")
				activateEvaluationEnvironment(t, s, claimed)
				other := createRun(t, s)
				if err := s.BindGoalToRun(context.Background(), "owner", g.ID, other.ID); err != nil {
					t.Fatal(err)
				}
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "no environment",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
				)
				claimed := claim(t, s, "worker")
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "preparing environment",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
				)
				claimed := claim(t, s, "worker")
				if _, err := s.PrepareEnvironment(
					context.Background(),
					claimed.Lease(),
					"evaluation-acquire",
					"coding",
					1,
				); err != nil {
					t.Fatal(err)
				}
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "environment from another epoch",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
				)
				first := claim(t, s, "worker-a")
				activateEvaluationEnvironment(t, s, first)
				if err := s.DB().WithContext(context.Background()).Exec(`
					UPDATE durable_run
					SET lease_until = clock_timestamp() - interval '1 second'
					WHERE id = ?
				`, first.ID).Error; err != nil {
					t.Fatal(err)
				}
				second := claim(t, s, "worker-b")
				if second.ID != first.ID || second.Epoch <= first.Epoch {
					t.Fatalf(
						"expected a new epoch for the same run, got %s/%d",
						second.ID,
						second.Epoch,
					)
				}
				completeEvaluationWindowRun(t, s, second.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "failed run",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
				)
				claimed := claim(t, s, "worker")
				activateEvaluationEnvironment(t, s, claimed)
				if err := s.CommitBoundary(context.Background(), claimed.Lease(), Boundary{
					Kind:   BoundaryFail,
					State:  json.RawMessage(`{}`),
					Detail: "failed",
				}); err != nil {
					t.Fatal(err)
				}
				return r.ID
			},
			wantStatus: Failed,
		},
		{
			name: "non-command evaluator",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(t, s, EvaluatorSpec{Kind: EvaluatorNone})
				claimed := claim(t, s, "worker")
				activateEvaluationEnvironment(t, s, claimed)
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "http evaluator",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(
					t,
					s,
					EvaluatorSpec{Kind: EvaluatorHTTP, Config: "https://example.test/evaluate"},
				)
				claimed := claim(t, s, "worker")
				activateEvaluationEnvironment(t, s, claimed)
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
		{
			name: "command evaluator with empty config",
			complete: func(t *testing.T, s *Store) string {
				_, r := evaluationWindowGoalRun(t, s, EvaluatorSpec{Kind: EvaluatorCommand})
				claimed := claim(t, s, "worker")
				activateEvaluationEnvironment(t, s, claimed)
				completeEvaluationWindowRun(t, s, claimed.Lease())
				return r.ID
			},
			wantStatus: Completed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			runID := tc.complete(t, s)
			assertNoEvaluationWindow(t, s, runID)
			row := evaluationWindowRow(t, s, runID)
			if row.Status != tc.wantStatus {
				t.Fatalf("status=%s want %s", row.Status, tc.wantStatus)
			}
		})
	}
}

func TestCommitBoundaryEvaluationWindowRollsBackWithTransaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, r := evaluationWindowGoalRun(
		t,
		s,
		EvaluatorSpec{Kind: EvaluatorCommand, Config: "true"},
	)
	claimed := claim(t, s, "worker")
	activateEvaluationEnvironment(t, s, claimed)
	if err := s.DB().WithContext(ctx).Exec(`
		CREATE FUNCTION reject_evaluation_window_event() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected evaluation window failure';
		END;
		$$;
		CREATE TRIGGER reject_evaluation_window_event
		BEFORE INSERT ON durable_event
		FOR EACH ROW EXECUTE FUNCTION reject_evaluation_window_event();
	`).Error; err != nil {
		t.Fatal(err)
	}
	err := s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "must roll back",
	})
	if err == nil {
		t.Fatal("expected transaction failure")
	}

	assertNoEvaluationWindow(t, s, r.ID)
	row := evaluationWindowRow(t, s, r.ID)
	if row.Status != Running || row.WorkerID != claimed.WorkerID || row.LeaseUntil == nil {
		t.Fatalf("failed complete partially committed: %+v", row)
	}
}

package durable

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func evaluationRequest(t *testing.T, s *Store) sdk.StartEvaluationRequest {
	t.Helper()
	session := testConversation(t, s, Spec{SystemPrompt: "original prompt"})
	return sdk.StartEvaluationRequest{
		Scope: sdk.EvaluationScope{TenantID: "tenant", ProjectID: "project", ActorID: "actor"},
		Suite: sdk.EvaluationSuite{
			SchemaVersion: 1,
			Name:          "writing",
			Version:       "1",
			Target: sdk.EvaluationTarget{
				Kind:         "Agentic",
				AgentID:      session.AgentID,
				AgentVersion: 1,
				Capabilities: []string{"output"},
			},
			Scenario: sdk.EvaluationScenario{Kind: "single_turn", TimeoutSeconds: 60},
			Dataset: sdk.EvaluationDataset{
				Name:    "regression",
				Version: "1",
				Cases:   []sdk.EvaluationCase{{ID: "a", Input: "first"}, {ID: "b", Input: "second"}},
			},
			Evaluators: []sdk.EvaluatorSpec{
				{
					ID:       "json",
					Version:  "1",
					Kind:     "json_valid",
					Scope:    "output",
					Requires: []string{"output"},
				},
			},
			Metrics: []sdk.EvaluationMetric{
				{
					ID:              "valid",
					Evaluator:       "json",
					Aggregate:       "pass_rate",
					MinimumScore:    1,
					MinimumCoverage: 1,
				},
			},
		},
	}
}

func TestEvaluationConcurrentSubmissionAndFrozenRuns(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	req := evaluationRequest(t, s)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ids []string
	createdCount := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, created, err := s.StartEvaluation(ctx, "owner", "stable", req)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids = append(ids, out.ID)
			if created {
				createdCount++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 6 || createdCount != 1 {
		t.Fatalf("ids=%v created=%d", ids, createdCount)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("created multiple experiments")
		}
	}
	out, err := s.Evaluation(ctx, "owner", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Trials) != 2 || out.Report.Passed || out.Trials[0].Run.SessionID == out.Trials[1].Run.SessionID {
		t.Fatalf("invalid initial experiment: %+v", out)
	}
	a, err := s.Agent(ctx, "owner", req.Suite.Target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	a.Draft.SystemPrompt = "edited candidate"
	a, err = s.UpdateAgent(ctx, "owner", a.ID, a.Name, a.Revision, a.Draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(ctx, "owner", a.ID, a.Revision); err != nil {
		t.Fatal(err)
	}
	for _, trial := range out.Trials {
		run, err := s.Get(ctx, "owner", trial.RunID)
		if err != nil || run.Spec.SystemPrompt != "original prompt" || run.ConfigVersion != "1" ||
			len(run.Spec.History) != 0 {
			t.Fatalf("snapshot changed %+v %v", run, err)
		}
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Reopen the actual database connection, then retry an already accepted request.
	reopened, err := Open(ctx, s.db.Config.Dialector.(*postgres.Dialector).DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, created, err := reopened.StartEvaluation(ctx, "owner", "stable", req)
	if err != nil || created || replay.ID != out.ID || replay.Trials[0].RunID != out.Trials[0].RunID {
		t.Fatalf("replay changed execution: %+v %v", replay, err)
	}
	req.Suite.Metrics[0].MinimumScore = 0.5
	if _, _, err = s.StartEvaluation(ctx, "owner", "stable", req); !errors.Is(err, ErrConflict) {
		t.Fatal("changed request accepted", err)
	}
	if _, err = s.Evaluation(ctx, "other", out.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-owner read", err)
	}
	rows, err := s.Evaluations(
		ctx,
		"owner",
		sdk.ListEvaluationsOptions{
			PageOptions: sdk.PageOptions{Limit: 100},
			TenantID:    "tenant",
			ProjectID:   "other",
		},
	)
	if err != nil || len(rows) != 0 {
		t.Fatal("cross-project listing", err)
	}
}

func TestEvaluationSubmissionRollsBackAllRuns(t *testing.T) {
	s := testStore(t)
	req := evaluationRequest(t, s)
	if err := s.db.Exec(
		`CREATE FUNCTION reject_second_trial() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.case_id='b' THEN RAISE EXCEPTION 'injected trial failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_trial BEFORE INSERT ON evaluation_trial FOR EACH ROW EXECUTE FUNCTION reject_second_trial()`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartEvaluation(t.Context(), "owner", "rollback", req); err == nil {
		t.Fatal("expected failure")
	}
	for _, model := range []any{&evaluationRecord{}, &evaluationTrialRecord{}, &runRecord{}, &eventRecord{}, &messageRecord{}} {
		var n int64
		if err := s.db.Model(model).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("partial commit %T: %d %v", model, n, err)
		}
	}
}

func TestEvaluationCompletionAttentionAndCancel(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		status       Status
		output       string
		wantStatus   string
		wantScore    string
		wantCoverage float64
		pass         bool
	}{
		{name: "success", status: Completed, output: `{"ok":true}`, wantStatus: "Completed", wantScore: "Scored", wantCoverage: 1, pass: true},
		{name: "execution failure", status: Failed, output: "", wantStatus: "Completed", wantScore: "ExecutionError", wantCoverage: 0.5, pass: false},
		{name: "clarification", status: WaitingClarification, output: "", wantStatus: "NeedsAttention", wantScore: "Incomplete", wantCoverage: 0.5, pass: false},
		{name: "uncertain tool", status: NeedsReview, output: "", wantStatus: "NeedsAttention", wantScore: "Incomplete", wantCoverage: 0.5, pass: false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := testStore(t)
			ctx := t.Context()
			req := evaluationRequest(t, s)
			out, _, err := s.StartEvaluation(ctx, "owner", "run", req)
			if err != nil {
				t.Fatal(err)
			}
			first := claim(t, s, "worker")
			if err = s.Save(
				ctx,
				first.Lease(),
				json.RawMessage(`{}`),
				Completed,
				`{"ok":true}`,
				"",
				"RunCompleted",
			); err != nil {
				t.Fatal(err)
			}
			next := claim(t, s, "worker")
			if err = s.Save(
				ctx,
				next.Lease(),
				json.RawMessage(`{"waitingStep":"step"}`),
				scenario.status,
				scenario.output,
				"test status",
				"Progress",
			); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := s.ReconcileEvaluation(ctx, out.ID); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			got, err := s.Evaluation(ctx, "owner", out.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != scenario.wantStatus || got.Report.Passed != scenario.pass ||
				got.Report.Metrics[0].Coverage != scenario.wantCoverage {
				t.Fatalf("unexpected report %+v", got)
			}
			statuses := map[string]string{}
			for _, score := range got.Report.Scores {
				statuses[score.CaseID] = score.Status
			}
			secondCase := ""
			for _, trial := range got.Trials {
				if trial.RunID == next.ID {
					secondCase = trial.CaseID
				}
			}
			if statuses[secondCase] != scenario.wantScore {
				t.Fatal(statuses)
			}
			if got.Status == "NeedsAttention" {
				if err = s.CancelEvaluation(ctx, "owner", out.ID); err != nil {
					t.Fatal(err)
				}
				got, err = s.Evaluation(ctx, "owner", out.ID)
				if err != nil || got.Status != "Canceled" || got.Report.Passed {
					t.Fatalf("cancel: %+v %v", got, err)
				}
				if err = s.Save(
					ctx,
					next.Lease(),
					json.RawMessage(`{}`),
					Completed,
					"stale",
					"",
					"RunCompleted",
				); !errors.Is(
					err,
					ErrLeaseLost,
				) {
					t.Fatal("canceled run accepted old worker", err)
				}
				if err = s.CancelEvaluation(ctx, "owner", out.ID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestEvaluationCancelCommitsWithRunEvents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	req := evaluationRequest(t, s)
	out, _, err := s.StartEvaluation(ctx, "owner", "cancel", req)
	if err != nil {
		t.Fatal(err)
	}
	active := claim(t, s, "worker")
	if err = s.db.Exec(
		`CREATE FUNCTION reject_cancel() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='RunCanceled' THEN RAISE EXCEPTION 'injected cancel failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_cancel BEFORE INSERT ON durable_event FOR EACH ROW EXECUTE FUNCTION reject_cancel()`,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.CancelEvaluation(ctx, "owner", out.ID); err == nil {
		t.Fatal("expected cancel failure")
	}
	run, err := s.Get(ctx, "owner", active.ID)
	if err != nil || run.Status != Running {
		t.Fatalf("partial cancellation: %+v %v", run, err)
	}
	if err = s.Renew(ctx, active.Lease(), time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationLateCancelPreservesCompletedResult(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	req := evaluationRequest(t, s)
	out, _, err := s.StartEvaluation(ctx, "owner", "done", req)
	if err != nil {
		t.Fatal(err)
	}
	for range out.Trials {
		run := claim(t, s, "worker")
		if err = s.Save(
			ctx,
			run.Lease(),
			json.RawMessage(`{}`),
			Completed,
			`{"ok":true}`,
			"",
			"RunCompleted",
		); err != nil {
			t.Fatal(err)
		}
	}
	// No reconciler has run yet; Cancel must inspect terminal Runs itself.
	if err = s.CancelEvaluation(ctx, "owner", out.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Evaluation(ctx, "owner", out.ID)
	if err != nil || got.Status != "Completed" || !got.Report.Passed {
		t.Fatalf("late cancel %+v %v", got, err)
	}
}

func TestEvaluationSharedFilesystemBatchRejected(t *testing.T) {
	s := testStore(t)
	req := evaluationRequest(t, s)
	req.Suite.Target.Capabilities = append(req.Suite.Target.Capabilities, "filesystem")
	req.Suite.Scenario.Filesystem = &sdk.FilesystemBinding{
		Provider:  "AIO",
		URL:       "http://aio.test",
		TokenEnv:  "AIO_TOKEN",
		Workspace: "/workspace",
	}
	if _, _, err := s.StartEvaluation(t.Context(), "owner", "shared-files", req); !errors.Is(err, ErrInvalid) {
		t.Fatal("shared writable workspace accepted", err)
	}
}

func TestEvaluationGateWaitsForEveryRun(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	req := evaluationRequest(t, s)
	req.Suite.Metrics[0].MinimumCoverage = 0.5
	req.Suite.Metrics[0].MinimumScore = 0.5
	out, _, err := s.StartEvaluation(ctx, "owner", "partial", req)
	if err != nil {
		t.Fatal(err)
	}
	run := claim(t, s, "worker")
	if err = s.Save(
		ctx,
		run.Lease(),
		json.RawMessage(`{}`),
		Completed,
		`{"ok":true}`,
		"",
		"RunCompleted",
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.Evaluation(ctx, "owner", out.ID)
	if err != nil || got.Status != "Running" || got.Report.Passed || !got.Report.Metrics[0].Passed {
		t.Fatalf("partial experiment gate %+v %v", got, err)
	}
}

func TestEvaluationCompletionWaitsForPersistedReport(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	req := evaluationRequest(t, s)
	out, _, err := s.StartEvaluation(ctx, "owner", "persisted", req)
	if err != nil {
		t.Fatal(err)
	}
	for range out.Trials {
		run := claim(t, s, "worker")
		if err = s.Save(
			ctx,
			run.Lease(),
			json.RawMessage(`{}`),
			Completed,
			`{"ok":true}`,
			"",
			"RunCompleted",
		); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Evaluation(ctx, "owner", out.ID)
	if err != nil || got.Status != "Running" || got.Report.Passed {
		t.Fatalf("uncommitted score exposed as final: %+v %v", got, err)
	}
	if err = s.ReconcileEvaluation(ctx, out.ID); err != nil {
		t.Fatal(err)
	}
	var row evaluationRecord
	if err = s.db.Take(
		&row,
		"id = ?",
		out.ID,
	).Error; err != nil || row.Status != "Completed" || row.Report == nil ||
		!row.Report.Passed {
		t.Fatalf("final report not persisted: %+v %v", row, err)
	}
}

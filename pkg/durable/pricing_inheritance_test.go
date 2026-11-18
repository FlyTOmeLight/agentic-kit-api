package durable

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

type pricingSnapshotCase struct {
	name     string
	frozen   bool
	snapshot *execution.PricingSnapshot
}

func pricingSnapshotCases() []pricingSnapshotCase {
	cachedZero := 0.0
	return []pricingSnapshotCase{
		{
			name:   "frozen snapshot",
			frozen: true,
			snapshot: &execution.PricingSnapshot{
				Version:               "catalog-2026-11-17",
				Model:                 "priced-model",
				BaseURL:               "https://pricing.example/v1",
				InputPerMillion:       1.25,
				CachedInputPerMillion: &cachedZero,
				OutputPerMillion:      4.5,
			},
		},
		{name: "frozen without snapshot", frozen: true},
		{name: "unfrozen without snapshot", frozen: false},
	}
}

func acceptPricingRun(
	t *testing.T,
	store *Store,
	sessionID string,
	config AgentConfig,
	key string,
	query string,
	snapshot *execution.PricingSnapshot,
) *Run {
	t.Helper()
	agentID, version := sessionPublishedFixture(t, store, sessionID)
	input, err := json.Marshal(struct{ Session, Query string }{Session: sessionID, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.Accept(context.Background(), execution.SubmitCommand{
		PricingSnapshot: snapshot,
		Owner:           "owner",
		RequestKey:      key,
		RequestHash:     Hash(input),
		TTL:             time.Hour,
		MaxAttempts:     3,
		Kind:            execution.SubmitChat,
		SessionID:       sessionID,
		Query:           query,
	}, PrepareChatSubmission(config, agentID, version, nil))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func setPricingFrozen(t *testing.T, store *Store, runID string, frozen bool) {
	t.Helper()
	value := "false"
	if frozen {
		value = "true"
	}
	if err := store.DB().WithContext(context.Background()).Exec(
		`UPDATE durable_run SET spec = jsonb_set(spec, '{pricingFrozen}', ?::jsonb, true) WHERE id = ?`,
		value,
		runID,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func readStoredSpec(t *testing.T, store *Store, runID string) execution.Spec {
	t.Helper()
	var stored struct {
		Spec string `gorm:"column:spec"`
	}
	if err := store.DB().WithContext(context.Background()).Raw(
		`SELECT spec::text AS spec FROM durable_run WHERE id = ?`,
		runID,
	).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var spec execution.Spec
	if err := json.Unmarshal([]byte(stored.Spec), &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func assertPricing(
	t *testing.T,
	label string,
	frozen bool,
	snapshot *execution.PricingSnapshot,
	wantFrozen bool,
	wantSnapshot *execution.PricingSnapshot,
) {
	t.Helper()
	if frozen != wantFrozen || !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("%s pricing = frozen:%v snapshot:%+v, want frozen:%v snapshot:%+v",
			label, frozen, snapshot, wantFrozen, wantSnapshot)
	}
}

func TestPricingSnapshotJSONBRoundTrip(t *testing.T) {
	for _, test := range pricingSnapshotCases() {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			session, config := testConversation(t, store, Spec{MaxTurns: 5})
			run := acceptPricingRun(
				t,
				store,
				session.ID,
				config,
				NewID(),
				"pricing round trip",
				test.snapshot,
			)
			if !test.frozen {
				setPricingFrozen(t, store, run.ID, false)
			}

			stored, err := store.Get(context.Background(), "owner", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertPricing(t, "stored run", stored.Spec.PricingFrozen, stored.Spec.PricingSnapshot,
				test.frozen, test.snapshot)

			spec := readStoredSpec(t, store, run.ID)
			assertPricing(t, "raw jsonb", spec.PricingFrozen, spec.PricingSnapshot,
				test.frozen, test.snapshot)
		})
	}
}

func failedPricingGoalIteration(
	t *testing.T,
	store *Store,
	test pricingSnapshotCase,
) (*Goal, *Run) {
	t.Helper()
	ctx := context.Background()
	session, config := testConversation(t, store, Spec{MaxTurns: 5})
	goal := newTestGoal(session.ID, NewID(), "continue with frozen pricing")
	goal.Evaluator = EvaluatorSpec{Kind: EvaluatorCommand, Config: "check"}
	goal.Budget = BudgetSpec{MaxWallClockSeconds: 3600}
	current, _, err := store.CreateGoal(ctx, goal)
	if err != nil {
		t.Fatal(err)
	}
	run := acceptPricingRun(
		t,
		store,
		session.ID,
		config,
		NewID(),
		"original pricing request",
		test.snapshot,
	)
	if !test.frozen {
		setPricingFrozen(t, store, run.ID, false)
	}
	if err = store.BindGoalToRun(ctx, "owner", current.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	claimed := claim(t, store, "worker")
	if claimed.ID != run.ID {
		t.Fatalf("claimed run %s, want %s", claimed.ID, run.ID)
	}
	if err = store.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:   BoundaryComplete,
		State:  json.RawMessage(`{}`),
		Output: "previous answer",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RecordEvaluation(
		ctx,
		"owner",
		current.ID,
		run.ID,
		EvaluationFailed,
		"missing required field",
	); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CheckGoalProgress(ctx, "owner", current.ID); err != nil {
		t.Fatal(err)
	}
	previous, err := store.Get(ctx, "owner", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return current, previous
}

func TestGoalIterationInheritsPricing(t *testing.T) {
	for _, test := range pricingSnapshotCases() {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			goal, _ := failedPricingGoalIteration(t, store, test)
			next, err := store.QueueGoalIteration(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if next.GoalID != goal.ID {
				t.Fatalf("next goal = %s, want %s", next.GoalID, goal.ID)
			}
			assertPricing(t, "goal iteration", next.Spec.PricingFrozen, next.Spec.PricingSnapshot,
				test.frozen, test.snapshot)

			spec := readStoredSpec(t, store, next.ID)
			assertPricing(t, "stored goal iteration", spec.PricingFrozen, spec.PricingSnapshot,
				test.frozen, test.snapshot)
		})
	}
}

func TestSpawnChildRunInheritsPricing(t *testing.T) {
	for _, test := range pricingSnapshotCases() {
		t.Run(test.name, func(t *testing.T) {
			store := testStore(t)
			ctx := context.Background()
			session, config := testConversation(t, store, Spec{MaxTurns: 5})
			parent := acceptPricingRun(
				t,
				store,
				session.ID,
				config,
				NewID(),
				"parent pricing request",
				test.snapshot,
			)
			if !test.frozen {
				setPricingFrozen(t, store, parent.ID, false)
			}
			claimed := claim(t, store, "worker")
			if claimed.ID != parent.ID {
				t.Fatalf("claimed run %s, want parent %s", claimed.ID, parent.ID)
			}
			intent := StepIntent{ID: "spawn-pricing", Input: "child pricing request", Policy: Idempotent}
			if _, err := store.BeginStep(ctx, claimed.Lease(), intent); err != nil {
				t.Fatal(err)
			}
			child, err := store.SpawnChildRun(
				ctx,
				claimed.Lease(),
				intent.ID,
				intent.Input,
				time.Hour,
			)
			if err != nil {
				t.Fatal(err)
			}
			assertPricing(t, "child run", child.Spec.PricingFrozen, child.Spec.PricingSnapshot,
				test.frozen, test.snapshot)

			spec := readStoredSpec(t, store, child.ID)
			assertPricing(t, "stored child run", spec.PricingFrozen, spec.PricingSnapshot,
				test.frozen, test.snapshot)
		})
	}
}

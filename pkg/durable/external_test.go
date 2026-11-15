package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestExternalWaitAndCallbackResumesRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := createRun(t, s)
	claimed := claim(t, s, "worker")

	intent := StepIntent{
		ID:          "call-1",
		Input:       "deploy\x00{}",
		Policy:      Idempotent,
		ExternalRef: "pipelines/42",
	}
	decision, err := s.BeginStep(ctx, claimed.Lease(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != StepExternalWait {
		t.Fatalf("expected ExternalWait, got %s", decision.Outcome)
	}
	if err = s.CommitBoundary(ctx, claimed.Lease(), Boundary{
		Kind:        BoundaryAwaitExternal,
		State:       json.RawMessage(`{}`),
		Detail:      "awaiting CI",
		WaitingStep: intent.ID,
		ExternalRef: intent.ExternalRef,
	}); err != nil {
		t.Fatal(err)
	}

	waiting, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != WaitingExternal {
		t.Fatalf("expected WaitingExternal, got %s", waiting.Status)
	}
	if waiting.WorkerID != "" || waiting.LeaseUntil != nil {
		t.Fatalf("suspended run still holds a lease: worker=%q lease=%v", waiting.WorkerID, waiting.LeaseUntil)
	}

	if err = s.SupplyExternalResult(
		ctx,
		"owner",
		r.ID,
		intent.ID,
		"wrong-ref",
		"ok",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("expected ErrConflict for mismatched ref, got %v", err)
	}

	result := `{"status":"success"}`
	if err = s.SupplyExternalResult(ctx, "owner", r.ID, intent.ID, intent.ExternalRef, result); err != nil {
		t.Fatal(err)
	}
	resumed, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != Queued {
		t.Fatalf("expected Queued after callback, got %s", resumed.Status)
	}

	// Identical replay is idempotent; a different result is a conflict.
	if err = s.SupplyExternalResult(ctx, "owner", r.ID, intent.ID, intent.ExternalRef, result); err != nil {
		t.Fatalf("expected idempotent replay, got %v", err)
	}
	if err = s.SupplyExternalResult(
		ctx,
		"owner",
		r.ID,
		intent.ID,
		intent.ExternalRef,
		"different",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("expected ErrConflict for changed result, got %v", err)
	}

	reclaimed := claim(t, s, "worker")
	if reclaimed.ID != r.ID {
		t.Fatalf("claimed run %s, want %s", reclaimed.ID, r.ID)
	}
	replayed, err := s.BeginStep(ctx, reclaimed.Lease(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Outcome != StepReplay || replayed.Result != result {
		t.Fatalf("expected committed result replay, got %s %q", replayed.Outcome, replayed.Result)
	}
}

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
		"wrong-ref",
		result,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for completed replay with mismatched ref, got %v", err)
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

func TestSupplyExternalResultByRunIDUsesStoredOwner(t *testing.T) {
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

	result := `{"status":"success"}`
	if err = s.SupplyExternalResult(
		ctx,
		"callback-service",
		r.ID,
		intent.ID,
		intent.ExternalRef,
		result,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("owner API accepted service identity: %v", err)
	}
	for _, test := range []struct {
		name string
		id   string
		step string
		ref  string
		want error
	}{
		{name: "missing run", id: "missing-run", step: intent.ID, ref: intent.ExternalRef, want: ErrNotFound},
		{name: "missing step", id: r.ID, step: "missing-step", ref: intent.ExternalRef, want: ErrConflict},
		{name: "wrong ref", id: r.ID, step: intent.ID, ref: "wrong-ref", want: ErrConflict},
		{name: "empty ref", id: r.ID, step: intent.ID, want: ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := s.SupplyExternalResultByRunID(
				ctx,
				test.id,
				test.step,
				test.ref,
				result,
			); !errors.Is(
				err,
				test.want,
			) {
				t.Fatalf("callback error = %v, want %v", err, test.want)
			}
		})
	}
	if err = s.SupplyExternalResultByRunID(ctx, r.ID, intent.ID, intent.ExternalRef, result); err != nil {
		t.Fatal(err)
	}
	before, err := s.Events(ctx, "owner", r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		ref    string
		result string
		want   error
	}{
		{name: "identical", ref: intent.ExternalRef, result: result},
		{name: "changed ref", ref: "wrong-ref", result: result, want: ErrConflict},
		{name: "changed result", ref: intent.ExternalRef, result: "different", want: ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := s.SupplyExternalResultByRunID(
				ctx,
				r.ID,
				intent.ID,
				test.ref,
				test.result,
			); !errors.Is(
				err,
				test.want,
			) {
				t.Fatalf("replay error = %v, want %v", err, test.want)
			}
		})
	}
	after, err := s.Events(ctx, "owner", r.ID, 0)
	if err != nil || len(after) != len(before) {
		t.Fatalf("replay emitted events: before=%d after=%d err=%v", len(before), len(after), err)
	}
	resumed, err := s.Get(ctx, "owner", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != Queued {
		t.Fatalf("expected Queued after callback, got %s", resumed.Status)
	}
}

package durable

import (
	"context"
	"errors"
	"testing"
)

func TestBeginStepOutcomes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	r := claim(t, s, "a")
	intent := StepIntent{ID: "lookup", Input: "term", Policy: ReadOnly}
	if d, err := s.BeginStep(ctx, r.Lease(), intent); err != nil || d.Outcome != StepExecute {
		t.Fatalf("first: %+v %v", d, err)
	}
	events, err := s.Events(ctx, "owner", r.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, e := range events {
		if e.Kind == "ToolStarted" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("ToolStarted=%d want 1", started)
	}
	if d, err := s.BeginStep(ctx, r.Lease(), intent); err != nil || d.Outcome != StepExecute {
		t.Fatalf("in-flight read-only: %+v %v", d, err)
	}
	if _, err := s.BeginStep(
		ctx,
		r.Lease(),
		StepIntent{ID: "lookup", Input: "other", Policy: ReadOnly},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed input: %v", err)
	}
	if _, err := s.BeginStep(
		ctx,
		r.Lease(),
		StepIntent{ID: "lookup", Input: "term", Policy: Idempotent},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed policy: %v", err)
	}
	if err := s.FinishStep(ctx, r.Lease(), "lookup", "result", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if d, err := s.BeginStep(
		ctx,
		r.Lease(),
		intent,
	); err != nil || d.Outcome != StepReplay ||
		d.Result != "result" {
		t.Fatalf("replay: %+v %v", d, err)
	}
	if d, err := s.BeginStep(
		ctx,
		r.Lease(),
		StepIntent{ID: "unsafe", Input: "x", Policy: Unsafe},
	); err != nil || d.Outcome != StepExecute {
		t.Fatalf("unsafe first: %+v %v", d, err)
	}
	if d, err := s.BeginStep(
		ctx,
		r.Lease(),
		StepIntent{ID: "unsafe", Input: "x", Policy: Unsafe},
	); err != nil || d.Outcome != StepUncertain {
		t.Fatalf("unsafe repeat: %+v %v", d, err)
	}
	invalid := []StepIntent{
		{ID: "", Input: "x", Policy: ReadOnly},
		{ID: "x", Input: "x", Policy: "Unknown"},
	}
	for _, intent := range invalid {
		if _, err := s.BeginStep(ctx, r.Lease(), intent); !errors.Is(err, ErrInvalid) {
			t.Fatalf("intent %+v: %v", intent, err)
		}
	}
}

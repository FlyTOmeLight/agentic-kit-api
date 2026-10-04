package durable

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplicationExecutionSnapshotAndFencing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	spec := Spec{Scope: &Scope{TenantID: "t", ProjectID: "p", ActorID: "u", TaskID: "task", RoundID: "round"}, Query: "fix", MaxTurns: 3, Model: ModelSpec{Name: "test", BaseURL: "http://model.test/v1"}}
	r, created, e := s.SubmitExecution(ctx, "coding", "round", spec, time.Minute)
	if e != nil || !created {
		t.Fatal(e)
	}
	again, created, e := s.SubmitExecution(ctx, "coding", "round", spec, time.Minute)
	if e != nil || created || again.ID != r.ID {
		t.Fatal(e)
	}
	spec.Scope.ProjectID = "foreign"
	if _, _, e = s.SubmitExecution(ctx, "coding", "round", spec, time.Minute); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	claimed := claim(t, s, "worker")
	if claimed.Spec.Scope.ProjectID != "p" {
		t.Fatal("scope mutated")
	}
	ok, e := s.CheckExecutionLease(ctx, "coding", r.ID, claimed.Epoch)
	if e != nil || !ok {
		t.Fatal(e)
	}
	ok, _ = s.CheckExecutionLease(ctx, "other", r.ID, claimed.Epoch)
	if ok {
		t.Fatal("owner leak")
	}
	if e = s.Cancel(ctx, "coding", r.ID); e != nil {
		t.Fatal(e)
	}
	ok, _ = s.CheckExecutionLease(ctx, "coding", r.ID, claimed.Epoch)
	if ok {
		t.Fatal("canceled lease accepted")
	}
	for range 2 {
		if e = s.Migrate(ctx); e != nil {
			t.Fatal(e)
		}
	}
}

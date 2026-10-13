package durable

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestApplicationExecutionSnapshotAndFencing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent, e := s.CreateAgent(
		ctx,
		"coding",
		"Coding",
		AgentConfig{
			SystemPrompt: "published",
			MaxTurns:     3,
			Model:        ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
		},
	)
	if e != nil {
		t.Fatal(e)
	}
	version, e := s.PublishAgent(ctx, "coding", agent.ID, agent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	spec := sdk.ExecutionSpec{
		Scope:        &Scope{TenantID: "t", ProjectID: "p", ActorID: "u", TaskID: "task", RoundID: "round"},
		Query:        "fix",
		AgentID:      agent.ID,
		AgentVersion: version.Number,
	}
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

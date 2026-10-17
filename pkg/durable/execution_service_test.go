package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestToolExecutionDoesNotRequireProject(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tools      []ToolSpec
		toolsets   []string
		filesystem *sdk.FilesystemBinding
	}{
		{
			name: "HTTP tool",
			tools: []ToolSpec{{
				Name:       "lookup",
				URL:        "http://tool.test/lookup",
				Parameters: json.RawMessage(`{"type":"object"}`),
				Policy:     ReadOnly,
			}},
		},
		{
			name:     "filesystem",
			toolsets: []string{"filesystem"},
			filesystem: &sdk.FilesystemBinding{
				Provider:  "AIO",
				URL:       "http://sandbox.test",
				TokenEnv:  "SANDBOX_TOKEN",
				Workspace: "/workspace",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			agent, err := s.CreateAgent(ctx, "app", "Assistant", AgentConfig{
				Model:    ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
				MaxTurns: 3,
				Tools:    tc.tools,
				Toolsets: tc.toolsets,
			})
			if err != nil {
				t.Fatal(err)
			}
			version, err := s.PublishAgent(ctx, "app", agent.ID, agent.Revision)
			if err != nil {
				t.Fatal(err)
			}
			spec := sdk.ExecutionSpec{
				AgentID:      agent.ID,
				AgentVersion: version.Number,
				Query:        "work",
				Scope:        &Scope{TenantID: "t", ActorID: "u", TaskID: "task", RoundID: "round"},
				Filesystem:   tc.filesystem,
			}
			run, created, err := s.SubmitExecution(ctx, "app", "no-project", spec, time.Minute)
			if err != nil || !created {
				t.Fatalf("projectless tool execution: created=%t err=%v", created, err)
			}
			if run.Spec.Scope.ProjectID != "" || len(run.Spec.Tools) != len(tc.tools) ||
				len(run.Spec.Toolsets) != len(tc.toolsets) {
				t.Fatalf("execution snapshot changed: %+v", run.Spec)
			}
			replayed, created, err := s.SubmitExecution(ctx, "app", "no-project", spec, time.Minute)
			if err != nil || created || replayed.ID != run.ID {
				t.Fatalf("replay: created=%t err=%v", created, err)
			}
			spec.Scope.ProjectID = "p"
			projectRun, created, err := s.SubmitExecution(ctx, "app", "with-project", spec, time.Minute)
			if err != nil || !created || projectRun.SessionID == run.SessionID {
				t.Fatalf("project scope isolation: created=%t err=%v", created, err)
			}
		})
	}
}

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

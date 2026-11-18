package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
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
			cfg := AgentConfig{
				Model:    ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
				MaxTurns: 3,
				Tools:    tc.tools,
				Toolsets: tc.toolsets,
			}
			agent := createAgentFixture(t, s, "app", "Assistant", cfg)
			version := publishAgentFixture(t, s, "app", agent.ID, agent.Revision)
			spec := sdk.ExecutionSpec{
				AgentID:      agent.ID,
				AgentVersion: version.Number,
				Query:        "work",
				Scope:        &Scope{TenantID: "t", ActorID: "u", TaskID: "task", RoundID: "round"},
				Filesystem:   tc.filesystem,
			}
			run, created, err := submitExecutionFixture(t, s, "app", "no-project", spec, time.Minute, cfg)
			if err != nil || !created {
				t.Fatalf("projectless tool execution: created=%t err=%v", created, err)
			}
			if run.Spec.Scope.ProjectID != "" || len(run.Spec.Tools) != len(tc.tools) ||
				len(run.Spec.Toolsets) != len(tc.toolsets) {
				t.Fatalf("execution snapshot changed: %+v", run.Spec)
			}
			replayed, created, err := submitExecutionFixture(
				t,
				s,
				"app",
				"no-project",
				spec,
				time.Minute,
				cfg,
			)
			if err != nil || created || replayed.ID != run.ID {
				t.Fatalf("replay: created=%t err=%v", created, err)
			}
			spec.Scope.ProjectID = "p"
			projectRun, created, err := submitExecutionFixture(
				t,
				s,
				"app",
				"with-project",
				spec,
				time.Minute,
				cfg,
			)
			if err != nil || !created || projectRun.SessionID == run.SessionID {
				t.Fatalf("project scope isolation: created=%t err=%v", created, err)
			}
		})
	}
}

func TestApplicationExecutionSnapshotAndFencing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cfg := AgentConfig{
		SystemPrompt: "published",
		MaxTurns:     3,
		Model:        ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
	}
	agent := createAgentFixture(t, s, "coding", "Coding", cfg)
	version := publishAgentFixture(t, s, "coding", agent.ID, agent.Revision)
	spec := sdk.ExecutionSpec{
		Scope:        &Scope{TenantID: "t", ProjectID: "p", ActorID: "u", TaskID: "task", RoundID: "round"},
		Query:        "fix",
		AgentID:      agent.ID,
		AgentVersion: version.Number,
	}
	r, created, e := submitExecutionFixture(t, s, "coding", "round", spec, time.Minute, cfg)
	if e != nil || !created {
		t.Fatal(e)
	}
	again, created, e := submitExecutionFixture(t, s, "coding", "round", spec, time.Minute, cfg)
	if e != nil || created || again.ID != r.ID {
		t.Fatal(e)
	}
	spec.Scope.ProjectID = "foreign"
	if _, _, e = submitExecutionFixture(
		t,
		s,
		"coding",
		"round",
		spec,
		time.Minute,
		cfg,
	); !errors.Is(
		e,
		ErrConflict,
	) {
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
		if e = s.InitSchema(ctx); e != nil {
			t.Fatal(e)
		}
	}
}

func TestCheckExecutionLeaseEnforcesDeadline(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	claimed := claim(t, s, "worker")
	ok, err := s.CheckExecutionLease(ctx, "owner", claimed.ID, claimed.Epoch)
	if err != nil || !ok {
		t.Fatalf("active lease rejected: ok=%v err=%v", ok, err)
	}
	if err := s.db.WithContext(ctx).
		Model(&table.Run{}).
		Where("id = ?", claimed.ID).
		Update("deadline", gorm.Expr("clock_timestamp()-interval '1 second'")).
		Error; err != nil {
		t.Fatal(err)
	}
	ok, err = s.CheckExecutionLease(ctx, "owner", claimed.ID, claimed.Epoch)
	if err != nil {
		t.Fatalf("deadline-expired lease surfaced error: %v", err)
	}
	if ok {
		t.Fatal("lease accepted after deadline expired")
	}
}

func TestAuthorizeExecutionBoundsAndWorkerCheck(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	createRun(t, s)
	claimed := claim(t, s, "worker-a")

	verdict, err := s.AuthorizeExecution(ctx, "owner", claimed.ID, "worker-a", claimed.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	if !verdict.Allowed || verdict.RunID != claimed.ID || verdict.WorkerID != "worker-a" ||
		verdict.Epoch != claimed.Epoch {
		t.Fatalf("unexpected authorization verdict: %+v", verdict)
	}
	if verdict.CheckedAt.IsZero() || verdict.ExpiresAt.IsZero() || !verdict.ExpiresAt.After(verdict.CheckedAt) {
		t.Fatalf(
			"expected database clock bounds, got checkedAt=%v expiresAt=%v",
			verdict.CheckedAt,
			verdict.ExpiresAt,
		)
	}

	// Another worker cannot authorize under worker-a's claim.
	wrongWorker, err := s.AuthorizeExecution(ctx, "owner", claimed.ID, "worker-b", claimed.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	if wrongWorker.Allowed {
		t.Fatal("mismatched worker must not be authorized")
	}
}

package durable

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestApplicationsReusePublishedAgentsAndTaskConversations(t *testing.T) {
	for _, name := range []string{"Writing", "Coding"} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			cfgV1 := AgentConfig{
				SystemPrompt: "v1",
				Model:        ModelSpec{Name: "local", BaseURL: "http://model.test/v1"},
				MaxTurns:     20,
			}
			a := createAgentFixture(t, s, "app", name, cfgV1)
			req := sdk.ExecutionSpec{
				AgentID:      a.ID,
				AgentVersion: 1,
				Query:        "first request",
				Scope: &Scope{
					TenantID:  "t",
					ProjectID: "p",
					ActorID:   "u",
					TaskID:    "task",
					RoundID:   "round1",
				},
			}
			publishAgentFixture(t, s, "app", a.ID, a.Revision)
			first, _, err := submitExecutionFixture(t, s, "app", "round1", req, time.Minute, cfgV1)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = submitExecutionFixture(
				t, s, "app", "overlap", req, time.Minute, cfgV1,
			); !errors.Is(
				err,
				ErrConflict,
			) {
				t.Fatalf("overlap: %v", err)
			}
			changed := cfgV1
			changed.SystemPrompt = "v2"
			updated := updateAgentFixture(t, s, "app", a.ID, name, a.Revision, changed)
			publishAgentFixture(t, s, "app", a.ID, updated.Revision)
			replay, created, err := submitExecutionFixture(t, s, "app", "round1", req, time.Minute, cfgV1)
			if err != nil || created || replay.ID != first.ID || replay.Spec.SystemPrompt != "v1" {
				t.Fatalf("retry changed snapshot: %+v %v", replay, err)
			}
			running := claim(t, s, "worker")
			if err = s.CommitBoundary(
				ctx,
				running.Lease(),
				Boundary{Kind: BoundaryComplete, State: []byte(`{}`), Output: "first answer"},
			); err != nil {
				t.Fatal(err)
			}
			req.AgentVersion = 2
			req.Scope.RoundID = "round2"
			req.Query = "revise it"
			second, _, err := submitExecutionFixture(t, s, "app", "round2", req, time.Minute, changed)
			if err != nil {
				t.Fatal(err)
			}
			if second.AgentID != first.AgentID || second.SessionID != first.SessionID ||
				second.Spec.SystemPrompt != "v2" ||
				len(second.Spec.History) != 2 ||
				second.Spec.History[1].Content != "first answer" {
				t.Fatalf("conversation/version lost: %+v", second)
			}
			if err = s.Cancel(ctx, "app", second.ID); err != nil {
				t.Fatal(err)
			}
			req.Scope.ProjectID = "other-project"
			third, _, err := submitExecutionFixture(t, s, "app", "other-project", req, time.Minute, changed)
			if err != nil || third.SessionID == first.SessionID || len(third.Spec.History) != 0 {
				t.Fatalf("project isolation: %+v %v", third, err)
			}
			var agents int64
			if err = s.db.Model(&table.Agent{}).Count(&agents).Error; err != nil || agents != 1 {
				t.Fatalf("execution created agents: %d %v", agents, err)
			}
		})
	}
}

func TestConcurrentApplicationRoundsShareOneConversation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cfg := AgentConfig{Model: ModelSpec{Name: "local", BaseURL: "http://model.test/v1"}, MaxTurns: 20}
	agent := createAgentFixture(t, s, "app", "Coding", cfg)
	publishAgentFixture(t, s, "app", agent.ID, agent.Revision)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, round := range []string{"one", "two"} {
		wg.Add(1)
		go func(round string) {
			defer wg.Done()
			_, _, err := submitExecutionFixture(
				t, s,
				"app",
				round,
				sdk.ExecutionSpec{
					AgentID:      agent.ID,
					AgentVersion: 1,
					Query:        "work",
					Scope: &Scope{
						TenantID:  "t",
						ProjectID: "p",
						ActorID:   "u",
						TaskID:    "same-task",
						RoundID:   round,
					},
				},
				time.Minute,
				cfg,
			)
			results <- err
		}(round)
	}
	wg.Wait()
	close(results)
	accepted, conflicts := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("accepted=%d conflicts=%d", accepted, conflicts)
	}
	sessions, err := s.Sessions(ctx, "app", agent.ID, "", 100)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%+v error=%v", sessions, err)
	}
}

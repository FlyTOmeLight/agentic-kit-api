package durable

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func TestApplicationsReusePublishedAgentsAndTaskConversations(t *testing.T) {
	for _, name := range []string{"Writing", "Coding"} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			a, err := s.CreateAgent(
				ctx,
				"app",
				name,
				AgentConfig{
					SystemPrompt: "v1",
					Model:        ModelSpec{Name: "local", BaseURL: "http://model.test/v1"},
					MaxTurns:     20,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
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
			if _, _, err = s.SubmitExecution(
				ctx,
				"app",
				"unpublished",
				req,
				time.Minute,
			); !errors.Is(
				err,
				ErrNotFound,
			) {
				t.Fatalf("unpublished: %v", err)
			}
			if _, err = s.PublishAgent(ctx, "app", a.ID, a.Revision); err != nil {
				t.Fatal(err)
			}
			first, _, err := s.SubmitExecution(ctx, "app", "round1", req, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.SubmitExecution(
				ctx,
				"foreign",
				"round1",
				req,
				time.Minute,
			); !errors.Is(
				err,
				ErrNotFound,
			) {
				t.Fatalf("foreign owner: %v", err)
			}
			if _, _, err = s.SubmitExecution(
				ctx,
				"app",
				"overlap",
				req,
				time.Minute,
			); !errors.Is(
				err,
				ErrConflict,
			) {
				t.Fatalf("overlap: %v", err)
			}
			changed := a.Draft
			changed.SystemPrompt = "v2"
			a, err = s.UpdateAgent(ctx, "app", a.ID, name, a.Revision, changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.PublishAgent(ctx, "app", a.ID, a.Revision); err != nil {
				t.Fatal(err)
			}
			replay, created, err := s.SubmitExecution(ctx, "app", "round1", req, time.Minute)
			if err != nil || created || replay.ID != first.ID || replay.Spec.SystemPrompt != "v1" {
				t.Fatalf("retry changed snapshot: %+v %v", replay, err)
			}
			running := claim(t, s, "worker")
			if err = s.Save(
				ctx,
				running.Lease(),
				[]byte(`{}`),
				Completed,
				"first answer",
				"",
				"RunCompleted",
			); err != nil {
				t.Fatal(err)
			}
			req.AgentVersion = 2
			req.Scope.RoundID = "round2"
			req.Query = "revise it"
			second, _, err := s.SubmitExecution(ctx, "app", "round2", req, time.Minute)
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
			third, _, err := s.SubmitExecution(ctx, "app", "other-project", req, time.Minute)
			if err != nil || third.SessionID == first.SessionID || len(third.Spec.History) != 0 {
				t.Fatalf("project isolation: %+v %v", third, err)
			}
			var agents int64
			if err = s.db.Model(&agentRecord{}).Count(&agents).Error; err != nil || agents != 1 {
				t.Fatalf("execution created agents: %d %v", agents, err)
			}
		})
	}
}

func TestConcurrentApplicationRoundsShareOneConversation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent, err := s.CreateAgent(
		ctx,
		"app",
		"Coding",
		AgentConfig{Model: ModelSpec{Name: "local", BaseURL: "http://model.test/v1"}, MaxTurns: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishAgent(ctx, "app", agent.ID, agent.Revision); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, round := range []string{"one", "two"} {
		wg.Add(1)
		go func(round string) {
			defer wg.Done()
			_, _, err := s.SubmitExecution(
				ctx,
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

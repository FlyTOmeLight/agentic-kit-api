package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	appagent "github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

type submissionRecaller struct {
	query  string
	filter memory.RecallFilter
	calls  int
}

func (recaller *submissionRecaller) RecallTx(
	_ context.Context,
	_ *gorm.DB,
	_ memory.Scope,
	_ string,
	query string,
	filter memory.RecallFilter,
) ([]memory.Entry, error) {
	recaller.query = query
	recaller.filter = filter
	recaller.calls++
	return nil, nil
}

func TestSubmissionRecallQueryAndFilterReplay(t *testing.T) {
	for _, kind := range []string{"chat", "application"} {
		t.Run(kind, func(t *testing.T) {
			store := durableTestStore(t)
			artifactServer, _ := newFakeArtifact(t)
			artifact := newArtifactClient(t, artifactServer)
			agents := appagent.NewService(store.DB(), artifact)
			ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant"})
			identity := client.Identity{TenantID: "tenant", PropagatedUserID: "owner"}
			config := agent.AgentConfig{
				Model:    agent.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
				MaxTurns: 5,
			}
			createdAgent, err := agents.CreateAgent(ctx, "owner", "recall", config)
			if err != nil {
				t.Fatal(err)
			}
			version, err := agents.PublishAgent(
				ctx,
				identity,
				"owner",
				createdAgent.ID,
				createdAgent.Revision,
			)
			if err != nil {
				t.Fatal(err)
			}
			session, err := store.CreateSession(ctx, "owner", createdAgent.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			recaller := &submissionRecaller{}
			submissions := appexec.NewService(store, artifact).WithMemoryRecaller(recaller)
			spec := execution.ExecutionSpec{
				AgentID:      createdAgent.ID,
				AgentVersion: version.Number,
				Query:        "editor preference",
				Scope: &workspace.Scope{
					TenantID: "tenant",
					ActorID:  "owner",
					TaskID:   "task",
					RoundID:  "round",
				},
			}
			submit := func(filter memory.RecallFilter) (*execution.Run, bool, error) {
				if kind == "application" {
					return submissions.SubmitApplication(
						ctx,
						identity,
						"owner",
						"recall-key",
						spec,
						time.Hour,
						memory.SelectionRecall,
						filter,
					)
				}
				return submissions.SubmitChat(
					ctx, identity, "owner", session.ID, "recall-key", spec.Query,
					time.Hour, 3, memory.SelectionRecall, filter,
				)
			}
			filter := memory.RecallFilter{Terms: []string{"editor"}, MaxEntries: 2}
			first, created, err := submit(filter)
			if err != nil || !created {
				t.Fatalf("submit: created=%v err=%v", created, err)
			}
			if recaller.query != spec.Query || !reflect.DeepEqual(recaller.filter, filter) {
				t.Fatalf("recall: query=%q filter=%+v", recaller.query, recaller.filter)
			}
			replayed, created, err := submit(filter)
			if err != nil || created || replayed.ID != first.ID || recaller.calls != 1 {
				t.Fatalf(
					"replay: run=%+v created=%v calls=%d err=%v",
					replayed,
					created,
					recaller.calls,
					err,
				)
			}
			filter.Terms = []string{"different"}
			if _, _, err := submit(filter); !errors.Is(err, durable.ErrConflict) {
				t.Fatalf("changed filter: want conflict, got %v", err)
			}
			if recaller.calls != 1 {
				t.Fatal("replayed or conflicting request recalled memory again")
			}
		})
	}
}

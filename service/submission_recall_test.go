package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	appagent "github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	appmemory "github.com/FlyTOmeLight/agentic-kit-api/internal/app/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
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

func TestApplicationActorPrivateRecall(t *testing.T) {
	const tenant, owner, other = "tenant", "owner", "other"
	store := durableTestStore(t)
	artifactServer, _ := newFakeArtifact(t)
	artifact := newArtifactClient(t, artifactServer)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: tenant})
	agents := appagent.NewService(store.DB(), artifact)
	memories := appmemory.NewService(store.DB(), nil)
	submissions := appexec.NewService(store, artifact).WithMemoryRecaller(memories)
	config := agent.AgentConfig{
		Model: agent.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"}, MaxTurns: 5,
	}
	submit := func(user, actor string) (*execution.Run, error) {
		t.Helper()
		identity := client.Identity{TenantID: tenant, PropagatedUserID: user}
		createdAgent, err := agents.CreateAgent(ctx, user, "recall", config)
		if err != nil {
			t.Fatal(err)
		}
		version, err := agents.PublishAgent(ctx, identity, user, createdAgent.ID, createdAgent.Revision)
		if err != nil {
			t.Fatal(err)
		}
		spec := execution.ExecutionSpec{
			AgentID:      createdAgent.ID,
			AgentVersion: version.Number,
			Query:        "editor",
			Scope: &workspace.Scope{
				TenantID: tenant,
				ActorID:  actor,
				TaskID:   durable.NewID(),
				RoundID:  "round",
			},
		}
		run, _, err := submissions.SubmitApplication(ctx, identity, user, durable.NewID(), spec, time.Hour,
			memory.SelectionRecall, memory.RecallFilter{})
		return run, err
	}
	snapshot := func(user string) (*execution.Run, contextbuilder.Snapshot) {
		t.Helper()
		run, err := submit(user, user)
		if err != nil {
			t.Fatal(err)
		}
		result, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
			Format: run.ContextSnapshot.Format, PolicyVersion: run.ContextSnapshot.PolicyVersion,
			Digest: run.ContextSnapshot.Digest, Payload: run.ContextSnapshot.Payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		return run, result
	}

	doc := memory.Document{
		Scope: memory.Scope{Kind: memory.ScopeActorPrivate, Tenant: tenant},
		Kind:  memory.KindFact, Summary: "editor preference", Body: json.RawMessage(`"use tabs"`),
		Source: memory.Source{Class: memory.SourceHumanConfirmed},
	}
	entry, err := memories.Create(ctx, owner, owner, "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	frozenRun, first := snapshot(owner)
	if first.MemoryMode != contextbuilder.MemoryRecall || len(first.Materials.Memory) != 1 ||
		first.Materials.Memory[0].Source.ID != entry.ID ||
		first.Materials.Memory[0].Content != "[Fact] editor preference\nuse tabs" {
		t.Fatalf("owner's frozen memory: %+v", first.Materials.Memory)
	}
	_, foreign := snapshot(other)
	if foreign.MemoryMode != contextbuilder.MemoryEmpty || len(foreign.Materials.Memory) != 0 {
		t.Fatalf("other user's memory: %+v", foreign.Materials.Memory)
	}
	if run, err := submit(other, owner); !errors.Is(err, memory.ErrForbidden) || run != nil {
		t.Fatalf("mismatched actor: %+v, %v", run, err)
	}

	doc.Body = json.RawMessage(`"use spaces"`)
	if _, err := memories.Update(ctx, owner, owner, "update", entry.ID, 1, doc); err != nil {
		t.Fatal(err)
	}
	_, latest := snapshot(owner)
	if len(latest.Materials.Memory) != 1 ||
		latest.Materials.Memory[0].Content != "[Fact] editor preference\nuse spaces" {
		t.Fatalf("updated memory: %+v", latest.Materials.Memory)
	}
	reloaded, err := store.Get(ctx, owner, frozenRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reloaded.ContextSnapshot.Payload, frozenRun.ContextSnapshot.Payload) {
		t.Fatal("updating memory changed an accepted Run snapshot")
	}
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

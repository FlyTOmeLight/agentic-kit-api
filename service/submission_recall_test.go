package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	appagent "github.com/FlyTOmeLight/agentic-kit-api/internal/app/agent"
	appexec "github.com/FlyTOmeLight/agentic-kit-api/internal/app/execution"
	appmemory "github.com/FlyTOmeLight/agentic-kit-api/internal/app/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

type submissionRecaller struct {
	query       string
	filter      memory.RecallFilter
	calls       int
	readGranted bool
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

func TestApplicationActorProjectRecall(t *testing.T) {
	const tenant, project, owner = "tenant", "project", "owner"
	store := durableTestStore(t)
	artifactServer, _ := newFakeArtifact(t)
	artifact := newArtifactClient(t, artifactServer)
	var status atomic.Int32
	status.Store(http.StatusOK)
	serviceKey := strings.Repeat("k", 32)
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var check struct {
			User    string `json:"user_id"`
			Tenant  string `json:"tenant_id"`
			Project string `json:"project_id"`
			Action  string `json:"action"`
		}
		if request.Method != http.MethodPost || request.URL.Path != "/internal/v1/access/check" ||
			request.Header.Get("Authorization") != "Bearer "+serviceKey ||
			json.NewDecoder(request.Body).Decode(&check) != nil ||
			check.User != owner || check.Tenant != tenant || check.Project != project ||
			check.Action != "read" {
			t.Error("invalid Forge project access check")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch status.Load() {
		case http.StatusOK:
			_, _ = w.Write(
				[]byte(`{"allowed":true,"read":true,"tenant_id":"tenant","project_id":"project"}`),
			)
		case http.StatusForbidden:
			_, _ = w.Write([]byte(`{"allowed":false,"reason":"NO_GRANT"}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(forge.Close)
	tenantClient, err := client.NewTenant(forge.URL, serviceKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tenantClient.Close() })

	ctx := access.WithScope(context.Background(), access.Scope{TenantID: tenant, ProjectID: project})
	projectID := project
	identity := client.Identity{TenantID: tenant, ProjectID: &projectID, PropagatedUserID: owner}
	memories := appmemory.NewService(store.DB(), nil)
	submissions := appexec.NewService(store, artifact).WithMemoryRecaller(memories).
		WithProjectReadCheck(tenantClient.CanReadProject)
	agents := appagent.NewService(store.DB(), artifact)
	createdAgent, err := agents.CreateAgent(ctx, owner, "recall", agent.AgentConfig{
		Model: agent.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"}, MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := agents.PublishAgent(ctx, identity, owner, createdAgent.ID, createdAgent.Revision)
	if err != nil {
		t.Fatal(err)
	}

	scope := memory.Scope{
		Kind:    memory.ScopeActorProject,
		Owner:   owner,
		Tenant:  tenant,
		Project: project,
		Actor:   owner,
	}
	projectEntry := memory.Entry{
		ID: durable.NewID(), Scope: scope, Kind: memory.KindFact, Summary: "editor preference",
		Body: json.RawMessage(`"use tabs"`), OutputVisibility: memory.OutputProject, Revision: 1,
	}
	privateEntry := projectEntry
	privateEntry.ID = durable.NewID()
	privateEntry.OutputVisibility = memory.OutputPrivate
	if err := store.DB().Create([]*table.MemoryEntry{
		table.MemoryEntryFromDomain(&projectEntry), table.MemoryEntryFromDomain(&privateEntry),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := memories.RecallTx(
		ctx,
		store.DB(),
		scope,
		memory.AudienceProject,
		createdAgent.ID,
		"editor",
		memory.RecallFilter{},
	); !errors.Is(
		err,
		memory.ErrForbidden,
	) {
		t.Fatalf("recall without a project grant: %v", err)
	}

	spec := execution.ExecutionSpec{
		AgentID: createdAgent.ID, AgentVersion: version.Number, Query: "editor",
		Scope: &workspace.Scope{
			TenantID: tenant, ProjectID: project, ActorID: owner, TaskID: "task", RoundID: "round",
		},
	}
	submit := func(key string) (*execution.Run, bool, error) {
		return submissions.SubmitApplication(
			ctx, identity, owner, key, spec, time.Hour, memory.SelectionRecall, memory.RecallFilter{},
		)
	}
	run, created, err := submit("project-recall")
	if err != nil || !created {
		t.Fatalf("project recall: run=%+v created=%v err=%v", run, created, err)
	}
	snapshot, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
		Format: run.ContextSnapshot.Format, PolicyVersion: run.ContextSnapshot.PolicyVersion,
		Digest: run.ContextSnapshot.Digest, Payload: run.ContextSnapshot.Payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Materials.Memory) != 1 || snapshot.Materials.Memory[0].Source.ID != projectEntry.ID {
		t.Fatalf("project-visible memory: %+v", snapshot.Materials.Memory)
	}
	replayed, created, err := submit("project-recall")
	if err != nil || created || replayed.ID != run.ID {
		t.Fatalf("project recall replay: run=%+v created=%v err=%v", replayed, created, err)
	}
	status.Store(http.StatusForbidden)
	for _, key := range []string{"project-recall", "new-request"} {
		if denied, created, err := submit(
			key,
		); !errors.Is(err, memory.ErrForbidden) || denied != nil ||
			created {
			t.Fatalf("revoked grant for %q: run=%+v created=%v err=%v", key, denied, created, err)
		}
	}
	status.Store(http.StatusServiceUnavailable)
	if denied, created, err := submit(
		"project-recall",
	); !errors.Is(err, memory.ErrUnavailable) || denied != nil ||
		created {
		t.Fatalf("Forge unavailable: run=%+v created=%v err=%v", denied, created, err)
	}
	spec.Scope.ActorID = "other"
	if denied, created, err := submit(
		"project-recall",
	); !errors.Is(err, memory.ErrForbidden) || denied != nil ||
		created {
		t.Fatalf("different actor: run=%+v created=%v err=%v", denied, created, err)
	}
}

func (recaller *submissionRecaller) RecallTx(
	ctx context.Context,
	_ *gorm.DB,
	scope memory.Scope,
	_ memory.OutputAudience,
	_ string,
	query string,
	filter memory.RecallFilter,
) ([]memory.Entry, error) {
	request, verified := access.FromContext(ctx)
	recaller.readGranted = verified && access.HasProjectRead(ctx, scope.Owner, request)
	recaller.query = query
	recaller.filter = filter
	recaller.calls++
	return nil, nil
}

func TestProjectChatRecallChecksSessionAndCurrentGrant(t *testing.T) {
	const tenant, project, owner = "tenant", "project", "owner"
	store := durableTestStore(t)
	artifactServer, _ := newFakeArtifact(t)
	artifact := newArtifactClient(t, artifactServer)
	agents := appagent.NewService(store.DB(), artifact)
	scope := access.Scope{TenantID: tenant, ProjectID: project}
	ctx := access.WithScope(context.Background(), scope)
	identity := client.Identity{TenantID: tenant, ProjectID: &scope.ProjectID, PropagatedUserID: owner}
	createdAgent, err := agents.CreateAgent(ctx, owner, "recall", agent.AgentConfig{
		Model: agent.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"}, MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agents.PublishAgent(ctx, identity, owner, createdAgent.ID, createdAgent.Revision); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, owner, createdAgent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	projectReadAllowed := true
	recaller := &submissionRecaller{}
	submissions := appexec.NewService(store, artifact).WithMemoryRecaller(recaller).
		WithProjectReadCheck(func(context.Context, string, access.Scope) (bool, error) {
			return projectReadAllowed, nil
		})
	submit := func(ctx context.Context, key string) (*execution.Run, bool, error) {
		return submissions.SubmitChat(
			ctx, identity, owner, session.ID, key, "editor",
			time.Hour, 3, memory.SelectionRecall, memory.RecallFilter{},
		)
	}
	first, created, err := submit(ctx, "recall")
	if err != nil || !created {
		t.Fatalf("submit: created=%v err=%v", created, err)
	}
	replayed, created, err := submit(ctx, "recall")
	if err != nil || created || replayed.ID != first.ID {
		t.Fatalf("replay: run=%+v created=%v err=%v", replayed, created, err)
	}
	if _, _, err := submit(context.Background(), "recall"); !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("replay without a verified scope: %v", err)
	}
	for _, projectID := range []string{"", "other-project"} {
		other := access.WithScope(ctx, access.Scope{TenantID: tenant, ProjectID: projectID})
		if _, _, err := submit(other, "recall"); !errors.Is(err, memory.ErrForbidden) {
			t.Fatalf("replay from project %q: %v", projectID, err)
		}
	}
	projectReadAllowed = false
	for _, key := range []string{"recall", "new-recall"} {
		if _, _, err := submit(ctx, key); !errors.Is(err, memory.ErrForbidden) {
			t.Fatalf("submit after revocation for key %q: %v", key, err)
		}
	}
	if recaller.calls != 1 {
		t.Fatalf("recall calls=%d, want 1", recaller.calls)
	}
	if !recaller.readGranted {
		t.Fatalf("recall did not receive the project read grant: %+v", recaller)
	}
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

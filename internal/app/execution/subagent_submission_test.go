package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/workspace"
)

type subAgentArtifactStore struct {
	tenantArtifactStore
	versions map[string][]byte
}

func (store *subAgentArtifactStore) GetPayload(
	_ context.Context,
	_ client.Identity,
	_, version string,
) ([]byte, error) {
	payload, ok := store.versions[version]
	if !ok {
		return nil, agent.ErrNotFound
	}
	return payload, nil
}

func createSubAgentFixture(
	t *testing.T,
	store *durable.Store,
	artifact *subAgentArtifactStore,
	id, tenant, owner string,
	configs ...agent.AgentConfig,
) table.Agent {
	t.Helper()
	row := table.Agent{ID: id, TenantID: tenant, OwnerID: owner, Name: id, Revision: 1}
	if len(configs) > 0 {
		row.Draft = configs[len(configs)-1]
		row.PublishedVersion = int64(len(configs))
		row.PublishedRevision = 1
	}
	if err := store.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for index, config := range configs {
		number := int64(index + 1)
		versionID := fmt.Sprintf("%s-%d", id, number)
		version := table.AgentVersion{
			AgentID:            id,
			Number:             number,
			ArtifactVersionID:  versionID,
			ArtifactVersionURI: "artifact://fixture/" + versionID,
		}
		if err := store.DB().Create(&version).Error; err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		artifact.versions[versionID] = payload
	}
	return row
}

func TestSubmissionFreezesConfiguredSubAgents(t *testing.T) {
	for _, kind := range []string{"chat", "application"} {
		t.Run(kind, func(t *testing.T) {
			store := tenantTestStore(t)
			ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant"})
			identity := client.Identity{TenantID: "tenant"}
			artifact := &subAgentArtifactStore{versions: make(map[string][]byte)}
			parent := agent.AgentConfig{
				Model:    agent.ModelSpec{Name: "parent-model", BaseURL: "https://parent.test"},
				MaxTurns: 5,
			}
			first := agent.AgentConfig{
				Model: agent.ModelSpec{
					Name:    "research-model",
					BaseURL: "https://research.test",
				},
				MaxTurns:     3,
				SystemPrompt: "research v1",
				SubAgents: []agent.SubAgentSpec{
					{Name: "nested", Description: "Nested reference", AgentID: "unresolved"},
				},
				SelfDelegate: true,
			}
			second := agent.AgentConfig{
				Model: agent.ModelSpec{
					Name:    "review-model",
					BaseURL: "https://review.test",
				},
				MaxTurns:     4,
				SystemPrompt: "review v2",
				MCPServers: []agent.MCPServerSpec{
					{
						Name:          "review",
						URL:           "https://mcp.test",
						DefaultPolicy: agent.ReadOnly,
						AllowedTools: []string{
							"check",
						},
						ToolPolicies: map[string]agent.Policy{"check": agent.Idempotent},
					},
				},
			}
			createSubAgentFixture(t, store, artifact, "research", "tenant", "owner", first, parent)
			createSubAgentFixture(t, store, artifact, "review", "tenant", "owner", parent, second)
			parent.SubAgents = []agent.SubAgentSpec{
				{Name: "research", Description: "Research references", AgentID: "research", Version: 1},
				{Name: "review", Description: "Review conclusions", AgentID: "review"},
			}
			parent.SelfDelegate = true
			createSubAgentFixture(t, store, artifact, "parent", "tenant", "owner", parent)
			catalog := execpkg.PricingCatalog{Version: "prices", Models: []execpkg.ModelPrice{
				{Name: parent.Model.Name, InputPerMillion: 1, OutputPerMillion: 2},
				{Name: first.Model.Name, InputPerMillion: 3, OutputPerMillion: 5},
				{Name: second.Model.Name, InputPerMillion: 7, OutputPerMillion: 9},
			}}
			svc := NewService(store, artifact).WithPricing(catalog)
			submit := func() (*execpkg.Run, bool, error) {
				return svc.SubmitApplication(ctx, identity, "owner", "key", execpkg.ExecutionSpec{
					AgentID:      "parent",
					AgentVersion: 1,
					Query:        "query",
					Scope: &workspace.Scope{
						TenantID: "tenant",
						ActorID:  "owner",
						TaskID:   "task",
						RoundID:  "round",
					},
				}, time.Minute, memory.SelectionOff, memory.RecallFilter{})
			}
			if kind == "chat" {
				session, err := store.CreateSession(ctx, "owner", "parent", "")
				if err != nil {
					t.Fatal(err)
				}
				submit = func() (*execpkg.Run, bool, error) {
					return svc.SubmitChat(
						ctx,
						identity,
						"owner",
						session.ID,
						"key",
						"query", nil,
						time.Minute,
						3,
						memory.SelectionOff,
						memory.RecallFilter{},
					)
				}
			}
			run, created, err := submit()
			if err != nil || !created || run == nil || !run.Spec.SelfDelegate ||
				len(run.Spec.SubAgents) != 2 {
				t.Fatalf("submitted run = %+v, created = %v, error = %v", run, created, err)
			}
			for index, config := range []agent.AgentConfig{first, second} {
				sub := run.Spec.SubAgents[index]
				reference := parent.SubAgents[index]
				price, err := catalog.Freeze(config.Model)
				if err != nil {
					t.Fatal(err)
				}
				if sub.Name != reference.Name || sub.Description != reference.Description {
					t.Fatalf("frozen sub-agent reference %d = %+v", index, sub)
				}
				if sub.AgentID != reference.AgentID || sub.ConfigVersion != fmt.Sprint(index+1) {
					t.Fatalf("frozen sub-agent version %d = %+v", index, sub)
				}
				if sub.SystemPrompt != config.SystemPrompt || sub.MaxTurns != config.MaxTurns {
					t.Fatalf("frozen sub-agent %d = %+v", index, sub)
				}
				if !reflect.DeepEqual(sub.Model, config.Model) {
					t.Fatalf("frozen sub-agent model %d = %+v", index, sub.Model)
				}
				if !reflect.DeepEqual(sub.MCPServers, config.MCPServers) {
					t.Fatalf("frozen sub-agent MCP servers %d = %+v", index, sub.MCPServers)
				}
				if !sub.PricingFrozen || !reflect.DeepEqual(sub.PricingSnapshot, price) {
					t.Fatalf(
						"frozen sub-agent pricing %d = %+v, want %+v",
						index,
						sub.PricingSnapshot,
						price,
					)
				}
			}
			clear(artifact.versions)
			replayed, created, err := submit()
			if err != nil || created || replayed == nil || replayed.ID != run.ID ||
				!reflect.DeepEqual(replayed.Spec, run.Spec) {
				t.Fatalf("replayed run = %+v, created = %v, error = %v", replayed, created, err)
			}
		})
	}
}

func TestSubmissionChecksSubAgentReferencesAndFilesystem(t *testing.T) {
	store := tenantTestStore(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant"})
	identity := client.Identity{TenantID: "tenant"}
	artifact := &subAgentArtifactStore{versions: make(map[string][]byte)}
	svc := NewService(store, artifact)
	config := agent.AgentConfig{Model: agent.ModelSpec{Name: "model", BaseURL: "https://model.test"}, MaxTurns: 5}
	filesystemConfig := config
	filesystemConfig.Toolsets = []string{"filesystem"}
	historicalInvalid := config
	historicalInvalid.MaxTurns = 0
	createSubAgentFixture(t, store, artifact, "ready", "tenant", "owner", config)
	createSubAgentFixture(t, store, artifact, "unpublished", "tenant", "owner")
	createSubAgentFixture(t, store, artifact, "other-owner", "tenant", "another-owner", config)
	createSubAgentFixture(t, store, artifact, "other-tenant", "another-tenant", "owner", config)
	createSubAgentFixture(t, store, artifact, "filesystem", "tenant", "owner", filesystemConfig)
	createSubAgentFixture(t, store, artifact, "invalid", "tenant", "owner", historicalInvalid)
	for _, scenario := range []struct {
		name, child     string
		version         int64
		filesystem      bool
		chatErr, appErr error
	}{
		{name: "unpublished", child: "unpublished", chatErr: agent.ErrConflict, appErr: agent.ErrConflict},
		{name: "missing", child: "missing", chatErr: agent.ErrNotFound, appErr: agent.ErrNotFound},
		{name: "other owner", child: "other-owner", chatErr: agent.ErrNotFound, appErr: agent.ErrNotFound},
		{name: "other tenant", child: "other-tenant", chatErr: agent.ErrNotFound, appErr: agent.ErrNotFound},
		{name: "missing version", child: "ready", version: 2, chatErr: agent.ErrNotFound, appErr: agent.ErrNotFound},
		{name: "invalid historical version", child: "invalid", chatErr: agent.ErrInvalid, appErr: agent.ErrInvalid},
		{name: "self reference", child: "self", chatErr: agent.ErrInvalid, appErr: agent.ErrInvalid},
		{name: "child requires filesystem", child: "filesystem", chatErr: execpkg.ErrInvalid, appErr: execpkg.ErrInvalid},
		{name: "child only filesystem", child: "filesystem", filesystem: true, chatErr: execpkg.ErrInvalid},
		{name: "unneeded filesystem", child: "ready", filesystem: true, appErr: execpkg.ErrInvalid},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			parentID := durable.NewID()
			childID := scenario.child
			if childID == "self" {
				childID = parentID
			}
			parent := config
			parent.SubAgents = []agent.SubAgentSpec{
				{
					Name:        "specialist",
					Description: "Do the work",
					AgentID:     childID,
					Version:     scenario.version,
				},
			}
			createSubAgentFixture(t, store, artifact, parentID, "tenant", "owner", parent)
			session, err := store.CreateSession(ctx, "owner", parentID, "")
			if err != nil {
				t.Fatal(err)
			}
			chat, created, err := svc.SubmitChat(
				ctx,
				identity,
				"owner",
				session.ID,
				durable.NewID(),
				"query", nil,
				time.Minute,
				3,
				memory.SelectionOff,
				memory.RecallFilter{},
			)
			assertSubAgentSubmission(t, chat, created, err, scenario.chatErr)
			input := execpkg.ExecutionSpec{
				AgentID:      parentID,
				AgentVersion: 1,
				Query:        "query",
				Scope: &workspace.Scope{
					TenantID: "tenant",
					ActorID:  "owner",
					TaskID:   parentID,
					RoundID:  "round",
				},
			}
			if scenario.filesystem {
				input.Filesystem = &execpkg.FilesystemBinding{
					Provider:  "Sandbox",
					URL:       "https://sandbox.test",
					TokenEnv:  "TOKEN",
					Workspace: "/workspace",
				}
			}
			app, created, err := svc.SubmitApplication(
				ctx,
				identity,
				"owner",
				durable.NewID(),
				input,
				time.Minute,
				memory.SelectionOff,
				memory.RecallFilter{},
			)
			assertSubAgentSubmission(t, app, created, err, scenario.appErr)
			if app != nil && (len(app.Spec.Toolsets) != 0 || len(app.Spec.SubAgents) != 1 ||
				!reflect.DeepEqual(app.Spec.SubAgents[0].Toolsets, filesystemConfig.Toolsets) ||
				!reflect.DeepEqual(app.Spec.Filesystem, input.Filesystem)) {
				t.Fatalf("child-only filesystem binding = %+v", app.Spec)
			}
		})
	}
}

func assertSubAgentSubmission(t *testing.T, run *execpkg.Run, created bool, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || created != (want == nil) || (run != nil) != (want == nil) {
		t.Fatalf("submission run = %+v, created = %v, error = %v, want %v", run, created, err, want)
	}
}

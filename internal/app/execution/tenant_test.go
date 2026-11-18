package execution

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
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

func TestSubmitApplicationRejectsMismatchedTenantScope(t *testing.T) {
	tests := []struct {
		name    string
		request access.Scope
		spec    *workspace.Scope
	}{
		{name: "missing spec scope", request: access.Scope{TenantID: "tenant-a"}},
		{
			name: "different tenant", request: access.Scope{TenantID: "tenant-a"},
			spec: &workspace.Scope{TenantID: "tenant-b"},
		},
		{
			name: "different project", request: access.Scope{TenantID: "tenant-a", ProjectID: "project-a"},
			spec: &workspace.Scope{TenantID: "tenant-a", ProjectID: "project-b"},
		},
		{
			name:    "missing spec project",
			request: access.Scope{TenantID: "tenant-a", ProjectID: "project-a"},
			spec:    &workspace.Scope{TenantID: "tenant-a"},
		},
		{
			name: "unexpected spec project", request: access.Scope{TenantID: "tenant-a"},
			spec: &workspace.Scope{TenantID: "tenant-a", ProjectID: "project-b"},
		},
		{
			name: "empty request tenant", request: access.Scope{},
			spec: &workspace.Scope{TenantID: "tenant-a"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := access.WithScope(context.Background(), test.request)
			service := NewService(nil, nil)
			run, created, err := service.SubmitApplication(
				ctx,
				client.Identity{TenantID: "tenant-a"},
				"owner",
				"key",
				execpkg.ExecutionSpec{
					AgentID:      "agent",
					AgentVersion: 1,
					Query:        "query",
					Scope:        test.spec,
				},
				time.Minute,
				memory.SelectionOff,
				memory.RecallFilter{},
			)
			if !errors.Is(err, agent.ErrInvalid) || run != nil || created {
				t.Fatalf("scope mismatch: run=%+v created=%v err=%v", run, created, err)
			}
		})
	}
}

func tenantTestStore(t *testing.T) *durable.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := durable.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "test_" + durable.NewID()
	if err := admin.DB().Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.DB().Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error; err != nil {
			t.Error(err)
		}
	})
	scopedDSN, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := scopedDSN.Query()
	query.Set("search_path", schema)
	scopedDSN.RawQuery = query.Encode()
	store, err := durable.Open(ctx, scopedDSN.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

type tenantArtifactStore struct {
	payload []byte
}

func (store *tenantArtifactStore) EnsureCollection(
	_ context.Context,
	_ client.Identity,
	agentID string,
) (string, error) {
	return agentID, nil
}

func (store *tenantArtifactStore) PublishVersion(
	context.Context,
	client.Identity,
	string,
	string,
	[]byte,
) (client.VersionRef, error) {
	return client.VersionRef{}, agent.ErrUnavailable
}

func (store *tenantArtifactStore) GetPayload(context.Context, client.Identity, string, string) ([]byte, error) {
	return store.payload, nil
}

func (store *tenantArtifactStore) Close() error { return nil }

func TestSubmissionTenantRequestKeyIsolation(t *testing.T) {
	for _, kind := range []string{"chat", "application"} {
		t.Run(kind, func(t *testing.T) {
			store := tenantTestStore(t)
			config := agent.AgentConfig{
				Model:    agent.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"},
				MaxTurns: 5,
			}
			payload, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(store, &tenantArtifactStore{payload: payload})
			const owner, key = "shared-owner", "shared-key"
			runs := make(map[string]*execpkg.Run)
			submissions := make(map[string]func() (*execpkg.Run, bool, error))
			for _, tenant := range []string{"tenant-a", "tenant-b"} {
				ctx := access.WithScope(
					context.Background(),
					access.Scope{TenantID: tenant, ProjectID: "project"},
				)
				row := table.Agent{
					ID:                durable.NewID(),
					TenantID:          tenant,
					ProjectID:         "project",
					OwnerID:           owner,
					Name:              "agent",
					Draft:             config,
					Revision:          1,
					PublishedRevision: 1,
					PublishedVersion:  1,
				}
				if err := store.DB().Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				version := table.AgentVersion{
					AgentID:            row.ID,
					Number:             1,
					ArtifactVersionID:  "version-" + row.ID,
					ArtifactVersionURI: "artifact://fixture/" + row.ID,
				}
				if err := store.DB().Create(&version).Error; err != nil {
					t.Fatal(err)
				}
				identity := client.Identity{TenantID: tenant}
				spec := execpkg.ExecutionSpec{
					AgentID: row.ID, AgentVersion: 1, Query: "query",
					Scope: &workspace.Scope{
						TenantID:  tenant,
						ProjectID: "project",
						ActorID:   owner,
						TaskID:    "task",
						RoundID:   "round",
					},
				}
				submit := func() (*execpkg.Run, bool, error) {
					return service.SubmitApplication(
						ctx,
						identity,
						owner,
						key,
						spec,
						time.Minute,
						memory.SelectionOff,
						memory.RecallFilter{},
					)
				}
				if kind == "chat" {
					session, err := store.CreateSession(ctx, owner, row.ID, "")
					if err != nil {
						t.Fatal(err)
					}
					submit = func() (*execpkg.Run, bool, error) {
						return service.SubmitChat(
							ctx,
							identity,
							owner,
							session.ID,
							key,
							"query",
							time.Minute,
							3,
							memory.SelectionOff,
							memory.RecallFilter{},
						)
					}
				}
				run, created, err := submit()
				if err != nil || !created || run == nil {
					t.Fatalf(
						"%s initial submit: run=%+v created=%v err=%v",
						tenant,
						run,
						created,
						err,
					)
				}
				if run.OwnerID != owner || run.AgentID != row.ID {
					t.Fatalf("%s returned another tenant's run: %+v", tenant, run)
				}
				if kind == "application" && (run.Scope == nil || run.Scope.TenantID != tenant) {
					t.Fatalf("%s lost execution scope: %+v", tenant, run.Scope)
				}
				if tenant == "tenant-a" {
					var persisted table.Run
					if err := store.DB().
						Where("id = ?", run.ID).
						Take(&persisted).
						Error; err != nil {
						t.Fatal(err)
					}
					otherCtx := access.WithScope(
						context.Background(),
						access.Scope{TenantID: "tenant-b", ProjectID: "project"},
					)
					foreign, err := service.replay(otherCtx, owner, key, persisted.RequestHash)
					if err != nil || foreign != nil {
						t.Fatalf(
							"matching hash replayed another tenant's run: run=%+v err=%v",
							foreign,
							err,
						)
					}
				}
				runs[tenant], submissions[tenant] = run, submit
			}
			if runs["tenant-a"].ID == runs["tenant-b"].ID ||
				runs["tenant-a"].SessionID == runs["tenant-b"].SessionID {
				t.Fatal("different tenants shared a run or session")
			}
			for _, tenant := range []string{"tenant-a", "tenant-b"} {
				replayed, created, err := submissions[tenant]()
				if err != nil || created || replayed == nil || replayed.ID != runs[tenant].ID {
					t.Fatalf("%s replay: run=%+v created=%v err=%v", tenant, replayed, created, err)
				}
				var persisted table.Run
				if err := store.DB().Where("id = ?", replayed.ID).Take(&persisted).Error; err != nil {
					t.Fatal(err)
				}
				if persisted.TenantID != tenant || persisted.ProjectID != "project" {
					t.Fatalf(
						"%s persisted identity: tenant=%q project=%q",
						tenant,
						persisted.TenantID,
						persisted.ProjectID,
					)
				}
			}
		})
	}
}

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/client"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
)

func TestAgentTenantIsolation(t *testing.T) {
	service, artifact := testService(t)
	const owner = "shared-owner"
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		ctx := access.WithScope(context.Background(), access.Scope{TenantID: tenant, ProjectID: "project"})
		created, err := service.CreateAgent(ctx, owner, "same-name", testConfig(tenant))
		if err != nil {
			t.Fatal(err)
		}
		var row table.Agent
		if err := service.db.Where("id = ?", created.ID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.TenantID != tenant || row.ProjectID != "project" || row.OwnerID != owner {
			t.Fatalf("created agent identity: %+v", row)
		}
		if _, err := service.PublishAgent(
			ctx,
			client.Identity{TenantID: tenant},
			owner,
			created.ID,
			created.Revision,
		); err != nil {
			t.Fatal(err)
		}
	}

	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		t.Run(tenant, func(t *testing.T) {
			ctx := access.WithScope(
				context.Background(),
				access.Scope{TenantID: tenant, ProjectID: "project"},
			)
			listed, err := service.Agents(ctx, owner, "", 100)
			if err != nil || len(listed) != 1 {
				t.Fatalf("tenant list: agents=%+v err=%v", listed, err)
			}
			own := listed[0]
			if own.Draft.SystemPrompt != tenant {
				t.Fatalf("listed another tenant's agent: %+v", own)
			}
			if _, err := service.Agent(ctx, owner, own.ID); err != nil {
				t.Fatalf("own read: %v", err)
			}
			identity := client.Identity{TenantID: tenant}
			if _, err := service.UpdateAgent(
				ctx,
				owner,
				own.ID,
				"updated",
				own.Revision,
				testConfig("updated"),
			); err != nil {
				t.Fatalf("own update: %v", err)
			}
			version, err := service.AgentVersion(ctx, identity, owner, own.ID, 1)
			if err != nil || version.Config.SystemPrompt != tenant {
				t.Fatalf("own immutable version: version=%+v err=%v", version, err)
			}

			var foreign table.Agent
			if err := service.db.Where("tenant_id <> ?", tenant).Take(&foreign).Error; err != nil {
				t.Fatal(err)
			}
			ensures, publishes := artifact.ensures, artifact.publishes
			checks := []struct {
				name string
				call func() error
			}{
				{name: "read", call: func() error {
					_, err := service.Agent(ctx, owner, foreign.ID)
					return err
				}},
				{name: "update", call: func() error {
					_, err := service.UpdateAgent(
						ctx,
						owner,
						foreign.ID,
						"intrusion",
						foreign.Revision,
						testConfig("intrusion"),
					)
					return err
				}},
				{name: "publish", call: func() error {
					_, err := service.PublishAgent(
						ctx,
						identity,
						owner,
						foreign.ID,
						foreign.Revision,
					)
					return err
				}},
				{name: "version", call: func() error {
					_, err := service.AgentVersion(ctx, identity, owner, foreign.ID, 1)
					return err
				}},
			}
			for _, check := range checks {
				t.Run(check.name, func(t *testing.T) {
					if err := check.call(); !errors.Is(err, agent.ErrNotFound) {
						t.Fatalf("cross-tenant operation: want not found, got %v", err)
					}
				})
			}
			var unchanged table.Agent
			if err := service.db.Where("id = ?", foreign.ID).Take(&unchanged).Error; err != nil {
				t.Fatal(err)
			}
			if unchanged.Revision != foreign.Revision || unchanged.Name != foreign.Name ||
				unchanged.Draft.SystemPrompt != foreign.Draft.SystemPrompt ||
				unchanged.PublishedVersion != foreign.PublishedVersion {
				t.Fatalf(
					"cross-tenant operation mutated agent: before=%+v after=%+v",
					foreign,
					unchanged,
				)
			}
			if artifact.ensures != ensures || artifact.publishes != publishes {
				t.Fatal("cross-tenant operation reached artifact publication")
			}
		})
	}

	ctx := access.WithScope(context.Background(), access.Scope{})
	if _, err := service.CreateAgent(
		ctx,
		owner,
		"missing-tenant",
		testConfig("invalid"),
	); !errors.Is(
		err,
		agent.ErrInvalid,
	) {
		t.Fatalf("empty tenant creation: want invalid, got %v", err)
	}
}

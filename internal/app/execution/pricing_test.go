package execution

import (
	"context"
	"encoding/json"
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

func TestSubmissionFreezesPricing(t *testing.T) {
	for _, kind := range []string{"chat", "application"} {
		for _, mode := range []string{"priced", "tokens only", "missing model"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				store := tenantTestStore(t)
				ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant"})
				config := agent.AgentConfig{Model: agent.ModelSpec{Name: "model", BaseURL: "http://model.test/v1"}, MaxTurns: 5}
				payload, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				artifact := &tenantArtifactStore{payload: payload}
				service := NewService(store, artifact)
				catalog := execpkg.PricingCatalog{Version: "prices-v1", Models: []execpkg.ModelPrice{
					{Name: "model", InputPerMillion: 2, OutputPerMillion: 8},
				}}
				switch mode {
				case "tokens only":
					catalog.Models = nil
				case "missing model":
					catalog.Models[0].Name = "another-model"
				}
				service.WithPricing(catalog)
				row := table.Agent{ID: durable.NewID(), TenantID: "tenant", OwnerID: "owner", Name: "agent",
					Draft: config, Revision: 1, PublishedRevision: 1, PublishedVersion: 1}
				if err := store.DB().Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				version := table.AgentVersion{AgentID: row.ID, Number: 1,
					ArtifactVersionID: "version", ArtifactVersionURI: "artifact://fixture/version"}
				if err := store.DB().Create(&version).Error; err != nil {
					t.Fatal(err)
				}
				identity := client.Identity{TenantID: "tenant"}
				spec := execpkg.ExecutionSpec{AgentID: row.ID, AgentVersion: 1, Query: "query",
					Scope: &workspace.Scope{TenantID: "tenant", ActorID: "owner", TaskID: "task", RoundID: "round"}}
				submit := func(key string) (*execpkg.Run, bool, error) {
					return service.SubmitApplication(ctx, identity, "owner", key, spec, time.Minute, memory.SelectionOff, memory.RecallFilter{})
				}
				if kind == "chat" {
					session, err := store.CreateSession(ctx, "owner", row.ID, "")
					if err != nil {
						t.Fatal(err)
					}
					submit = func(key string) (*execpkg.Run, bool, error) {
						return service.SubmitChat(ctx, identity, "owner", session.ID, key, "query", time.Minute, 3, memory.SelectionOff, memory.RecallFilter{})
					}
				}
				run, created, err := submit("key")
				if mode == "missing model" {
					if err == nil || run != nil || created {
						t.Fatalf("missing price accepted: run=%+v created=%v err=%v", run, created, err)
					}
					var count int64
					if err := store.DB().Model(&table.Run{}).Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("rejected submission created runs: count=%d err=%v", count, err)
					}
					return
				}
				if err != nil || !created || run == nil {
					t.Fatalf("submission: run=%+v created=%v err=%v", run, created, err)
				}
				want, err := catalog.Freeze(config.Model)
				if err != nil {
					t.Fatal(err)
				}
				if !run.Spec.PricingFrozen || !reflect.DeepEqual(run.Spec.PricingSnapshot, want) {
					t.Fatalf("run price=%+v frozen=%v want=%+v", run.Spec.PricingSnapshot, run.Spec.PricingFrozen, want)
				}
				service = NewService(store, artifact).WithPricing(execpkg.PricingCatalog{
					Version: "prices-v2", Models: []execpkg.ModelPrice{{Name: "unrelated", InputPerMillion: 99}},
				})
				replayed, created, err := submit("key")
				if err != nil || created || replayed == nil || replayed.ID != run.ID {
					t.Fatalf("replay after catalog change: run=%+v created=%v err=%v", replayed, created, err)
				}
				if !replayed.Spec.PricingFrozen || !reflect.DeepEqual(replayed.Spec.PricingSnapshot, want) {
					t.Fatal("replay repriced accepted run")
				}
				if next, created, err := submit("new-key"); err == nil || created || next != nil {
					t.Fatalf("new request ignored changed catalog: run=%+v created=%v err=%v", next, created, err)
				}
			})
		}
	}
}

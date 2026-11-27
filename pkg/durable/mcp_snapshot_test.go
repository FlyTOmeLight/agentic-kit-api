package durable

import (
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func TestMCPServersFreezeOnSubmissionAndInheritInChild(t *testing.T) {
	for _, kind := range []execution.SubmitKind{execution.SubmitChat, execution.SubmitApplication} {
		t.Run(string(kind), func(t *testing.T) {
			store := testStore(t)
			ctx := t.Context()
			servers := []MCPServerSpec{{
				Name:          "catalog",
				URL:           "https://mcp.test/tools",
				TokenEnv:      "CATALOG_TOKEN",
				DefaultPolicy: Unsafe,
				AllowedTools:  []string{"lookup"},
				ToolPolicies:  map[string]Policy{"lookup": ReadOnly},
				ForwardScope:  true,
			}}
			session, config := testConversation(t, store, Spec{MCPServers: servers})
			var parent *Run
			var created bool
			var err error
			switch kind {
			case execution.SubmitChat:
				parent, created, err = submitFixture(
					t, store, "owner", session.ID, NewID(), "lookup", time.Hour, 3, config,
				)
			case execution.SubmitApplication:
				agentID, version := sessionPublishedFixture(t, store, session.ID)
				parent, created, err = submitExecutionFixture(
					t,
					store,
					"owner",
					NewID(),
					execution.ExecutionSpec{
						AgentID:      agentID,
						AgentVersion: version,
						Query:        "lookup",
						Scope: &Scope{
							TenantID: "tenant",
							ActorID:  "actor",
							TaskID:   "mcp-task",
							RoundID:  "mcp-round",
						},
					},
					time.Hour,
					config,
				)
			}
			if err != nil || !created {
				t.Fatalf("submit %s: created=%v err=%v", kind, created, err)
			}
			if spec := readStoredSpec(t, store, parent.ID); !reflect.DeepEqual(spec.MCPServers, servers) ||
				spec.Filesystem != nil {
				t.Fatalf("parent MCPServers = %+v, filesystem = %+v", spec.MCPServers, spec.Filesystem)
			}

			claimed := claim(t, store, "worker")
			if claimed.ID != parent.ID || !reflect.DeepEqual(claimed.Spec.MCPServers, servers) {
				t.Fatalf("claimed run %s with MCPServers %+v, want run %s with %+v",
					claimed.ID, claimed.Spec.MCPServers, parent.ID, servers)
			}
			intent := StepIntent{ID: "spawn-mcp", Input: "child lookup", Policy: Idempotent}
			if _, err := store.BeginStep(ctx, claimed.Lease(), intent); err != nil {
				t.Fatal(err)
			}
			child, err := store.SpawnChildRun(ctx, claimed.Lease(), intent.ID, intent.Input, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if spec := readStoredSpec(t, store, child.ID); !reflect.DeepEqual(spec.MCPServers, servers) ||
				spec.Filesystem != nil || spec.Query != intent.Input {
				t.Fatalf("child MCPServers = %+v, filesystem = %+v, query = %q",
					spec.MCPServers, spec.Filesystem, spec.Query)
			}
		})
	}
}

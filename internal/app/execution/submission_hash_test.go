package execution

import (
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	execpkg "github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestSubmissionRequestHash(t *testing.T) {
	spec := execpkg.ExecutionSpec{AgentID: "agent", AgentVersion: 1, Query: "query"}
	paths := []struct {
		name       string
		hash       func(memory.Selection, memory.RecallFilter) string
		legacyJSON string
		recallJSON string
	}{
		{
			name: "chat",
			hash: func(selection memory.Selection, filter memory.RecallFilter) string {
				return chatRequestHash("session", "query", selection, filter)
			},
			legacyJSON: `{"Session":"session","Query":"query"}`,
			recallJSON: `{"Session":"session","Query":"query","Memory":"Recall","Filter":{}}`,
		},
		{
			name: "application",
			hash: func(selection memory.Selection, filter memory.RecallFilter) string {
				return applicationRequestHash(spec, time.Minute, selection, filter)
			},
			legacyJSON: `{"Spec":{"scope":null,"agentID":"agent","agentVersion":1,"query":"query"},"TTL":60000000000}`,
			recallJSON: `{"Spec":{"scope":null,"agentID":"agent","agentVersion":1,"query":"query"},"TTL":60000000000,"Memory":"Recall","Filter":{}}`,
		},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			legacyHash := durable.Hash([]byte(path.legacyJSON))
			for _, selection := range []memory.Selection{"", memory.SelectionOff} {
				for _, filter := range []memory.RecallFilter{
					{},
					{Terms: []string{}, Tags: []string{}},
				} {
					if got := path.hash(selection, filter); got != legacyHash {
						t.Fatalf("selection %q filter %+v: hash = %s, want legacy %s", selection, filter, got, legacyHash)
					}
				}
			}
			recallHash := path.hash(memory.SelectionRecall, memory.RecallFilter{})
			if recallHash == legacyHash {
				t.Fatal("Recall must differ from Off")
			}
			if want := durable.Hash([]byte(path.recallJSON)); recallHash != want {
				t.Fatalf("Recall hash = %s, want existing %s", recallHash, want)
			}
			filters := []struct {
				name   string
				filter memory.RecallFilter
			}{
				{name: "terms", filter: memory.RecallFilter{Terms: []string{"term"}}},
				{name: "tags", filter: memory.RecallFilter{Tags: []string{"tag"}}},
				{name: "max_entries", filter: memory.RecallFilter{MaxEntries: 1}},
				{name: "max_bytes", filter: memory.RecallFilter{MaxBytes: 1}},
				{name: "unbounded_entries", filter: memory.RecallFilter{MaxEntries: -1}},
				{name: "unbounded_bytes", filter: memory.RecallFilter{MaxBytes: -1}},
			}
			for _, test := range filters {
				t.Run(test.name, func(t *testing.T) {
					if path.hash(memory.SelectionRecall, test.filter) == recallHash {
						t.Fatal("explicit filter must change the Recall hash")
					}
					omittedHash := path.hash("", test.filter)
					offHash := path.hash(memory.SelectionOff, test.filter)
					if omittedHash != offHash {
						t.Fatal("omitted memory and explicit Off must have the same hash")
					}
					if offHash == legacyHash {
						t.Fatal("Off with a filter must not replay an accepted legacy request")
					}
				})
			}
		})
	}
}

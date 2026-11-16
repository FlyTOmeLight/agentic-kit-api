package table

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestMemoryOptionalArchiveReferences(t *testing.T) {
	for _, test := range []struct {
		name string
		data json.RawMessage
	}{
		{name: "nil"},
		{name: "empty", data: json.RawMessage{}},
		{name: "json null", data: json.RawMessage("null")},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := (&MemoryEntry{Binding: test.data, ActiveVersionRef: test.data}).ToDomain()
			operation := (&MemoryOperation{Binding: test.data, VersionRef: test.data}).ToDomain()
			if entry.Binding != nil || entry.ActiveVersionRef != nil ||
				operation.Binding != nil || operation.VersionRef != nil {
				t.Fatal("absent archive reference became a non-nil domain value")
			}
			entryRow := MemoryEntryFromDomain(entry)
			operationRow := MemoryOperationFromDomain(operation)
			if entryRow.Binding != nil || entryRow.ActiveVersionRef != nil ||
				operationRow.Binding != nil || operationRow.VersionRef != nil {
				t.Fatal("absent archive reference must serialize to SQL NULL")
			}
		})
	}
	binding := &memory.ArtifactBinding{
		StoreID: "store", TenantID: "tenant", ProjectID: "project", UserID: "user", ArtifactID: "artifact",
	}
	version := &memory.VersionRef{
		StoreID: "store", TenantID: "tenant", ProjectID: "project", UserID: "user",
		ArtifactID: "artifact", VersionID: "version", Sequence: 7, VersionURI: "artifact://artifact/version",
	}
	entry := MemoryEntryFromDomain(&memory.Entry{Binding: binding, ActiveVersionRef: version}).ToDomain()
	operation := MemoryOperationFromDomain(&memory.Operation{Binding: binding, VersionRef: version}).ToDomain()
	if !reflect.DeepEqual(entry.Binding, binding) || !reflect.DeepEqual(operation.Binding, binding) ||
		!reflect.DeepEqual(
			entry.ActiveVersionRef,
			version,
		) || !reflect.DeepEqual(operation.VersionRef, version) {
		t.Fatal("archive identity changed during table conversion")
	}
}

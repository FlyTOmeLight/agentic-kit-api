package memory

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/agent"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/contextbuilder"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func TestArchivedMemoryFreezesOnlyActivatedContent(t *testing.T) {
	archive := &fakeArchiver{}
	svc, store := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner")
	entry, err := svc.Create(ctx, "owner", "actor", "create",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatal(err)
	}
	config := agent.AgentConfig{
		Model: agent.ModelSpec{Name: "test", BaseURL: "http://model.test/v1"}, MaxTurns: 5,
	}
	record := table.Agent{
		ID: durable.NewID(), TenantID: scope.Tenant, OwnerID: scope.Owner,
		Name: "memory", Draft: config, Revision: 1, PublishedRevision: 1, PublishedVersion: 1,
	}
	if err := store.DB().Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	submit := func() (*durable.Run, contextbuilder.Snapshot) {
		t.Helper()
		session, err := store.CreateSession(ctx, "owner", record.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		run, _, err := store.Accept(ctx, execution.SubmitCommand{
			Owner: "owner", RequestKey: durable.NewID(), RequestHash: durable.Hash([]byte(session.ID)),
			TTL: time.Hour, MaxAttempts: 3, Kind: execution.SubmitChat,
			SessionID: session.ID, Query: "editor", Memory: memory.SelectionRecall,
		}, durable.PrepareChatSubmission(config, record.ID, record.PublishedVersion, svc))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := contextbuilder.DecodeSnapshot(contextbuilder.Envelope{
			Format: run.ContextSnapshot.Format, PolicyVersion: run.ContextSnapshot.PolicyVersion,
			Digest: run.ContextSnapshot.Digest, Payload: run.ContextSnapshot.Payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		return run, snapshot
	}
	assertMemory := func(snapshot contextbuilder.Snapshot, expected *memory.Entry, text string) {
		t.Helper()
		if snapshot.MemoryMode != contextbuilder.MemoryRecall || len(snapshot.Materials.Memory) != 1 {
			t.Fatalf("unexpected memory snapshot: %+v", snapshot)
		}
		block := snapshot.Materials.Memory[0]
		if block.Source.ID != expected.ID || block.Source.Revision != expected.Revision ||
			block.Source.Version != expected.ActiveVersionRef.VersionURI || !strings.Contains(block.Content, text) {
			t.Fatalf("unexpected frozen content/reference: %+v", block)
		}
	}

	archive.failPublish = memory.ErrUnavailable
	update := factDocument(scope, "editor", "editor preference", "use spaces")
	if _, err := svc.Update(
		ctx,
		"owner",
		"actor",
		"update",
		entry.ID,
		1,
		update,
	); !errors.Is(
		err,
		memory.ErrUnavailable,
	) {
		t.Fatalf("failed publish = %v", err)
	}
	frozenRun, snapshot := submit()
	assertMemory(snapshot, entry, "use tabs")

	archive.failPublish = nil
	updated, err := svc.Update(ctx, "owner", "actor", "update", entry.ID, 1, update)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot = submit()
	assertMemory(snapshot, updated, "use spaces")
	if _, err := svc.Retire(ctx, "owner", "actor", "retire", entry.ID, 2); err != nil {
		t.Fatal(err)
	}
	_, snapshot = submit()
	if snapshot.MemoryMode != contextbuilder.MemoryEmpty || len(snapshot.Materials.Memory) != 0 {
		t.Fatalf("retired memory was recalled: %+v", snapshot)
	}
	reloaded, err := store.Get(ctx, "owner", frozenRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ContextSnapshot.Digest != frozenRun.ContextSnapshot.Digest ||
		!bytes.Equal(reloaded.ContextSnapshot.Payload, frozenRun.ContextSnapshot.Payload) {
		t.Fatal("memory update/retire changed the accepted Run snapshot")
	}
}

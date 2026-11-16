package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func testService(t *testing.T) (*Service, *durable.Store) {
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
	schema := "test_" + durable.NewID()
	if err = admin.DB().WithContext(ctx).Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, err := durable.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		if err := admin.DB().WithContext(ctx).Exec(`DROP SCHEMA ` + schema + ` CASCADE`).Error; err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err = store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return NewService(store.DB()), store
}

func factDocument(scope memory.Scope, subject, summary, body string) memory.Document {
	data, _ := json.Marshal(body)
	return memory.Document{
		Scope:      scope,
		Kind:       memory.KindFact,
		SubjectKey: subject,
		Summary:    summary,
		Body:       json.RawMessage(data),
		Source:     memory.Source{Class: memory.SourceHumanConfirmed},
	}
}

func TestMemoryCreateGetListUpdateRetireRestore(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}

	entry, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.Revision != 1 || entry.ID == "" {
		t.Fatalf("unexpected entry: %+v", entry)
	}

	got, err := svc.Get(ctx, "owner-a", entry.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Summary != "editor preference" {
		t.Fatalf("Get summary = %q", got.Summary)
	}
	if _, err = svc.Get(ctx, "owner-b", entry.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("cross-owner Get error = %v, want ErrNotFound", err)
	}

	entries, err := svc.List(ctx, "owner-a", "", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List len = %d", len(entries))
	}

	updated, err := svc.Update(ctx, "owner-a", "actor-a", "key-2", entry.ID, 1,
		factDocument(scope, "editor", "editor preference v2", "use spaces"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 || updated.Summary != "editor preference v2" {
		t.Fatalf("unexpected updated entry: %+v", updated)
	}

	if _, err = svc.Update(ctx, "owner-a", "actor-a", "key-3", entry.ID, 1,
		factDocument(scope, "editor", "stale", "x")); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("stale Update error = %v, want ErrConflict", err)
	}

	retired, err := svc.Retire(ctx, "owner-a", "actor-a", "key-4", entry.ID, 2)
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if retired.RetiredAt == nil {
		t.Fatal("Retire did not set RetiredAt")
	}

	if _, err = svc.Update(ctx, "owner-a", "actor-a", "key-5", entry.ID, 3,
		factDocument(scope, "editor", "x", "y")); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("Update retired error = %v, want ErrConflict", err)
	}

	restored, err := svc.Restore(ctx, "owner-a", "actor-a", "key-6", entry.ID, 3,
		factDocument(scope, "editor", "editor preference v3", "use spaces 4"))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.RetiredAt != nil || restored.Revision != 4 {
		t.Fatalf("unexpected restored entry: %+v", restored)
	}
}

func TestMemorySubjectUniqueIndex(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}
	doc := factDocument(scope, "editor", "one", "a")

	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-2", doc); err == nil {
		t.Fatal("second Create with same subject succeeded, want unique violation")
	}

	other := factDocument(scope, "shell", "two", "b")
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-3", other); err != nil {
		t.Fatalf("different subject Create: %v", err)
	}
}

func TestMemoryRecallTx(t *testing.T) {
	svc, store := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}

	plain, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatalf("Create plain: %v", err)
	}
	agentDoc := factDocument(scope, "review", "review procedure", "run go vet")
	agentDoc.Kind = memory.KindProcedure
	agentDoc.AgentID = "agent-1"
	agentScoped, err := svc.Create(ctx, "owner-a", "actor-a", "key-2", agentDoc)
	if err != nil {
		t.Fatalf("Create agent-scoped: %v", err)
	}
	otherAgentDoc := factDocument(scope, "lint", "lint rule", "run golangci")
	otherAgentDoc.AgentID = "agent-2"
	if _, err = svc.Create(ctx, "owner-a", "actor-a", "key-3", otherAgentDoc); err != nil {
		t.Fatalf("Create other-agent: %v", err)
	}

	err = store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entries, err := svc.RecallTx(ctx, tx, scope, "agent-1", "", memory.RecallFilter{})
		if err != nil {
			return err
		}
		if len(entries) != 2 {
			t.Fatalf("RecallTx agent-1 len = %d, want 2 (plain + agent-1)", len(entries))
		}
		ids := map[string]bool{entries[0].ID: true, entries[1].ID: true}
		if !ids[plain.ID] || !ids[agentScoped.ID] {
			t.Fatalf("RecallTx returned wrong entries: %+v", ids)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RecallTx tx: %v", err)
	}

	if _, err = svc.Retire(ctx, "owner-a", "actor-a", "key-4", plain.ID, 1); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	err = store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entries, err := svc.RecallTx(ctx, tx, scope, "agent-1", "", memory.RecallFilter{})
		if err != nil {
			return err
		}
		if len(entries) != 1 || entries[0].ID != agentScoped.ID {
			t.Fatalf("RecallTx after retire: %+v", entries)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RecallTx after retire: %v", err)
	}

	err = store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entries, err := svc.RecallTx(
			ctx,
			tx,
			memory.Scope{
				Kind:  memory.ScopeProjectShared,
				Owner: "owner-a",
			},
			"agent-1",
			"",
			memory.RecallFilter{},
		)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			t.Fatalf("cross-scope RecallTx len = %d", len(entries))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-scope RecallTx: %v", err)
	}
}

func TestMemoryRecallTxExpiredSkipped(t *testing.T) {
	svc, store := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}

	past := int64(1)
	doc := factDocument(scope, "temp", "temporary", "expires")
	doc.ValidUntil = &past
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc); err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	err := store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entries, err := svc.RecallTx(ctx, tx, scope, "", "", memory.RecallFilter{})
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			t.Fatalf("expired RecallTx len = %d", len(entries))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expired RecallTx: %v", err)
	}
}

func recallInTx(
	t *testing.T,
	svc *Service,
	store *durable.Store,
	scope memory.Scope,
	agentID string,
	query string,
	filter memory.RecallFilter,
) []memory.Entry {
	t.Helper()
	var entries []memory.Entry
	err := store.DB().WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		var err error
		entries, err = svc.RecallTx(context.Background(), tx, scope, agentID, query, filter)
		return err
	})
	if err != nil {
		t.Fatalf("RecallTx: %v", err)
	}
	return entries
}

func TestMemoryRecallTxTermMatching(t *testing.T) {
	svc, store := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}

	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs")); err != nil {
		t.Fatalf("Create editor: %v", err)
	}
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-2",
		factDocument(scope, "shell", "shell preference", "use zsh")); err != nil {
		t.Fatalf("Create shell: %v", err)
	}

	entries := recallInTx(t, svc, store, scope, "", "editor", memory.RecallFilter{})
	if len(entries) != 1 || entries[0].SubjectKey != "editor" {
		t.Fatalf("term recall: %+v", entries)
	}

	entries = recallInTx(t, svc, store, scope, "", "编辑器", memory.RecallFilter{})
	if len(entries) != 0 {
		t.Fatalf("unrelated Han query len = %d", len(entries))
	}

	entries = recallInTx(t, svc, store, scope, "", "", memory.RecallFilter{})
	if len(entries) != 2 {
		t.Fatalf("empty query len = %d, want all active", len(entries))
	}
}

func TestMemoryRecallTxTagFilter(t *testing.T) {
	svc, store := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}

	goDoc := factDocument(scope, "vet", "vet procedure", "run go vet")
	goDoc.Conditions.TaskTags = []string{"go"}
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", goDoc); err != nil {
		t.Fatalf("Create go: %v", err)
	}
	pyDoc := factDocument(scope, "lint", "lint procedure", "run ruff")
	pyDoc.Conditions.TaskTags = []string{"python"}
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-2", pyDoc); err != nil {
		t.Fatalf("Create python: %v", err)
	}

	entries := recallInTx(t, svc, store, scope, "", "", memory.RecallFilter{Tags: []string{"GO"}})
	if len(entries) != 1 || entries[0].SubjectKey != "vet" {
		t.Fatalf("tag recall: %+v", entries)
	}
}

func TestMemoryRecallTxBoundsAndOrdering(t *testing.T) {
	svc, store := testService(t)
	ctx := context.Background()
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a"}

	first, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "one", "first", "aaaa"))
	if err != nil {
		t.Fatalf("Create one: %v", err)
	}
	second, err := svc.Create(ctx, "owner-a", "actor-a", "key-2",
		factDocument(scope, "two", "second", "bbbb"))
	if err != nil {
		t.Fatalf("Create two: %v", err)
	}
	updated, err := svc.Update(ctx, "owner-a", "actor-a", "key-3", first.ID, 1,
		factDocument(scope, "one", "first revised", "aaaa"))
	if err != nil {
		t.Fatalf("Update one: %v", err)
	}

	entries := recallInTx(t, svc, store, scope, "", "", memory.RecallFilter{})
	if len(entries) != 2 || entries[0].ID != updated.ID || entries[1].ID != second.ID {
		t.Fatalf("stable ordering by revision desc: %+v", entries)
	}

	entries = recallInTx(t, svc, store, scope, "", "", memory.RecallFilter{MaxEntries: 1})
	if len(entries) != 1 || entries[0].ID != updated.ID {
		t.Fatalf("MaxEntries=1: %+v", entries)
	}

	entries = recallInTx(t, svc, store, scope, "", "",
		memory.RecallFilter{MaxBytes: updated.MaterialBytes()})
	if len(entries) != 1 || entries[0].ID != updated.ID {
		t.Fatalf("MaxBytes keeps first, skips overflow: %+v", entries)
	}
}

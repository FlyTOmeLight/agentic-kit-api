package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"testing"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func testService(t *testing.T) (*Service, *durable.Store) {
	return testServiceWithArchive(t, nil)
}

func testServiceWithArchive(t *testing.T, archive Archiver) (*Service, *durable.Store) {
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
	return NewService(store.DB(), archive), store
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

const archiveTestTenant = "tenant-store"

func archiveTestContext() context.Context {
	return access.WithScope(context.Background(), access.Scope{TenantID: archiveTestTenant})
}

func archiveTestScope(owner string) memory.Scope {
	return memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: owner, Tenant: archiveTestTenant}
}

func TestMemoryAPIRequiresTrustedScope(t *testing.T) {
	svc := NewService(nil, nil)
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing", ctx: context.Background()},
		{name: "empty tenant", ctx: access.WithScope(context.Background(), access.Scope{})},
		{name: "blank tenant", ctx: access.WithScope(context.Background(), access.Scope{TenantID: " "})},
		{name: "untrimmed tenant", ctx: access.WithScope(context.Background(), access.Scope{TenantID: " tenant-a"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := factDocument(
				memory.Scope{Kind: memory.ScopeOwnerPrivate, Tenant: "tenant-a"},
				"editor",
				"editor",
				"tabs",
			)
			for name, call := range map[string]func() error{
				"Create": func() error {
					_, err := svc.Create(test.ctx, "owner", "actor", "key", doc)
					return err
				},
				"Get": func() error {
					_, err := svc.Get(test.ctx, "owner", "entry")
					return err
				},
				"List": func() error {
					_, err := svc.List(test.ctx, "owner", "", 10)
					return err
				},
				"Update": func() error {
					_, err := svc.Update(test.ctx, "owner", "actor", "key", "entry", 1, doc)
					return err
				},
				"Retire": func() error {
					_, err := svc.Retire(test.ctx, "owner", "actor", "key", "entry", 1)
					return err
				},
				"Restore": func() error {
					_, err := svc.Restore(test.ctx, "owner", "actor", "key", "entry", 1, doc)
					return err
				},
			} {
				t.Run(name, func(t *testing.T) {
					if err := call(); !errors.Is(err, memory.ErrForbidden) {
						t.Fatalf("error = %v, want ErrForbidden", err)
					}
				})
			}
		})
	}
}

func TestMemoryDocumentMustMatchTrustedScope(t *testing.T) {
	svc := NewService(nil, nil)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a", ProjectID: "project-a"})
	for _, test := range []struct {
		name    string
		tenant  string
		project string
	}{
		{name: "other tenant", tenant: "tenant-b", project: "project-a"},
		{name: "missing tenant", project: "project-a"},
		{name: "other project", tenant: "tenant-a", project: "project-b"},
		{name: "missing project", tenant: "tenant-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := factDocument(memory.Scope{
				Kind:    memory.ScopeOwnerPrivate,
				Owner:   "forged",
				Tenant:  test.tenant,
				Project: test.project,
			}, "editor", "editor", "tabs")
			for name, call := range map[string]func() error{
				"Create": func() error {
					_, err := svc.Create(ctx, "owner", "actor", "key", doc)
					return err
				},
				"Update": func() error {
					_, err := svc.Update(ctx, "owner", "actor", "key", "entry", 1, doc)
					return err
				},
				"Restore": func() error {
					_, err := svc.Restore(ctx, "owner", "actor", "key", "entry", 1, doc)
					return err
				},
			} {
				t.Run(name, func(t *testing.T) {
					if err := call(); !errors.Is(err, memory.ErrForbidden) {
						t.Fatalf("error = %v, want ErrForbidden", err)
					}
				})
			}
		})
	}
}

func TestMemoryCreateGetListUpdateRetireRestore(t *testing.T) {
	svc, store := testService(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}

	entry, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.Revision != 1 || entry.ID == "" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	var localEntries, localReceipts int64
	if err := store.DB().Model(&table.MemoryEntry{}).
		Where("id = ? AND binding IS NULL AND active_version_ref IS NULL", entry.ID).
		Count(&localEntries).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB().Model(&table.MemoryOperation{}).
		Where("entry_id = ? AND binding IS NULL AND version_ref IS NULL", entry.ID).
		Count(&localReceipts).Error; err != nil {
		t.Fatal(err)
	}
	if localEntries != 1 || localReceipts != 1 {
		t.Fatal("local writes must store SQL NULL archive references")
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
	replayed, err := svc.Retire(ctx, "owner-a", "actor-a", "key-4", entry.ID, 2)
	if err != nil || replayed.Revision != 3 {
		t.Fatalf("Retire replay: %+v, %v", replayed, err)
	}
	if _, err = svc.Create(ctx, "owner-a", "actor-a", "retired-subject",
		factDocument(scope, "editor", "duplicate", "x")); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("retired subject Create = %v, want Conflict", err)
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
	persisted, err := svc.Get(ctx, "owner-a", restored.ID)
	if err != nil || persisted.RetiredAt != nil || persisted.Revision != 4 {
		t.Fatalf("persisted restored entry: %+v, err=%v", persisted, err)
	}
	var receipts []table.MemoryOperation
	if err = store.DB().Where("entry_id = ?", entry.ID).Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	var applied, rejected int
	for _, receipt := range receipts {
		if receipt.TenantID != scope.Tenant || receipt.ToDomain().TenantID != scope.Tenant {
			t.Fatalf("receipt lost tenant: %+v", receipt)
		}
		switch memory.OperationStatus(receipt.Status) {
		case memory.OperationApplied:
			applied++
		case memory.OperationRejected:
			rejected++
		default:
			t.Fatalf("unexpected receipt status: %+v", receipt)
		}
	}
	if applied != 4 || rejected != 2 {
		t.Fatalf("receipts = %d applied, %d rejected; want 4 applied, 2 rejected", applied, rejected)
	}
}

func TestMemorySameOwnerTenantIsolation(t *testing.T) {
	svc, store := testService(t)
	svc.WithProjectReadCheck(allowMemoryProject).WithProjectWriteCheck(allowMemoryProject)
	ctxA := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a", ProjectID: "project-a"})
	ctxB := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-b", ProjectID: "project-b"})
	scopeA := memory.Scope{
		Kind:    memory.ScopeOwnerPrivate,
		Owner:   "forged-owner",
		Tenant:  "tenant-a",
		Project: "project-a",
	}
	scopeB := memory.Scope{
		Kind:    memory.ScopeOwnerPrivate,
		Owner:   "forged-owner",
		Tenant:  "tenant-b",
		Project: "project-b",
	}
	docA := factDocument(scopeA, "editor", "editor A", "tabs")
	docB := factDocument(scopeB, "editor", "editor B", "spaces")
	entryA, err := svc.Create(ctxA, "owner", "actor", "shared-key", docA)
	if err != nil {
		t.Fatal(err)
	}
	entryB, err := svc.Create(ctxB, "owner", "actor", "shared-key", docB)
	if err != nil {
		t.Fatalf("same owner/requestKey in another tenant: %v", err)
	}
	if entryA.Scope.Owner != "owner" || entryB.Scope.Owner != "owner" {
		t.Fatal("document owner was trusted")
	}
	var receipts []table.MemoryOperation
	if err = store.DB().
		Where("owner_id = ? AND request_key = ?", "owner", "shared-key").
		Order("tenant_id").
		Find(&receipts).
		Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 || receipts[0].TenantID != "tenant-a" || receipts[1].TenantID != "tenant-b" {
		t.Fatalf("tenant receipts: %+v", receipts)
	}
	var savedDoc memory.Document
	if err = json.Unmarshal(receipts[0].Document, &savedDoc); err != nil || savedDoc.Scope.Owner != "owner" {
		t.Fatalf("receipt owner: %+v, err=%v", savedDoc.Scope, err)
	}
	for _, test := range []struct {
		name  string
		ctx   context.Context
		own   *memory.Entry
		other *memory.Entry
		doc   memory.Document
	}{
		{name: "tenant-a", ctx: ctxA, own: entryA, other: entryB, doc: docA},
		{name: "tenant-b", ctx: ctxB, own: entryB, other: entryA, doc: docB},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries, err := svc.List(test.ctx, "owner", "", 10)
			if err != nil || len(entries) != 1 || entries[0].ID != test.own.ID {
				t.Fatalf("List = %+v, err=%v", entries, err)
			}
			if _, err = svc.Get(test.ctx, "owner", test.other.ID); !errors.Is(err, memory.ErrNotFound) {
				t.Fatalf("cross-tenant Get = %v", err)
			}
			if _, err = svc.Update(
				test.ctx,
				"owner",
				"actor",
				"cross-update",
				test.other.ID,
				1,
				test.doc,
			); !errors.Is(
				err,
				memory.ErrNotFound,
			) {
				t.Fatalf("cross-tenant Update = %v", err)
			}
			if _, err = svc.Retire(
				test.ctx,
				"owner",
				"actor",
				"cross-retire",
				test.other.ID,
				1,
			); !errors.Is(
				err,
				memory.ErrNotFound,
			) {
				t.Fatalf("cross-tenant Retire = %v", err)
			}
			if _, err = svc.Restore(
				test.ctx,
				"owner",
				"actor",
				"cross-restore",
				test.other.ID,
				1,
				test.doc,
			); !errors.Is(
				err,
				memory.ErrNotFound,
			) {
				t.Fatalf("cross-tenant Restore = %v", err)
			}
		})
	}
	duplicateDoc := factDocument(scopeA, "different-subject", "duplicate receipt", "data")
	if _, err = svc.Create(ctxA, "owner", "actor", "shared-key", duplicateDoc); err == nil {
		t.Fatal("same tenant/owner/requestKey accepted a second receipt")
	}
	entries, err := svc.List(ctxA, "owner", "", 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("duplicate receipt did not roll back entry: %+v, err=%v", entries, err)
	}
	updated, err := svc.Update(ctxA, "owner", "actor", "update", entryA.ID, 1, docA)
	if err != nil || updated.Scope.Owner != "owner" {
		t.Fatalf("Update owner: %+v, err=%v", updated, err)
	}
	retired, err := svc.Retire(ctxA, "owner", "actor", "retire", entryA.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := svc.Restore(ctxA, "owner", "actor", "restore", entryA.ID, retired.Revision, docA)
	if err != nil || restored.Scope.Owner != "owner" {
		t.Fatalf("Restore owner: %+v, err=%v", restored, err)
	}
	for _, test := range []struct {
		ctx   context.Context
		scope memory.Scope
		id    string
	}{
		{ctx: ctxA, scope: entryA.Scope, id: entryA.ID},
		{ctx: ctxB, scope: entryB.Scope, id: entryB.ID},
	} {
		project, _ := access.FromContext(test.ctx)
		granted := access.WithProjectRead(test.ctx, "owner", project)
		entries := recallInTx(t, granted, svc, store, test.scope, "", "", memory.RecallFilter{})
		if len(entries) != 1 || entries[0].ID != test.id {
			t.Fatalf("worker RecallTx scope %+v: %+v", test.scope, entries)
		}
	}
}

func TestResumePendingUsesReceiptTenantAndFixedBinding(t *testing.T) {
	archive := &fakeArchiver{failPublish: memory.ErrUnavailable}
	svc, store := testServiceWithArchive(t, archive)
	svc.WithProjectReadCheck(allowMemoryProject).WithProjectWriteCheck(allowMemoryProject)
	jobCtx := context.Background()
	var requestContexts []context.Context
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		ctx := access.WithScope(jobCtx, access.Scope{TenantID: tenant, ProjectID: "project"})
		requestContexts = append(requestContexts, ctx)
		doc := factDocument(memory.Scope{
			Kind: memory.ScopeOwnerPrivate, Tenant: tenant, Project: "project",
		}, "editor", "editor", "tabs")
		if _, err := svc.Create(
			ctx,
			"owner",
			"actor",
			"same-key",
			doc,
		); !errors.Is(
			err,
			memory.ErrUnavailable,
		) {
			t.Fatalf("Create in %s = %v, want ErrUnavailable", tenant, err)
		}
	}

	legacyDoc := factDocument(
		memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"},
		"editor",
		"editor",
		"tabs",
	)
	legacyDoc.SchemaVersion = schemaVersion
	legacyDoc.EntryID = newEntryID()
	legacyOp := operationFromDocument(&legacyDoc, "owner", "actor", "same-key", memory.OperationCreate, 0)
	legacyOp.Binding = &memory.ArtifactBinding{
		StoreID: "artifact-test", TenantID: "legacy-storage", UserID: "owner", ArtifactID: "legacy-artifact",
	}
	if err := store.DB().Create(table.MemoryOperationFromDomain(legacyOp)).Error; err != nil {
		t.Fatal(err)
	}

	archive.failPublish = nil
	if resumed, err := svc.ResumePending(jobCtx, 10); err != nil || resumed != 3 {
		t.Fatalf("ResumePending = %d, %v, want three applied receipts", resumed, err)
	}
	if got := archive.bindings.Load(); got != 2 {
		t.Fatalf("ResolveBinding calls = %d, want only the original requests", got)
	}
	for _, ctx := range requestContexts {
		scope, _ := access.FromContext(ctx)
		entries, err := svc.List(ctx, "owner", "", 10)
		if err != nil || len(entries) != 1 {
			t.Fatalf("List in %s = %+v, %v", scope.TenantID, entries, err)
		}
		entry := entries[0]
		if entry.Scope.Tenant != scope.TenantID || entry.ActiveVersionRef == nil ||
			entry.ActiveVersionRef.TenantID != scope.TenantID || entry.ActiveVersionRef.ProjectID != scope.ProjectID {
			t.Fatalf("entry in %s = %+v", scope.TenantID, entry)
		}
	}
	var legacyRow table.MemoryEntry
	if err := store.DB().Take(&legacyRow, "id = ?", legacyDoc.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	legacy := legacyRow.ToDomain()
	if legacy.Scope.Tenant != "" || legacy.Binding == nil || *legacy.Binding != *legacyOp.Binding ||
		legacy.ActiveVersionRef == nil || legacy.ActiveVersionRef.TenantID != "legacy-storage" {
		t.Fatalf("legacy receipt changed business scope or archive binding: %+v", legacy)
	}
}

func TestMemorySubjectUniqueIndex(t *testing.T) {
	svc, _ := testService(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}
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
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}

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
		entries, err := svc.RecallTx(
			ctx,
			tx,
			scope,
			memory.AudiencePrivate,
			"agent-1",
			"",
			memory.RecallFilter{},
		)
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
		entries, err := svc.RecallTx(
			ctx,
			tx,
			scope,
			memory.AudiencePrivate,
			"agent-1",
			"",
			memory.RecallFilter{},
		)
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
		_, err := svc.RecallTx(
			context.Background(),
			tx,
			memory.Scope{
				Kind:  memory.ScopeProjectShared,
				Owner: "owner-a",
			},
			memory.AudienceProject,
			"agent-1",
			"",
			memory.RecallFilter{},
		)
		if !errors.Is(err, memory.ErrForbidden) {
			t.Fatalf("unsupported scope RecallTx = %v, want forbidden", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-scope RecallTx: %v", err)
	}
}

func TestMemoryRecallTxExpiredSkipped(t *testing.T) {
	svc, store := testService(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}

	past := int64(1)
	doc := factDocument(scope, "temp", "temporary", "expires")
	doc.ValidUntil = &past
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc); err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	err := store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entries, err := svc.RecallTx(ctx, tx, scope, memory.AudiencePrivate, "", "", memory.RecallFilter{})
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
	ctx context.Context,
	svc *Service,
	store *durable.Store,
	scope memory.Scope,
	agentID string,
	query string,
	filter memory.RecallFilter,
) []memory.Entry {
	t.Helper()
	var entries []memory.Entry
	err := store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		entries, err = svc.RecallTx(ctx, tx, scope, memory.AudiencePrivate, agentID, query, filter)
		return err
	})
	if err != nil {
		t.Fatalf("RecallTx: %v", err)
	}
	return entries
}

func TestMemoryRecallTxTermMatching(t *testing.T) {
	svc, store := testService(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}

	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs")); err != nil {
		t.Fatalf("Create editor: %v", err)
	}
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-2",
		factDocument(scope, "shell", "shell preference", "use zsh")); err != nil {
		t.Fatalf("Create shell: %v", err)
	}

	entries := recallInTx(t, ctx, svc, store, scope, "", "editor", memory.RecallFilter{})
	if len(entries) != 1 || entries[0].SubjectKey != "editor" {
		t.Fatalf("term recall: %+v", entries)
	}

	entries = recallInTx(t, ctx, svc, store, scope, "", "编辑器", memory.RecallFilter{})
	if len(entries) != 0 {
		t.Fatalf("unrelated Han query len = %d", len(entries))
	}

	entries = recallInTx(t, ctx, svc, store, scope, "", "", memory.RecallFilter{})
	if len(entries) != 2 {
		t.Fatalf("empty query len = %d, want all active", len(entries))
	}
}

func TestMemoryRecallTxTagFilter(t *testing.T) {
	svc, store := testService(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}

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

	entries := recallInTx(t, ctx, svc, store, scope, "", "", memory.RecallFilter{Tags: []string{"GO"}})
	if len(entries) != 1 || entries[0].SubjectKey != "vet" {
		t.Fatalf("tag recall: %+v", entries)
	}
}

func TestMemoryRecallTxBoundsAndOrdering(t *testing.T) {
	svc, store := testService(t)
	ctx := access.WithScope(context.Background(), access.Scope{TenantID: "tenant-a"})
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner-a", Tenant: "tenant-a"}

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

	entries := recallInTx(t, ctx, svc, store, scope, "", "", memory.RecallFilter{})
	if len(entries) != 2 || entries[0].ID != updated.ID || entries[1].ID != second.ID {
		t.Fatalf("stable ordering by revision desc: %+v", entries)
	}

	entries = recallInTx(t, ctx, svc, store, scope, "", "", memory.RecallFilter{MaxEntries: 1})
	if len(entries) != 1 || entries[0].ID != updated.ID {
		t.Fatalf("MaxEntries=1: %+v", entries)
	}

	entries = recallInTx(t, ctx, svc, store, scope, "", "",
		memory.RecallFilter{MaxBytes: updated.MaterialBytes()})
	if len(entries) != 1 || entries[0].ID != updated.ID {
		t.Fatalf("MaxBytes keeps first, skips overflow: %+v", entries)
	}
}

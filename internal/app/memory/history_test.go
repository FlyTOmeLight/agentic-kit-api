package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

type historyArchiver struct {
	fakeArchiver
	documents map[string]memory.Document
	readErr   error
}

func (f *historyArchiver) PublishVersion(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	entryID, key string,
	doc *memory.Document,
) (*memory.VersionRef, error) {
	ref, err := f.fakeArchiver.PublishVersion(ctx, binding, entryID, key, doc)
	if err == nil {
		f.documents[ref.VersionID] = *doc
	}
	return ref, err
}

func (f *historyArchiver) ReadVersion(_ context.Context, ref memory.VersionRef) (*memory.Document, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	doc, ok := f.documents[ref.VersionID]
	if !ok {
		return nil, &memory.VersionUnavailableError{Ref: ref}
	}
	return &doc, nil
}

func TestVersionHistory(t *testing.T) {
	archive := &historyArchiver{documents: make(map[string]memory.Document)}
	svc, store := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner-a")
	doc := factDocument(scope, "editor", "first", "tabs")
	entry, err := svc.Create(ctx, "owner-a", "actor-a", "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	first := *entry.ActiveVersionRef
	doc.Summary = "second"
	entry, err = svc.Update(ctx, "owner-a", "actor-a", "update", entry.ID, entry.Revision, doc)
	if err != nil {
		t.Fatal(err)
	}
	second := *entry.ActiveVersionRef
	var adopted table.MemoryOperation
	if err := store.DB().
		Where("entry_id = ? AND applied_revision = ?", entry.ID, 2).
		Take(&adopted).
		Error; err != nil {
		t.Fatal(err)
	}
	for _, status := range []memory.OperationStatus{memory.OperationPending, memory.OperationRejected} {
		row := adopted
		row.ID = string(status)
		row.RequestKey = string(status)
		row.Status = string(status)
		if err := store.DB().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.ListVersions(ctx, "owner-a", entry.ID, 0, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].VersionRef != second || !page.Items[0].Active ||
		page.NextAfterRevision != 2 {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = svc.ListVersions(ctx, "owner-a", entry.ID, page.NextAfterRevision, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].VersionRef != first || page.Items[0].Active ||
		page.NextAfterRevision != 0 {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
	version, err := svc.GetVersion(ctx, "owner-a", entry.ID, first.VersionID)
	if err != nil || version.Document.Summary != "first" || version.Active {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	if _, err := svc.GetVersion(ctx, "owner-b", entry.ID, first.VersionID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("other owner: %v", err)
	}
	if _, err := svc.GetVersion(ctx, "owner-a", entry.ID, "unadopted"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("unadopted version: %v", err)
	}
	archive.readErr = &memory.VersionUnavailableError{Ref: first}
	if _, err := svc.GetVersion(
		ctx,
		"owner-a",
		entry.ID,
		first.VersionID,
	); !errors.Is(
		err,
		memory.ErrVersionUnavailable,
	) {
		t.Fatalf("missing version: %v", err)
	}
	page, err = svc.ListVersions(ctx, "owner-a", entry.ID, 0, 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("list with archive offline: %+v %v", page, err)
	}
	entry, err = svc.Retire(ctx, "owner-a", "actor-a", "retire", entry.ID, entry.Revision)
	if err != nil {
		t.Fatal(err)
	}
	page, err = svc.ListVersions(ctx, "owner-a", entry.ID, 0, 10)
	if err != nil || len(page.Items) != 2 || page.Items[0].Active || page.RetiredAt == nil {
		t.Fatalf("retired page=%+v err=%v", page, err)
	}
	archive.readErr = nil
	doc.Summary = "restored"
	entry, err = svc.Restore(ctx, "owner-a", "actor-a", "restore", entry.ID, entry.Revision, doc)
	if err != nil {
		t.Fatal(err)
	}
	page, err = svc.ListVersions(ctx, "owner-a", entry.ID, 0, 10)
	if err != nil || len(page.Items) != 3 || !page.Items[0].Active || page.Items[0].AppliedRevision != 4 {
		t.Fatalf("restored page=%+v err=%v", page, err)
	}
	bad := archive.documents[first.VersionID]
	bad.Scope.Owner = "other"
	archive.documents[first.VersionID] = bad
	if _, err := svc.GetVersion(ctx, "owner-a", entry.ID, first.VersionID); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("invalid archived identity: %v", err)
	}
}

func TestLocalMemoryHasEmptyVersionHistory(t *testing.T) {
	svc, _ := testService(t)
	ctx := archiveTestContext()
	doc := factDocument(archiveTestScope("owner-a"), "", "local", "body")
	entry, err := svc.Create(ctx, "owner-a", "actor-a", "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListVersions(ctx, "owner-a", entry.ID, 0, 50)
	if err != nil || len(page.Items) != 0 || page.Items == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

package memory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// fakeArchiver records calls and injects failures at each remote step.
type fakeArchiver struct {
	bindings       atomic.Int64
	collections    atomic.Int64
	publishes      atomic.Int64
	failCollection error
	failPublish    error
	lastBinding    *memory.ArtifactBinding
	publishStarted chan struct{}
	publishRelease chan struct{}
	replayRef      *memory.VersionRef
}

type uniqueCollectionArchiver struct {
	fakeArchiver
	mu                 sync.Mutex
	collectionsByEntry map[string]string
	bindingsByEntry    map[string]memory.ArtifactBinding
}

func (f *uniqueCollectionArchiver) EnsureCollection(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	entryID, requestKey string,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.collectionsByEntry == nil {
		f.collectionsByEntry = make(map[string]string)
		f.bindingsByEntry = make(map[string]memory.ArtifactBinding)
	}
	if previous, exists := f.collectionsByEntry[entryID]; exists {
		if previous != requestKey || f.bindingsByEntry[entryID] != *binding {
			return "", memory.ErrConflict
		}
	} else {
		f.collectionsByEntry[entryID] = requestKey
		f.bindingsByEntry[entryID] = *binding
	}
	return f.fakeArchiver.EnsureCollection(ctx, binding, entryID, requestKey)
}

func (f *fakeArchiver) ResolveBinding(scope memory.Scope, actor string) (*memory.ArtifactBinding, error) {
	f.bindings.Add(1)
	binding := &memory.ArtifactBinding{
		StoreID:   "artifact-test",
		TenantID:  scope.Tenant,
		ProjectID: scope.Project,
		UserID:    actor,
	}
	if binding.TenantID == "" {
		binding.TenantID = "tenant-store"
	}
	if scope.Kind == memory.ScopeOwnerPrivate {
		binding.UserID = scope.Owner
	}
	f.lastBinding = binding
	return binding, nil
}

func (f *fakeArchiver) EnsureCollection(
	_ context.Context,
	binding *memory.ArtifactBinding,
	entryID, _ string,
) (string, error) {
	f.collections.Add(1)
	if err := f.failCollection; err != nil {
		return "", err
	}
	if binding.ArtifactID != "" {
		return binding.ArtifactID, nil
	}
	return "art-" + entryID, nil
}

func (f *fakeArchiver) PublishVersion(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	_, requestKey string,
	_ *memory.Document,
) (*memory.VersionRef, error) {
	f.publishes.Add(1)
	if f.publishStarted != nil {
		select {
		case f.publishStarted <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-f.publishRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := f.failPublish; err != nil {
		return nil, err
	}
	if f.replayRef != nil {
		ref := *f.replayRef
		return &ref, nil
	}
	return &memory.VersionRef{
		StoreID:    binding.StoreID,
		TenantID:   binding.TenantID,
		ProjectID:  binding.ProjectID,
		UserID:     binding.UserID,
		ArtifactID: binding.ArtifactID,
		VersionID:  "ver-" + requestKey,
		Sequence:   f.publishes.Load(),
		VersionURI: fmt.Sprintf("artifact://%s/%s", binding.ArtifactID, requestKey),
	}, nil
}

type cancelAfterArchiveResult struct {
	fakeArchiver
	step      string
	cancel    context.CancelFunc
	tombstone bool
}

func (f *cancelAfterArchiveResult) EnsureCollection(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	entryID, requestKey string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	artifactID, err := f.fakeArchiver.EnsureCollection(ctx, binding, entryID, requestKey)
	if f.step == "collection" {
		f.cancel()
	}
	return artifactID, err
}

func (f *cancelAfterArchiveResult) PublishVersion(
	ctx context.Context,
	binding *memory.ArtifactBinding,
	entryID, requestKey string,
	doc *memory.Document,
) (*memory.VersionRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref, err := f.fakeArchiver.PublishVersion(ctx, binding, entryID, requestKey, doc)
	if f.step == "publish" {
		f.cancel()
	}
	if err == nil && f.tombstone {
		return nil, &memory.VersionUnavailableError{Ref: *ref}
	}
	return ref, err
}

func TestArchivePersistsRemoteResultAfterCancellation(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		step      string
		tombstone bool
	}{
		{name: "collection", step: "collection"},
		{name: "version", step: "publish"},
		{name: "tombstone", step: "publish", tombstone: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(archiveTestContext())
			defer cancel()
			archive := &cancelAfterArchiveResult{
				step: scenario.step, cancel: cancel, tombstone: scenario.tombstone,
			}
			svc, store := testServiceWithArchive(t, archive)
			doc := factDocument(
				archiveTestScope("owner"),
				"",
				"preference",
				"tabs",
			)
			_, err := svc.Create(ctx, "owner", "actor", "canceled", doc)
			if scenario.tombstone {
				if !errors.Is(err, memory.ErrVersionUnavailable) {
					t.Fatalf("Create error = %v, want tombstone", err)
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("Create error = %v, want cancellation", err)
			}
			var receipt table.MemoryOperation
			if err := store.DB().Where("request_key = ?", "canceled").Take(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			op := receipt.ToDomain()
			if op.Binding == nil || op.Binding.ArtifactID != "art-"+op.EntryID {
				t.Fatalf("remote collection identity not saved: %+v", op.Binding)
			}
			if (op.VersionRef != nil) != (scenario.step == "publish") {
				t.Fatalf("saved version = %+v, step = %s", op.VersionRef, scenario.step)
			}
			wantStatus := memory.OperationPending
			if scenario.tombstone {
				wantStatus = memory.OperationRejected
			}
			if op.Status != wantStatus {
				t.Fatalf("receipt status = %s, want %s", op.Status, wantStatus)
			}
			if receipt.ArchiveClaim != "" || receipt.ArchiveUntil != nil {
				t.Fatalf(
					"archive claim not released: %q, %v",
					receipt.ArchiveClaim,
					receipt.ArchiveUntil,
				)
			}
			var entries int64
			if err := store.DB().Model(&table.MemoryEntry{}).Count(&entries).Error; err != nil {
				t.Fatal(err)
			}
			if entries != 0 {
				t.Fatalf("canceled operation activated %d entries", entries)
			}
			svc.archive = &archive.fakeArchiver
			entry, err := svc.Create(archiveTestContext(), "owner", "actor", "canceled", doc)
			if scenario.tombstone {
				var unavailable *memory.VersionUnavailableError
				if !errors.As(err, &unavailable) ||
					!reflect.DeepEqual(unavailable.Ref, *op.VersionRef) {
					t.Fatalf("replayed tombstone = %v, want fixed version %+v", err, op.VersionRef)
				}
			} else if err != nil || entry.ID != op.EntryID || entry.Revision != 1 {
				t.Fatalf("replayed entry = %+v, error = %v", entry, err)
			}
			if archive.collections.Load() != 1 || archive.publishes.Load() != 1 {
				t.Fatalf("remote calls = %d collections, %d publishes; want one each",
					archive.collections.Load(), archive.publishes.Load())
			}
		})
	}
}

func TestArchiveConcurrentReplayClaimsOnce(t *testing.T) {
	archive := &fakeArchiver{publishStarted: make(chan struct{}, 2), publishRelease: make(chan struct{})}
	svc, store := testServiceWithArchive(t, archive)
	ctx, cancel := context.WithTimeout(archiveTestContext(), 10*time.Second)
	defer cancel()
	scope := archiveTestScope("owner")
	doc := factDocument(scope, "", "preference", "tabs")
	finished := make(chan error, 1)
	go func() {
		_, err := svc.Create(ctx, "owner", "actor", "same-key", doc)
		finished <- err
	}()
	select {
	case <-archive.publishStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := NewService(store.DB(), archive)
	_, replayErr := second.Create(ctx, "owner", "actor", "same-key", doc)
	close(archive.publishRelease)
	if !errors.Is(replayErr, memory.ErrPending) {
		t.Fatalf("concurrent replay error = %v, want Pending", replayErr)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	entry, err := second.Create(ctx, "owner", "actor", "same-key", doc)
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.DB().Model(&table.MemoryEntry{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || archive.collections.Load() != 1 || archive.publishes.Load() != 1 || entry.Revision != 1 {
		t.Fatalf(
			"entries=%d collection=%d publish=%d entry=%+v",
			count,
			archive.collections.Load(),
			archive.publishes.Load(),
			entry,
		)
	}
}

func TestArchiveClaimExpiresAndCanResume(t *testing.T) {
	archive := &fakeArchiver{failCollection: memory.ErrUnavailable}
	svc, store := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	doc := factDocument(
		archiveTestScope("owner"),
		"editor",
		"preference",
		"tabs",
	)
	if _, err := svc.Create(ctx, "owner", "actor", "key", doc); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatal(err)
	}
	var receipt table.MemoryOperation
	if err := store.DB().Where("request_key = ?", "key").Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB().
		Model(&receipt).
		Updates(map[string]any{"archive_claim": "crashed", "archive_until": gorm.Expr("clock_timestamp() + interval '1 minute'")}).
		Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "owner", "actor", "key", doc); !errors.Is(err, memory.ErrPending) {
		t.Fatalf("unexpired claim = %v", err)
	}
	if err := store.DB().Model(&receipt).
		Update("archive_until", gorm.Expr("clock_timestamp() - interval '1 minute'")).Error; err != nil {
		t.Fatal(err)
	}
	archive.failCollection = nil
	if _, err := svc.Create(ctx, "owner", "actor", "key", doc); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRevisionLoserRetainsPublishedReference(t *testing.T) {
	archive := &fakeArchiver{}
	svc, store := testServiceWithArchive(t, archive)
	ctx, cancel := context.WithTimeout(archiveTestContext(), 10*time.Second)
	defer cancel()
	scope := archiveTestScope("owner")
	entry, err := svc.Create(ctx, "owner", "actor", "create", factDocument(scope, "editor", "original", "tabs"))
	if err != nil {
		t.Fatal(err)
	}
	archive.publishStarted = make(chan struct{}, 1)
	archive.publishRelease = make(chan struct{})
	finished := make(chan error, 1)
	doc := factDocument(scope, "editor", "updated", "spaces")
	go func() {
		_, err := svc.Update(ctx, "owner", "actor", "update", entry.ID, 1, doc)
		finished <- err
	}()
	select {
	case <-archive.publishStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, retireErr := svc.Retire(ctx, "owner", "actor", "retire", entry.ID, 1)
	close(archive.publishRelease)
	if retireErr != nil {
		t.Fatal(retireErr)
	}
	if err := <-finished; !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("losing update = %v", err)
	}
	var receipt table.MemoryOperation
	if err := store.DB().Where("request_key = ?", "update").Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Status != string(memory.OperationRejected) || receipt.ToDomain().VersionRef == nil {
		t.Fatalf("lost rejected receipt/reference: %+v", receipt)
	}
	if _, err := svc.Update(
		ctx,
		"owner",
		"actor",
		"update",
		entry.ID,
		1,
		doc,
	); !errors.Is(
		err,
		memory.ErrConflict,
	) {
		t.Fatalf("rejected replay = %v", err)
	}
	if archive.publishes.Load() != 2 {
		t.Fatal("rejected replay published again")
	}
	current, err := svc.Get(ctx, "owner", entry.ID)
	if err != nil || current.RetiredAt == nil || current.Summary != "original" {
		t.Fatalf("loser changed projection: %+v, %v", current, err)
	}
}

func TestFirstArchiveLosesRevisionThenRestores(t *testing.T) {
	archive := &uniqueCollectionArchiver{}
	local, store := testServiceWithArchive(t, nil)
	ctx, cancel := context.WithTimeout(archiveTestContext(), 10*time.Second)
	defer cancel()
	scope := archiveTestScope("owner")
	doc := factDocument(scope, "editor", "original", "tabs")
	entry, err := local.Create(ctx, "owner", "actor-a", "create-local", doc)
	if err != nil {
		t.Fatal(err)
	}
	archive.publishStarted = make(chan struct{}, 1)
	archive.publishRelease = make(chan struct{})
	archived := NewService(store.DB(), archive)
	finished := make(chan error, 1)
	go func() {
		_, err := archived.Update(ctx, "owner", "actor-a", "update", entry.ID, 1, doc)
		finished <- err
	}()
	select {
	case <-archive.publishStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, retireErr := archived.Retire(ctx, "owner", "actor-a", "retire", entry.ID, 1)
	close(archive.publishRelease)
	if retireErr != nil {
		t.Fatal(retireErr)
	}
	if err := <-finished; !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("losing update = %v", err)
	}
	archive.publishStarted = nil
	archive.publishRelease = nil
	restored, err := archived.Restore(ctx, "owner", "actor-b", "restore", entry.ID, 2, doc)
	if err != nil {
		t.Fatalf("restore after losing first archive: %v", err)
	}
	if restored.Binding == nil || restored.Binding.UserID != "owner" || restored.ActiveVersionRef == nil {
		t.Fatalf("restore did not preserve original binding: %+v", restored)
	}
}

func TestArchiveReplayConfirmsPublishedVersionAfterRevisionChanges(t *testing.T) {
	archive := &fakeArchiver{}
	svc, store := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner")
	original := factDocument(scope, "editor", "original", "tabs")
	entry, err := svc.Create(ctx, "owner", "actor", "create", original)
	if err != nil {
		t.Fatal(err)
	}
	update := factDocument(scope, "editor", "updated", "spaces")
	archive.failPublish = memory.ErrUnavailable
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
		t.Fatalf("interrupted update = %v", err)
	}
	var receipt table.MemoryOperation
	if err := store.DB().Where("request_key = ?", "update").Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	pending := receipt.ToDomain()
	if pending.Binding == nil || pending.VersionRef != nil {
		t.Fatalf("unexpected pending receipt: %+v", pending)
	}
	archive.failPublish = nil
	archived, err := archive.PublishVersion(ctx, pending.Binding, entry.ID, pending.ID, &update)
	if err != nil {
		t.Fatal(err)
	}
	archive.replayRef = archived
	if _, err := svc.Retire(ctx, "owner", "actor", "retire", entry.ID, 1); err != nil {
		t.Fatal(err)
	}
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
		memory.ErrConflict,
	) {
		t.Fatalf("replayed update = %v", err)
	}
	if err := store.DB().Where("request_key = ?", "update").Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	rejected := receipt.ToDomain()
	if rejected.Status != memory.OperationRejected || rejected.VersionRef == nil ||
		*rejected.VersionRef != *archived {
		t.Fatalf("published version not retained on conflict: %+v", rejected)
	}
	current, err := svc.Get(ctx, "owner", entry.ID)
	if err != nil || current.RetiredAt == nil || current.Summary != original.Summary {
		t.Fatalf("replay changed current entry: %+v, %v", current, err)
	}
}

func TestArchivedEntryCannotFallBackToLocalWrites(t *testing.T) {
	svc, store := testServiceWithArchive(t, &fakeArchiver{})
	ctx := archiveTestContext()
	doc := factDocument(
		archiveTestScope("owner"),
		"editor",
		"preference",
		"tabs",
	)
	entry, err := svc.Create(ctx, "owner", "actor", "create", doc)
	if err != nil {
		t.Fatal(err)
	}
	local := NewService(store.DB(), nil)
	if _, err := local.Update(
		ctx,
		"owner",
		"actor",
		"update",
		entry.ID,
		1,
		doc,
	); !errors.Is(
		err,
		memory.ErrUnavailable,
	) {
		t.Fatalf("disabled archive accepted update: %v", err)
	}
}

func TestArchiveCreatePublishesAndActivates(t *testing.T) {
	archive := &fakeArchiver{}
	svc, _ := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner-a")

	entry, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.ActiveVersionRef == nil {
		t.Fatal("Create did not store an active version reference")
	}
	if entry.ActiveVersionRef.ArtifactID != "art-"+entry.ID {
		t.Fatalf("unexpected artifact binding: %+v", entry.ActiveVersionRef)
	}
	if entry.Binding == nil || entry.Binding.ArtifactID != "art-"+entry.ID {
		t.Fatalf("entry binding not persisted: %+v", entry.Binding)
	}
	if archive.collections.Load() != 1 || archive.publishes.Load() != 1 {
		t.Fatalf("remote calls = %d collections, %d publishes; want 1 each",
			archive.collections.Load(), archive.publishes.Load())
	}

	replayed, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatalf("replay Create: %v", err)
	}
	if replayed.ID != entry.ID || archive.collections.Load() != 1 || archive.publishes.Load() != 1 {
		t.Fatalf("replay must not repeat remote calls: id=%q calls=%d/%d",
			replayed.ID, archive.collections.Load(), archive.publishes.Load())
	}
}

func TestArchiveDeletedVersionRetainsReceiptWithoutActivation(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			archive := &fakeArchiver{}
			svc, store := testServiceWithArchive(t, archive)
			ctx := archiveTestContext()
			scope := archiveTestScope("owner")
			doc := factDocument(scope, "editor", "preference", "tabs")
			var original *memory.Entry
			if operation == "update" {
				var err error
				original, err = svc.Create(ctx, "owner", "actor", "original", doc)
				if err != nil {
					t.Fatal(err)
				}
			}
			write := func() error {
				if original != nil {
					_, err := svc.Update(ctx, "owner", "actor", "deleted", original.ID, 1, doc)
					return err
				}
				_, err := svc.Create(ctx, "owner", "actor", "deleted", doc)
				return err
			}
			archive.failPublish = memory.ErrUnavailable
			if err := write(); !errors.Is(err, memory.ErrUnavailable) {
				t.Fatal(err)
			}
			var receipt table.MemoryOperation
			if err := store.DB().Where("request_key = ?", "deleted").Take(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			binding := receipt.ToDomain().Binding
			ref := memory.VersionRef{
				StoreID: binding.StoreID, TenantID: binding.TenantID,
				ProjectID: binding.ProjectID, UserID: binding.UserID,
				ArtifactID: binding.ArtifactID, VersionID: "deleted-version",
				Sequence: 2, VersionURI: "artifact://" + binding.ArtifactID + "/deleted-version",
			}
			archive.failPublish = &memory.VersionUnavailableError{Ref: ref}
			for attempt := 0; attempt < 2; attempt++ {
				err := write()
				var unavailable *memory.VersionUnavailableError
				if !errors.Is(err, memory.ErrVersionUnavailable) || !errors.As(err, &unavailable) ||
					unavailable.Ref != ref {
					t.Fatalf("attempt %d: lost unavailable version identity: %v", attempt, err)
				}
				if attempt == 0 {
					archive.failPublish = nil
				}
			}
			expectedPublishes := int64(2)
			if original != nil {
				expectedPublishes++
			}
			if archive.publishes.Load() != expectedPublishes {
				t.Fatal("deleted version replay published again")
			}
			if err := store.DB().Where("request_key = ?", "deleted").Take(&receipt).Error; err != nil {
				t.Fatal(err)
			}
			stored := receipt.ToDomain()
			if stored.Status != memory.OperationRejected ||
				stored.ErrorReason != "MemoryVersionUnavailable" ||
				stored.VersionRef == nil ||
				*stored.VersionRef != ref ||
				receipt.ArchiveClaim != "" {
				t.Fatalf("lost tombstone receipt: %+v", stored)
			}
			if _, err := svc.activate(ctx, stored, &doc); !errors.Is(err, memory.ErrVersionUnavailable) {
				t.Fatalf("deleted version activation = %v", err)
			}
			if original == nil {
				if _, err := svc.Get(
					ctx,
					"owner",
					stored.EntryID,
				); !errors.Is(
					err,
					memory.ErrNotFound,
				) {
					t.Fatalf("deleted version created an entry: %v", err)
				}
			} else {
				current, err := svc.Get(ctx, "owner", original.ID)
				if err != nil || !reflect.DeepEqual(current, original) {
					t.Fatalf("deleted version changed active entry: %+v, %v", current, err)
				}
			}
		})
	}
}

func TestArchivePublishFailureKeepsPendingAndRecovers(t *testing.T) {
	archive := &fakeArchiver{failPublish: memory.ErrUnavailable}
	svc, _ := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner-a")
	doc := factDocument(scope, "editor", "editor preference", "use tabs")

	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	if archive.publishes.Load() != 1 {
		t.Fatalf("publishes = %d, want 1 attempt", archive.publishes.Load())
	}

	archive.failPublish = nil
	entry, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc)
	if err != nil {
		t.Fatalf("retry Create: %v", err)
	}
	if entry.ActiveVersionRef == nil {
		t.Fatal("retry did not activate the archived version")
	}
	if archive.collections.Load() != 1 || archive.publishes.Load() != 2 {
		t.Fatalf("recovery calls = %d collections, %d publishes; want 1/2",
			archive.collections.Load(), archive.publishes.Load())
	}
}

func TestArchiveCollectionFailureKeepsPending(t *testing.T) {
	archive := &fakeArchiver{failCollection: memory.ErrUnavailable}
	svc, _ := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner-a")
	doc := factDocument(scope, "editor", "editor preference", "use tabs")

	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	if archive.publishes.Load() != 0 {
		t.Fatalf("publish attempted before collection: %d", archive.publishes.Load())
	}

	archive.failCollection = nil
	if _, err := svc.Create(ctx, "owner-a", "actor-a", "key-1", doc); err != nil {
		t.Fatalf("retry Create: %v", err)
	}
	if archive.collections.Load() != 2 || archive.publishes.Load() != 1 {
		t.Fatalf("recovery calls = %d collections, %d publishes; want 2/1",
			archive.collections.Load(), archive.publishes.Load())
	}
}

func TestArchiveUpdatePublishesNewVersion(t *testing.T) {
	archive := &fakeArchiver{}
	svc, _ := testServiceWithArchive(t, archive)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner-a")

	entry, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "v1", "use tabs"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := svc.Update(ctx, "owner-a", "actor-a", "key-2", entry.ID, 1,
		factDocument(scope, "editor", "v2", "use spaces"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 || updated.ActiveVersionRef == nil {
		t.Fatalf("unexpected updated entry: %+v", updated)
	}
	if updated.ActiveVersionRef.VersionID == entry.ActiveVersionRef.VersionID {
		t.Fatal("Update did not publish a new version")
	}
	if archive.collections.Load() != 1 || archive.publishes.Load() != 2 {
		t.Fatalf("calls = %d collections, %d publishes; want 1/2",
			archive.collections.Load(), archive.publishes.Load())
	}
}

func TestLocalOnlyCreateHasNoVersionRef(t *testing.T) {
	svc, _ := testServiceWithArchive(t, nil)
	ctx := archiveTestContext()
	scope := archiveTestScope("owner-a")

	entry, err := svc.Create(ctx, "owner-a", "actor-a", "key-1",
		factDocument(scope, "editor", "editor preference", "use tabs"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.ActiveVersionRef != nil || entry.Binding != nil {
		t.Fatalf("local-only create must stay without remote refs: %+v", entry)
	}
}

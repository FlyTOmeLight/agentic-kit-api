package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

func pendingOperation(t *testing.T, store *durable.Store, requestKey string) table.MemoryOperation {
	t.Helper()
	var receipt table.MemoryOperation
	if err := store.DB().Where("request_key = ?", requestKey).Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.Status != string(memory.OperationPending) {
		t.Fatalf("operation status = %s, want Pending", receipt.Status)
	}
	return receipt
}

func TestResumePendingRetriesCommittedOperations(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		prepare      func(*testing.T, *Service, *fakeArchiver) string
		wantRevision int64
	}{
		{
			name: "create",
			prepare: func(t *testing.T, svc *Service, archive *fakeArchiver) string {
				scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
				archive.failPublish = memory.ErrUnavailable
				if _, err := svc.Create(
					context.Background(),
					"owner",
					"actor",
					"create",
					factDocument(scope, "editor", "editor preference", "use tabs"),
				); !errors.Is(err, memory.ErrUnavailable) {
					t.Fatalf("Create error = %v, want ErrUnavailable", err)
				}
				return ""
			},
			wantRevision: 1,
		},
		{
			name: "update",
			prepare: func(t *testing.T, svc *Service, archive *fakeArchiver) string {
				scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
				entry, err := svc.Create(
					context.Background(),
					"owner",
					"actor",
					"create",
					factDocument(scope, "editor", "original", "use tabs"),
				)
				if err != nil {
					t.Fatal(err)
				}
				archive.failPublish = memory.ErrUnavailable
				if _, err := svc.Update(
					context.Background(),
					"owner",
					"actor",
					"update",
					entry.ID,
					1,
					factDocument(scope, "editor", "updated", "use spaces"),
				); !errors.Is(err, memory.ErrUnavailable) {
					t.Fatalf("Update error = %v, want ErrUnavailable", err)
				}
				return entry.ID
			},
			wantRevision: 2,
		},
		{
			name: "restore",
			prepare: func(t *testing.T, svc *Service, archive *fakeArchiver) string {
				scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
				entry, err := svc.Create(
					context.Background(),
					"owner",
					"actor",
					"create",
					factDocument(scope, "editor", "original", "use tabs"),
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Retire(context.Background(), "owner", "actor", "retire", entry.ID, 1); err != nil {
					t.Fatal(err)
				}
				archive.failPublish = memory.ErrUnavailable
				if _, err := svc.Restore(
					context.Background(),
					"owner",
					"actor",
					"restore",
					entry.ID,
					2,
					factDocument(scope, "editor", "restored", "use spaces"),
				); !errors.Is(err, memory.ErrUnavailable) {
					t.Fatalf("Restore error = %v, want ErrUnavailable", err)
				}
				return entry.ID
			},
			wantRevision: 3,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			archive := &fakeArchiver{}
			svc, store := testServiceWithArchive(t, archive)
			entryID := scenario.prepare(t, svc, archive)
			receipt := pendingOperation(t, store, scenario.name)
			if entryID == "" {
				entryID = receipt.EntryID
			}
			archive.failPublish = nil

			resumed, err := svc.ResumePending(context.Background(), 10)
			if err != nil {
				t.Fatalf("ResumePending: %v", err)
			}
			if resumed != 1 {
				t.Fatalf("resumed = %d, want 1", resumed)
			}
			entry, err := svc.Get(context.Background(), "owner", entryID)
			if err != nil {
				t.Fatal(err)
			}
			if entry.Revision != scenario.wantRevision || entry.ActiveVersionRef == nil {
				t.Fatalf(
					"entry = %+v, want revision %d with active version",
					entry,
					scenario.wantRevision,
				)
			}
			if receipt := pendingOperationStatus(
				t,
				store,
				scenario.name,
			); receipt != memory.OperationApplied {
				t.Fatalf("operation status = %s, want Applied", receipt)
			}
		})
	}
}

func pendingOperationStatus(
	t *testing.T,
	store *durable.Store,
	requestKey string,
) memory.OperationStatus {
	t.Helper()
	var receipt table.MemoryOperation
	if err := store.DB().Where("request_key = ?", requestKey).Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	return memory.OperationStatus(receipt.Status)
}

func TestResumePendingRetriesCollectionFailure(t *testing.T) {
	archive := &fakeArchiver{failCollection: memory.ErrUnavailable}
	svc, store := testServiceWithArchive(t, archive)
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
	if _, err := svc.Create(
		context.Background(),
		"owner",
		"actor",
		"create",
		factDocument(scope, "editor", "editor preference", "use tabs"),
	); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	receipt := pendingOperation(t, store, "create")
	archive.failCollection = nil

	resumed, err := svc.ResumePending(context.Background(), 10)
	if err != nil || resumed != 1 {
		t.Fatalf("ResumePending = (%d, %v), want (1, nil)", resumed, err)
	}
	entry, err := svc.Get(context.Background(), "owner", receipt.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Binding == nil || entry.Binding.ArtifactID == "" || entry.ActiveVersionRef == nil {
		t.Fatalf("entry was not archived and activated: %+v", entry)
	}
	if archive.collections.Load() != 2 || archive.publishes.Load() != 1 {
		t.Fatalf("remote calls = %d collections, %d publishes; want 2/1",
			archive.collections.Load(), archive.publishes.Load())
	}
}

func TestResumePendingWaitsForUnexpiredClaim(t *testing.T) {
	archive := &fakeArchiver{failPublish: memory.ErrUnavailable}
	svc, store := testServiceWithArchive(t, archive)
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
	if _, err := svc.Create(
		context.Background(),
		"owner",
		"actor",
		"create",
		factDocument(scope, "editor", "editor preference", "use tabs"),
	); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	receipt := pendingOperation(t, store, "create")
	if err := store.DB().Model(&receipt).Updates(map[string]any{
		"archive_claim": "crashed",
		"archive_until": gorm.Expr("clock_timestamp() + interval '1 minute'"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	archive.failPublish = nil
	publishes := archive.publishes.Load()

	resumed, err := svc.ResumePending(context.Background(), 10)
	if err != nil || resumed != 0 {
		t.Fatalf("ResumePending = (%d, %v), want (0, nil)", resumed, err)
	}
	if archive.publishes.Load() != publishes {
		t.Fatal("resume bypassed an unexpired archive claim")
	}
	if status := pendingOperationStatus(t, store, "create"); status != memory.OperationPending {
		t.Fatalf("operation status = %s, want Pending", status)
	}

	if err := store.DB().Model(&receipt).
		Update("archive_until", gorm.Expr("clock_timestamp() - interval '1 minute'")).Error; err != nil {
		t.Fatal(err)
	}
	resumed, err = svc.ResumePending(context.Background(), 10)
	if err != nil || resumed != 1 {
		t.Fatalf("ResumePending after expiry = (%d, %v), want (1, nil)", resumed, err)
	}
}

func TestResumePendingContinuesAfterInvalidReceipt(t *testing.T) {
	archive := &fakeArchiver{failPublish: memory.ErrUnavailable}
	svc, store := testServiceWithArchive(t, archive)
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
	if _, err := svc.Create(
		context.Background(),
		"owner",
		"actor",
		"valid",
		factDocument(scope, "editor", "editor preference", "use tabs"),
	); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	valid := pendingOperation(t, store, "valid")
	invalid := table.MemoryOperation{
		ID:           "mop_invalid_" + durable.NewID(),
		OwnerID:      "owner",
		RequestKey:   "invalid",
		IntentDigest: "invalid",
		OpType:       string(memory.OperationCreate),
		EntryID:      "mem_invalid_" + durable.NewID(),
		ActorID:      "actor",
		Document:     json.RawMessage(`[]`),
		Status:       string(memory.OperationPending),
	}
	if err := store.DB().Create(&invalid).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB().Model(&invalid).
		Update("updated_at", gorm.Expr("clock_timestamp() - interval '1 minute'")).Error; err != nil {
		t.Fatal(err)
	}
	archive.failPublish = nil

	resumed, err := svc.ResumePending(context.Background(), 1)
	if resumed != 0 || !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("first ResumePending = (%d, %v), want invalid receipt error", resumed, err)
	}
	resumed, err = svc.ResumePending(context.Background(), 1)
	if resumed != 1 || err != nil {
		t.Fatalf("second ResumePending = (%d, %v), want one success", resumed, err)
	}
	if _, err := svc.Get(context.Background(), "owner", valid.EntryID); err != nil {
		t.Fatalf("valid operation was not resumed: %v", err)
	}
	if status := pendingOperationStatus(t, store, "invalid"); status != memory.OperationPending {
		t.Fatalf("invalid operation status = %s, want Pending", status)
	}
}

func TestResumePendingDefersUnboundReceipt(t *testing.T) {
	archive := &fakeArchiver{failPublish: memory.ErrUnavailable}
	svc, store := testServiceWithArchive(t, archive)
	scope := memory.Scope{Kind: memory.ScopeOwnerPrivate, Owner: "owner"}
	if _, err := svc.Create(
		context.Background(),
		"owner",
		"actor",
		"valid",
		factDocument(scope, "editor", "editor preference", "use tabs"),
	); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("Create error = %v, want ErrUnavailable", err)
	}
	valid := pendingOperation(t, store, "valid")
	doc, err := json.Marshal(factDocument(scope, "editor", "unbound", "use spaces"))
	if err != nil {
		t.Fatal(err)
	}
	unbound := table.MemoryOperation{
		ID:           "mop_unbound_" + durable.NewID(),
		OwnerID:      "owner",
		RequestKey:   "unbound",
		IntentDigest: "unbound",
		OpType:       string(memory.OperationCreate),
		EntryID:      "mem_unbound_" + durable.NewID(),
		ActorID:      "actor",
		Document:     doc,
		Status:       string(memory.OperationPending),
	}
	if err := store.DB().Create(&unbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB().Model(&unbound).
		Update("updated_at", gorm.Expr("clock_timestamp() - interval '1 minute'")).Error; err != nil {
		t.Fatal(err)
	}
	archive.failPublish = nil

	resumed, err := svc.ResumePending(context.Background(), 1)
	if resumed != 0 || err == nil {
		t.Fatalf("first ResumePending = (%d, %v), want unbound receipt error", resumed, err)
	}
	resumed, err = svc.ResumePending(context.Background(), 1)
	if resumed != 1 || err != nil {
		t.Fatalf("second ResumePending = (%d, %v), want one success", resumed, err)
	}
	if _, err := svc.Get(context.Background(), "owner", valid.EntryID); err != nil {
		t.Fatalf("valid operation was not resumed: %v", err)
	}
	if status := pendingOperationStatus(t, store, "unbound"); status != memory.OperationPending {
		t.Fatalf("unbound operation status = %s, want Pending", status)
	}
}

func TestResumePendingNoArchiveAndZeroLimit(t *testing.T) {
	t.Run("no archive", func(t *testing.T) {
		svc, _ := testService(t)
		resumed, err := svc.ResumePending(context.Background(), 10)
		if err != nil || resumed != 0 {
			t.Fatalf("ResumePending = (%d, %v), want (0, nil)", resumed, err)
		}
	})
	t.Run("zero limit", func(t *testing.T) {
		svc, _ := testServiceWithArchive(t, &fakeArchiver{})
		resumed, err := svc.ResumePending(context.Background(), 0)
		if err != nil || resumed != 0 {
			t.Fatalf("ResumePending = (%d, %v), want (0, nil)", resumed, err)
		}
	})
}

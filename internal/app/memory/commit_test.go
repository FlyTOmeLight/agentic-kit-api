package memory

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

var errInjectedCommit = errors.New("injected commit failure")

type commitFailureMode uint8

const (
	commitAppliedThenError commitFailureMode = iota
	commitRolledBackThenError
)

var commitFailureModes = []struct {
	name      string
	mode      commitFailureMode
	committed bool
}{
	{name: "CommittedThenError", mode: commitAppliedThenError, committed: true},
	{name: "RolledBackThenError", mode: commitRolledBackThenError},
}

type commitFaultPool struct {
	*sql.DB
	target   int64
	begins   atomic.Int64
	injected atomic.Bool
	mode     commitFailureMode
}

func (p *commitFaultPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if p.begins.Add(1) == p.target {
		p.injected.Store(true)
		return &commitFaultTx{Tx: tx, mode: p.mode}, nil
	}
	return tx, nil
}

type commitFaultTx struct {
	*sql.Tx
	mode     commitFailureMode
	finished atomic.Bool
}

func (tx *commitFaultTx) Commit() error {
	switch tx.mode {
	case commitAppliedThenError:
		if err := tx.Tx.Commit(); err != nil {
			return err
		}
	case commitRolledBackThenError:
		if err := tx.Tx.Rollback(); err != nil {
			return errors.Join(errInjectedCommit, err)
		}
	default:
		return tx.Tx.Commit()
	}
	tx.finished.Store(true)
	return errInjectedCommit
}

func (tx *commitFaultTx) Rollback() error {
	if tx.finished.Swap(true) {
		return nil
	}
	return tx.Tx.Rollback()
}

func commitFaultDB(
	t *testing.T,
	store *durable.Store,
	mode commitFailureMode,
	target int64,
) (*gorm.DB, *commitFaultPool) {
	t.Helper()
	sqlDB, err := store.DB().DB()
	if err != nil {
		t.Fatal(err)
	}
	pool := &commitFaultPool{DB: sqlDB, mode: mode, target: target}
	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{
			SkipDefaultTransaction: true,
			TranslateError:         true,
			Logger:                 logger.Default.LogMode(logger.Silent),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return db, pool
}

func requireOperation(t *testing.T, store *durable.Store, owner, requestKey string) *memory.Operation {
	t.Helper()
	var row table.MemoryOperation
	if err := store.DB().
		Where("owner_id = ? AND request_key = ?", owner, requestKey).
		Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ToDomain()
}

func requireUnknown(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, memory.ErrOperationOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrOperationOutcomeUnknown", err)
	}
}

func requireNoOperation(t *testing.T, store *durable.Store, owner, requestKey string) {
	t.Helper()
	var count int64
	if err := store.DB().Model(&table.MemoryOperation{}).
		Where("owner_id = ? AND request_key = ?", owner, requestKey).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back receipt count = %d, want 0", count)
	}
}

func TestCommitCreateActivateApplied(t *testing.T) {
	for _, tc := range commitFailureModes {
		t.Run(tc.name, func(t *testing.T) {
			archive := &fakeArchiver{}
			svc, store := testServiceWithArchive(t, archive)
			ctx := archiveTestContext()
			scope := archiveTestScope("owner")
			doc := factDocument(scope, "editor", "preference", "tabs")

			db, pool := commitFaultDB(t, store, tc.mode, 2)
			entry, err := NewService(db, archive).Create(ctx, "owner", "actor", "create", doc)
			if !pool.injected.Load() {
				t.Fatal("target activate transaction was not injected")
			}
			receipt := requireOperation(t, store, "owner", "create")
			if tc.committed {
				if err != nil {
					t.Fatalf("Create = %v, want committed result", err)
				}
				if entry == nil || entry.Revision != 1 {
					t.Fatalf("committed entry = %+v", entry)
				}
				if receipt.Status != memory.OperationApplied {
					t.Fatalf("committed receipt = %+v", receipt)
				}
			} else {
				if entry != nil {
					t.Fatalf("rolled-back entry = %+v", entry)
				}
				requireUnknown(t, err)
				if receipt.Status != memory.OperationPending {
					t.Fatalf("rolled-back receipt = %+v", receipt)
				}
				if _, err := svc.Get(
					ctx,
					"owner",
					receipt.EntryID,
				); !errors.Is(
					err,
					memory.ErrNotFound,
				) {
					t.Fatalf("rolled-back entry is visible: %v", err)
				}
			}

			replayed, err := NewService(db, archive).Create(ctx, "owner", "actor", "create", doc)
			if err != nil {
				t.Fatalf("replay Create = %v", err)
			}
			if replayed.ID != receipt.EntryID || replayed.Revision != 1 {
				t.Fatalf("replay changed fixed identity: %+v", replayed)
			}
			if archive.collections.Load() != 1 || archive.publishes.Load() != 1 {
				t.Fatalf(
					"replay repeated remote work: collections=%d publishes=%d",
					archive.collections.Load(),
					archive.publishes.Load(),
				)
			}
			if receipt = requireOperation(
				t,
				store,
				"owner",
				"create",
			); receipt.Status != memory.OperationApplied {
				t.Fatalf("replayed receipt = %+v", receipt)
			}
		})
	}
}

func TestCommitUpdateActivateRejected(t *testing.T) {
	for _, tc := range commitFailureModes {
		t.Run(tc.name, func(t *testing.T) {
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
				ctx, "owner", "actor", "update", entry.ID, 1, update,
			); !errors.Is(err, memory.ErrUnavailable) {
				t.Fatalf("interrupted update = %v", err)
			}
			archive.failPublish = nil
			if _, err := svc.Retire(ctx, "owner", "actor", "retire", entry.ID, 1); err != nil {
				t.Fatal(err)
			}

			db, pool := commitFaultDB(t, store, tc.mode, 2)
			got, err := NewService(db, archive).Update(
				ctx, "owner", "actor", "update", entry.ID, 1, update,
			)
			if !pool.injected.Load() {
				t.Fatal("target activate transaction was not injected")
			}
			if got != nil {
				t.Fatalf("rejected entry = %+v", got)
			}
			receipt := requireOperation(t, store, "owner", "update")
			if tc.committed {
				if !errors.Is(err, memory.ErrConflict) {
					t.Fatalf("error = %v, want ErrConflict", err)
				}
				if receipt.Status != memory.OperationRejected ||
					receipt.ErrorReason != "RevisionConflict" {
					t.Fatalf("committed receipt = %+v", receipt)
				}
			} else {
				requireUnknown(t, err)
				if receipt.Status != memory.OperationPending || receipt.VersionRef == nil {
					t.Fatalf("rolled-back receipt = %+v", receipt)
				}
			}
			if archive.publishes.Load() != 3 {
				t.Fatalf("publishes before replay = %d, want 3", archive.publishes.Load())
			}

			if _, err := NewService(db, archive).Update(
				ctx, "owner", "actor", "update", entry.ID, 1, update,
			); !errors.Is(err, memory.ErrConflict) {
				t.Fatalf("replay error = %v, want ErrConflict", err)
			}
			if archive.publishes.Load() != 3 {
				t.Fatalf("replay published again: %d", archive.publishes.Load())
			}
			if receipt = requireOperation(
				t,
				store,
				"owner",
				"update",
			); receipt.Status != memory.OperationRejected ||
				receipt.ErrorReason != "RevisionConflict" {
				t.Fatalf("replayed receipt = %+v", receipt)
			}
			current, err := svc.Get(ctx, "owner", entry.ID)
			if err != nil || current.Revision != 2 || current.RetiredAt == nil ||
				current.Summary != original.Summary {
				t.Fatalf("rejected update changed projection: %+v, %v", current, err)
			}
		})
	}
}

func TestCommitRetireApplied(t *testing.T) {
	for _, tc := range commitFailureModes {
		t.Run(tc.name, func(t *testing.T) {
			archive := &fakeArchiver{}
			svc, store := testServiceWithArchive(t, archive)
			ctx := archiveTestContext()
			scope := archiveTestScope("owner")
			doc := factDocument(scope, "editor", "preference", "tabs")
			entry, err := svc.Create(ctx, "owner", "actor", "create", doc)
			if err != nil {
				t.Fatal(err)
			}

			db, pool := commitFaultDB(t, store, tc.mode, 1)
			got, err := NewService(db, archive).Retire(
				ctx, "owner", "actor", "retire", entry.ID, 1,
			)
			if !pool.injected.Load() {
				t.Fatal("target retire transaction was not injected")
			}
			if tc.committed {
				receipt := requireOperation(t, store, "owner", "retire")
				if err != nil {
					t.Fatalf("Retire = %v, want committed result", err)
				}
				if got == nil || got.Revision != 2 || got.RetiredAt == nil {
					t.Fatalf("committed entry = %+v", got)
				}
				if receipt.Status != memory.OperationApplied {
					t.Fatalf("committed receipt = %+v", receipt)
				}
			} else {
				if got != nil {
					t.Fatalf("rolled-back entry = %+v", got)
				}
				requireUnknown(t, err)
				requireNoOperation(t, store, "owner", "retire")
				current, err := svc.Get(ctx, "owner", entry.ID)
				if err != nil || current.Revision != 1 || current.RetiredAt != nil {
					t.Fatalf("rolled-back projection = %+v, %v", current, err)
				}
			}

			replayed, err := NewService(db, archive).Retire(
				ctx, "owner", "actor", "retire", entry.ID, 1,
			)
			if err != nil {
				t.Fatalf("replay Retire = %v", err)
			}
			if replayed.ID != entry.ID || replayed.Revision != 2 || replayed.RetiredAt == nil {
				t.Fatalf("replay result = %+v", replayed)
			}
			if receipt := requireOperation(
				t,
				store,
				"owner",
				"retire",
			); receipt.Status != memory.OperationApplied {
				t.Fatalf("replayed receipt = %+v", receipt)
			}
			if archive.publishes.Load() != 1 {
				t.Fatalf("Retire replay changed archive calls: %d", archive.publishes.Load())
			}
		})
	}
}

func TestCommitRetireRejected(t *testing.T) {
	for _, tc := range commitFailureModes {
		t.Run(tc.name, func(t *testing.T) {
			archive := &fakeArchiver{}
			svc, store := testServiceWithArchive(t, archive)
			ctx := archiveTestContext()
			scope := archiveTestScope("owner")
			doc := factDocument(scope, "editor", "preference", "tabs")
			entry, err := svc.Create(ctx, "owner", "actor", "create", doc)
			if err != nil {
				t.Fatal(err)
			}
			retired, err := svc.Retire(ctx, "owner", "actor", "retire-first", entry.ID, 1)
			if err != nil {
				t.Fatal(err)
			}

			db, pool := commitFaultDB(t, store, tc.mode, 1)
			got, err := NewService(db, archive).Retire(
				ctx, "owner", "actor", "retire-conflict", entry.ID, retired.Revision,
			)
			if !pool.injected.Load() {
				t.Fatal("target retire transaction was not injected")
			}
			if got != nil {
				t.Fatalf("rejected entry = %+v", got)
			}
			if tc.committed {
				receipt := requireOperation(t, store, "owner", "retire-conflict")
				if !errors.Is(err, memory.ErrConflict) {
					t.Fatalf("error = %v, want ErrConflict", err)
				}
				if receipt.Status != memory.OperationRejected || receipt.ErrorReason != "EntryRetired" {
					t.Fatalf("committed receipt = %+v", receipt)
				}
			} else {
				requireUnknown(t, err)
				requireNoOperation(t, store, "owner", "retire-conflict")
			}

			if _, err := NewService(db, archive).Retire(
				ctx, "owner", "actor", "retire-conflict", entry.ID, retired.Revision,
			); !errors.Is(err, memory.ErrConflict) {
				t.Fatalf("replay error = %v, want ErrConflict", err)
			}
			if receipt := requireOperation(
				t,
				store,
				"owner",
				"retire-conflict",
			); receipt.Status != memory.OperationRejected ||
				receipt.ErrorReason != "EntryRetired" {
				t.Fatalf("replayed receipt = %+v", receipt)
			}
			current, err := svc.Get(ctx, "owner", entry.ID)
			if err != nil || current.Revision != 2 || current.RetiredAt == nil {
				t.Fatalf("rejected retire changed projection: %+v, %v", current, err)
			}
		})
	}
}

func TestCommitPrepareRejected(t *testing.T) {
	for _, tc := range commitFailureModes {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := testServiceWithArchive(t, nil)
			ctx := archiveTestContext()
			scope := archiveTestScope("owner")
			doc := factDocument(scope, "editor", "preference", "tabs")
			entry, err := svc.Create(ctx, "owner", "actor", "create", doc)
			if err != nil {
				t.Fatal(err)
			}
			advanced, err := svc.Update(ctx, "owner", "actor", "advance", entry.ID, 1, doc)
			if err != nil {
				t.Fatal(err)
			}

			db, pool := commitFaultDB(t, store, tc.mode, 1)
			got, err := NewService(db, nil).Update(
				ctx, "owner", "actor", "stale", entry.ID, 1, doc,
			)
			if !pool.injected.Load() {
				t.Fatal("target prepare transaction was not injected")
			}
			if got != nil {
				t.Fatalf("rejected entry = %+v", got)
			}
			if tc.committed {
				receipt := requireOperation(t, store, "owner", "stale")
				if !errors.Is(err, memory.ErrConflict) {
					t.Fatalf("error = %v, want ErrConflict", err)
				}
				if receipt.Status != memory.OperationRejected ||
					receipt.ErrorReason != "RevisionConflict" {
					t.Fatalf("committed receipt = %+v", receipt)
				}
			} else {
				requireUnknown(t, err)
				requireNoOperation(t, store, "owner", "stale")
			}

			if _, err := NewService(db, nil).Update(
				ctx, "owner", "actor", "stale", entry.ID, 1, doc,
			); !errors.Is(err, memory.ErrConflict) {
				t.Fatalf("replay error = %v, want ErrConflict", err)
			}
			if receipt := requireOperation(
				t,
				store,
				"owner",
				"stale",
			); receipt.Status != memory.OperationRejected ||
				receipt.ErrorReason != "RevisionConflict" {
				t.Fatalf("replayed receipt = %+v", receipt)
			}
			current, err := svc.Get(ctx, "owner", entry.ID)
			if err != nil || current.Revision != advanced.Revision {
				t.Fatalf("rejected prepare changed projection: %+v, %v", current, err)
			}
		})
	}
}

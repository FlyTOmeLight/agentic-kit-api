// Package memory implements the Memory business service: CRUD, retire/restore,
// and recall for Run acceptance. Content operations archive an immutable
// document through the archive port and activate the fixed version reference
// locally; recall only reads the activated projection, never the archive.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const (
	schemaVersion                  = 1
	memoryVersionUnavailableReason = "MemoryVersionUnavailable"
)

// Archiver is the only remote behavior the service needs from artifact-go.
// It is defined at the consumer so tests inject failures directly; the
// artifact SDK stays inside internal/client.
type Archiver interface {
	ResolveBinding(scope memory.Scope, actor string) (*memory.ArtifactBinding, error)
	EnsureCollection(
		ctx context.Context,
		binding *memory.ArtifactBinding,
		entryID, requestKey string,
	) (string, error)
	PublishVersion(
		ctx context.Context,
		binding *memory.ArtifactBinding,
		entryID, requestKey string,
		doc *memory.Document,
	) (*memory.VersionRef, error)
}

// Service owns the transactional behavior for Memory CRUD and recall.
// archive may be nil for deployments without Memory archiving; writes then
// stay local without a fixed version reference.
type Service struct {
	db      *gorm.DB
	archive Archiver
}

// NewService borrows the database owned by the caller.
func NewService(db *gorm.DB, archive Archiver) *Service {
	return &Service{db: db, archive: archive}
}

// Create prepares the receipt, archives the document, then activates the
// fixed version in one local transaction.
func (s *Service) Create(
	ctx context.Context,
	owner, actor, requestKey string,
	doc memory.Document,
) (*memory.Entry, error) {
	if err := authorizeDocument(ctx, owner, actor, &doc); err != nil {
		return nil, err
	}
	if owner == "" || actor == "" || requestKey == "" {
		return nil, memory.ErrInvalid
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	if err := validateWriteScope(doc.Scope, owner, actor); err != nil {
		return nil, err
	}
	doc.SchemaVersion = schemaVersion
	doc.Source.Actor = actor
	doc.EntryID = newEntryID()
	op := operationFromDocument(&doc, owner, actor, requestKey, memory.OperationCreate, 0)
	return s.runContentOperation(ctx, op)
}

// Get returns one entry if the caller is authorized.
func (s *Service) Get(ctx context.Context, owner, id string) (*memory.Entry, error) {
	scope, err := requireScope(ctx)
	if err != nil {
		return nil, err
	}
	var row table.MemoryEntry
	query := privateEntries(table.TenantQuery(ctx, s.db), owner, scope.ProjectID == "")
	if err := query.Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, mapError(err)
	}
	return row.ToDomain(), nil
}

// List returns entries owned by the caller, newest first.
func (s *Service) List(ctx context.Context, owner, after string, limit int) ([]memory.Entry, error) {
	scope, err := requireScope(ctx)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, memory.ErrInvalid
	}
	q := privateEntries(table.TenantQuery(ctx, s.db), owner, scope.ProjectID == "").Where("id > ?", after)
	var rows []table.MemoryEntry
	if err := q.Order("id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, mapError(err)
	}
	entries := make([]memory.Entry, len(rows))
	for i := range rows {
		entries[i] = *rows[i].ToDomain()
	}
	return entries, nil
}

// Update replaces the complete content of an active entry.
func (s *Service) Update(
	ctx context.Context,
	owner, actor, requestKey, entryID string,
	expectedRevision int64,
	doc memory.Document,
) (*memory.Entry, error) {
	return s.prepareRevisioned(
		ctx, owner, actor, requestKey, entryID, expectedRevision, doc, memory.OperationUpdate,
	)
}

// Restore re-enables a retired entry with new complete content.
func (s *Service) Restore(
	ctx context.Context,
	owner, actor, requestKey, entryID string,
	expectedRevision int64,
	doc memory.Document,
) (*memory.Entry, error) {
	return s.prepareRevisioned(
		ctx, owner, actor, requestKey, entryID, expectedRevision, doc, memory.OperationRestore,
	)
}

func (s *Service) prepareRevisioned(
	ctx context.Context,
	owner, actor, requestKey, entryID string,
	expectedRevision int64,
	doc memory.Document,
	opType memory.OperationType,
) (*memory.Entry, error) {
	if err := authorizeDocument(ctx, owner, actor, &doc); err != nil {
		return nil, err
	}
	if owner == "" || actor == "" || requestKey == "" || entryID == "" || expectedRevision < 1 {
		return nil, memory.ErrInvalid
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	if err := validateWriteScope(doc.Scope, owner, actor); err != nil {
		return nil, err
	}
	doc.SchemaVersion = schemaVersion
	doc.Source.Actor = actor
	doc.EntryID = entryID
	op := operationFromDocument(&doc, owner, actor, requestKey, opType, expectedRevision)
	return s.runContentOperation(ctx, op)
}

// runContentOperation executes the receipt-driven publish flow: prepare the
// durable receipt, archive outside any transaction, then activate locally.
// A replayed key returns the original result without new remote calls.
func (s *Service) runContentOperation(
	ctx context.Context,
	op *memory.Operation,
) (*memory.Entry, error) {
	entry, archived, err := s.prepare(ctx, op)
	if err != nil {
		return nil, err
	}
	if op.Status == memory.OperationApplied {
		return entry, nil
	}
	doc, err := operationDocument(op)
	if err != nil {
		return nil, err
	}
	if archived {
		return s.activate(ctx, op, doc)
	}
	archived, err = s.Archive(ctx, op)
	if err != nil {
		return nil, err
	}
	if !archived {
		return nil, memory.ErrPending
	}
	return s.activate(ctx, op, doc)
}

// prepare persists the operation receipt and, for a new create, resolves the
// fixed storage binding. It replays an existing receipt: identical intents
// continue the original operation, different intents conflict.
func (s *Service) prepare(ctx context.Context, op *memory.Operation) (*memory.Entry, bool, error) {
	var entry *memory.Entry
	var archived bool
	var rejected bool
	callbackSucceeded := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row table.MemoryOperation
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(table.MemoryOperationFromDomain(op)).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND owner_id = ? AND request_key = ?", op.TenantID, op.OwnerID, op.RequestKey).
			Take(&row).Error; err != nil {
			return err
		}
		existing := row.ToDomain()
		if existing.IntentDigest != op.IntentDigest || existing.ActorID != op.ActorID {
			return memory.ErrConflict
		}
		*op = *existing
		switch existing.Status {
		case memory.OperationApplied:
			var err error
			entry, err = appliedEntry(existing)
			return err
		case memory.OperationRejected:
			if existing.ErrorReason == memoryVersionUnavailableReason {
				if existing.VersionRef == nil {
					return memory.ErrUnavailable
				}
				return &memory.VersionUnavailableError{Ref: *existing.VersionRef}
			}
			return memory.ErrConflict
		}
		if row.ArchiveClaim != "" {
			var active int64
			if err := tx.Model(&table.MemoryOperation{}).
				Where("id = ? AND archive_until > clock_timestamp()", row.ID).
				Count(&active).Error; err != nil {
				return err
			}
			if active != 0 {
				return memory.ErrPending
			}
		}
		doc, err := operationDocument(existing)
		if err != nil {
			return err
		}
		if err := validateWriteScope(doc.Scope, existing.OwnerID, existing.ActorID); err != nil {
			return err
		}
		actorPrivate := doc.Scope.Project == "" && existing.ActorID == existing.OwnerID
		var target *memory.Entry
		if existing.Type != memory.OperationCreate {
			var entryRow table.MemoryEntry
			if err := privateEntries(
				tx.Clauses(clause.Locking{Strength: "UPDATE"}),
				existing.OwnerID,
				actorPrivate,
			).Where("tenant_id = ? AND id = ?", existing.TenantID, existing.EntryID).
				Take(&entryRow).
				Error; err != nil {
				return mapError(err)
			}
			target = entryRow.ToDomain()
			if existing.Binding == nil {
				if reason := revisionConflict(target, existing, doc); reason != "" {
					rejected = true
					if err := markRejected(tx, existing, reason); err != nil {
						return err
					}
					callbackSucceeded = true
					return nil
				}
			}
		}
		if s.archive == nil {
			if existing.Binding != nil || target != nil && target.Binding != nil {
				return memory.ErrUnavailable
			}
			// Local-only deployments archive nothing; activation projects the
			// receipt document directly.
			archived = true
			return nil
		}
		if existing.Type == memory.OperationCreate && existing.Binding == nil {
			binding, err := s.archive.ResolveBinding(doc.Scope, existing.ActorID)
			if err != nil {
				return err
			}
			existing.Binding = binding
			result := table.TenantQuery(ctx, tx.Model(&table.MemoryOperation{})).
				Where("id = ? AND status = ?", existing.ID, string(memory.OperationPending)).
				Update("binding", mustMarshal(binding))
			if err := requireUpdated(result); err != nil {
				return err
			}
			op.Binding = binding
		}
		if existing.Type != memory.OperationCreate && existing.Binding == nil {
			binding := target.Binding
			if binding == nil {
				resolved, err := s.archive.ResolveBinding(target.Scope, existing.ActorID)
				if err != nil {
					return err
				}
				binding = resolved
			}
			existing.Binding = binding
			result := table.TenantQuery(ctx, tx.Model(&table.MemoryOperation{})).
				Where("id = ? AND status = ?", existing.ID, string(memory.OperationPending)).
				Update("binding", mustMarshal(binding))
			if err := requireUpdated(result); err != nil {
				return err
			}
			op.Binding = binding
		}
		if existing.Type != memory.OperationCreate {
			if target.Binding == nil {
				result := privateEntries(
					tx.Model(&table.MemoryEntry{}),
					existing.OwnerID,
					actorPrivate,
				).
					Where("tenant_id = ? AND id = ? AND binding IS NULL", existing.TenantID, existing.EntryID).
					Update("binding", mustMarshal(existing.Binding))
				if err := requireUpdated(result); err != nil {
					return err
				}
			} else if existing.Binding.StoreID != target.Binding.StoreID ||
				existing.Binding.TenantID != target.Binding.TenantID ||
				existing.Binding.ProjectID != target.Binding.ProjectID ||
				existing.Binding.UserID != target.Binding.UserID ||
				(target.Binding.ArtifactID != "" && existing.Binding.ArtifactID != target.Binding.ArtifactID) {
				return memory.ErrConflict
			}
		}
		if existing.Binding == nil || existing.Binding.ArtifactID == "" {
			return nil
		}
		if existing.VersionRef == nil {
			return nil
		}
		archived = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			resolved, resolveErr := s.resolveCommittedOperation(ctx, op, err)
			return resolved, false, resolveErr
		}
		return nil, false, err
	}
	if rejected {
		return nil, false, memory.ErrConflict
	}
	return entry, archived, nil
}

// Archive performs the remote collection and publish steps for one pending
// receipt. It is separate from prepare so callers never hold a database
// transaction across HTTP. A lost receipt race (another operation already
// activated the entry) ends as ErrPending so the caller surfaces a retryable
// state instead of publishing into a dead receipt.
func (s *Service) Archive(ctx context.Context, op *memory.Operation) (archived bool, err error) {
	if op.Status != memory.OperationPending || s.archive == nil || op.Binding == nil {
		return op.Status == memory.OperationApplied || s.archive == nil, nil
	}
	claim := durable.NewID()
	var row table.MemoryOperation
	result := table.TenantQuery(ctx, s.db).Model(&row).Clauses(clause.Returning{}).
		Where("id = ? AND tenant_id = ? AND status = ? AND (archive_until IS NULL OR archive_until <= clock_timestamp())",
			op.ID, op.TenantID, string(memory.OperationPending)).
		Updates(map[string]any{
			"archive_claim": claim,
			"archive_until": gorm.Expr("clock_timestamp() + interval '2 minutes'"),
			"updated_at":    gorm.Expr("clock_timestamp()"),
		})
	if err := requireUpdated(result); err != nil {
		return false, err
	}
	*op = *row.ToDomain()
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		release := s.db.WithContext(releaseCtx).Model(&table.MemoryOperation{}).
			Where("id = ? AND tenant_id = ? AND archive_claim = ?", op.ID, op.TenantID, claim).
			Updates(map[string]any{
				"archive_claim": "",
				"archive_until": nil,
				"updated_at":    gorm.Expr("clock_timestamp()"),
			})
		err = errors.Join(err, release.Error)
	}()
	archiveCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	doc, err := operationDocument(op)
	if err != nil {
		return false, err
	}
	if doc.Scope.Tenant != op.TenantID {
		return false, memory.ErrConflict
	}
	if op.Binding.ArtifactID == "" {
		artifactID, err := s.archive.EnsureCollection(
			archiveCtx,
			op.Binding,
			op.EntryID,
			collectionKey(op.EntryID),
		)
		if err != nil {
			return false, err
		}
		err = s.persistArchiveResult(ctx, op, claim, map[string]any{
			"binding": mustMarshal(&memory.ArtifactBinding{
				StoreID:    op.Binding.StoreID,
				TenantID:   op.Binding.TenantID,
				ProjectID:  op.Binding.ProjectID,
				UserID:     op.Binding.UserID,
				ArtifactID: artifactID,
			}),
		})
		if err != nil {
			return false, err
		}
		op.Binding.ArtifactID = artifactID
	}
	if op.VersionRef != nil {
		return true, nil
	}
	ref, err := s.archive.PublishVersion(archiveCtx, op.Binding, op.EntryID, op.ID, doc)
	if err != nil {
		var unavailable *memory.VersionUnavailableError
		if errors.As(err, &unavailable) {
			if saveErr := s.persistArchiveResult(ctx, op, claim, map[string]any{
				"version_ref":  mustMarshal(&unavailable.Ref),
				"status":       string(memory.OperationRejected),
				"error_reason": memoryVersionUnavailableReason,
			}); saveErr != nil {
				return false, saveErr
			}
			op.VersionRef = &unavailable.Ref
			op.Status = memory.OperationRejected
			op.ErrorReason = memoryVersionUnavailableReason
		}
		return false, err
	}
	if err := s.persistArchiveResult(ctx, op, claim, map[string]any{
		"version_ref": mustMarshal(ref),
	}); err != nil {
		return false, err
	}
	op.VersionRef = ref
	return true, nil
}

func (s *Service) persistArchiveResult(
	ctx context.Context,
	op *memory.Operation,
	claim string,
	values map[string]any,
) error {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result := s.db.WithContext(saveCtx).Model(&table.MemoryOperation{}).
		Where("id = ? AND tenant_id = ? AND status = ? AND archive_claim = ? AND archive_until > clock_timestamp()",
			op.ID, op.TenantID, string(memory.OperationPending), claim).
		Updates(values)
	return requireUpdated(result)
}

// activate applies the archived version to the entry projection in one
// transaction. A concurrent winner ends this operation as a conflict; the
// archived version stays unadopted and never becomes active.
func (s *Service) activate(
	ctx context.Context,
	op *memory.Operation,
	doc *memory.Document,
) (*memory.Entry, error) {
	var entry *memory.Entry
	var rejected bool
	callbackSucceeded := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var opRow table.MemoryOperation
		if err := table.TenantQuery(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"})).
			Where("id = ? AND tenant_id = ?", op.ID, op.TenantID).
			Take(&opRow).Error; err != nil {
			return err
		}
		current := opRow.ToDomain()
		if current.Status == memory.OperationApplied {
			var err error
			entry, err = appliedEntry(current)
			callbackSucceeded = err == nil
			return err
		}
		if current.Status == memory.OperationRejected {
			if current.ErrorReason == memoryVersionUnavailableReason {
				if current.VersionRef == nil {
					return memory.ErrUnavailable
				}
				return &memory.VersionUnavailableError{Ref: *current.VersionRef}
			}
			return memory.ErrConflict
		}
		if current.Status != memory.OperationPending || current.VersionRef == nil && s.archive != nil {
			return memory.ErrPending
		}
		if doc.Scope.Tenant != current.TenantID {
			return memory.ErrConflict
		}
		if err := validateWriteScope(doc.Scope, current.OwnerID, current.ActorID); err != nil {
			return err
		}
		ref := current.VersionRef
		now := time.Now().Unix()
		switch current.Type {
		case memory.OperationCreate:
			entry = entryFromDocument(doc, current.OwnerID)
			entry.Binding = current.Binding
			entry.ActiveVersionRef = ref
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).
				Create(table.MemoryEntryFromDomain(entry))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				rejected = true
				if err := markRejected(tx, current, "SubjectConflict"); err != nil {
					return err
				}
				callbackSucceeded = true
				return nil
			}
		case memory.OperationUpdate, memory.OperationRestore:
			var row table.MemoryEntry
			actorPrivate := doc.Scope.Project == "" && current.ActorID == current.OwnerID
			if err := privateEntries(
				tx.Clauses(clause.Locking{Strength: "UPDATE"}),
				current.OwnerID,
				actorPrivate,
			).
				Where("tenant_id = ? AND id = ?", current.TenantID, current.EntryID).
				Take(&row).
				Error; err != nil {
				return mapError(err)
			}
			existing := row.ToDomain()
			if reason := revisionConflict(existing, current, doc); reason != "" {
				rejected = true
				if err := markRejected(tx, current, reason); err != nil {
					return err
				}
				callbackSucceeded = true
				return nil
			}
			entry = entryFromDocument(doc, current.OwnerID)
			entry.ID = row.ID
			entry.Revision = row.Revision + 1
			entry.Binding = current.Binding
			entry.ActiveVersionRef = ref
			if current.Type == memory.OperationRestore {
				entry.RetiredAt = nil
			} else {
				entry.RetiredAt = existing.RetiredAt
			}
			projected := table.MemoryEntryFromDomain(entry)
			result := tx.Model(&table.MemoryEntry{}).
				Where("tenant_id = ? AND id = ? AND revision = ?", current.TenantID, row.ID, current.ExpectedRevision).
				Updates(map[string]any{
					"summary":            projected.Summary,
					"body":               projected.Body,
					"conditions":         projected.Conditions,
					"source":             projected.Source,
					"output_visibility":  projected.OutputVisibility,
					"valid_until":        projected.ValidUntil,
					"binding":            projected.Binding,
					"active_version_ref": projected.ActiveVersionRef,
					"revision":           projected.Revision,
					"retired_at":         projected.RetiredAt,
					"updated_at":         time.Now(),
				})
			if err := requireUpdated(result); err != nil {
				return err
			}
		default:
			return memory.ErrInvalid
		}
		updates := map[string]any{
			"status":           string(memory.OperationApplied),
			"applied_revision": entry.Revision,
			"applied_at":       now,
		}
		if err := requireUpdated(tx.Model(&table.MemoryOperation{}).
			Where("id = ? AND tenant_id = ? AND status = ?", current.ID, current.TenantID, string(memory.OperationPending)).
			Updates(updates)); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return s.resolveCommittedOperation(ctx, op, err)
		}
		return nil, err
	}
	if rejected {
		return nil, memory.ErrConflict
	}
	return entry, nil
}

// resolveCommittedOperation reads the durable receipt after a transaction
// commit reported an error. The commit may still have reached PostgreSQL, so
// only the stored receipt can decide the operation's result. A receipt that is
// missing, pending, or does not match the original intent stays outcome-unknown
// and the caller must retry with the same request key.
func (s *Service) resolveCommittedOperation(
	ctx context.Context,
	op *memory.Operation,
	commitErr error,
) (*memory.Entry, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var row table.MemoryOperation
	if err := s.db.WithContext(readCtx).
		Where("tenant_id = ? AND owner_id = ? AND request_key = ?", op.TenantID, op.OwnerID, op.RequestKey).
		Take(&row).Error; err != nil {
		return nil, errors.Join(memory.ErrOperationOutcomeUnknown, commitErr, err)
	}
	stored := row.ToDomain()
	if stored.IntentDigest != op.IntentDigest || stored.ActorID != op.ActorID {
		return nil, errors.Join(memory.ErrOperationOutcomeUnknown, commitErr)
	}
	switch stored.Status {
	case memory.OperationApplied:
		entry, err := appliedEntry(stored)
		if err != nil {
			return nil, errors.Join(memory.ErrOperationOutcomeUnknown, commitErr, err)
		}
		return entry, nil
	case memory.OperationRejected:
		if stored.ErrorReason == memoryVersionUnavailableReason {
			if stored.VersionRef == nil {
				return nil, errors.Join(memory.ErrOperationOutcomeUnknown, commitErr)
			}
			return nil, &memory.VersionUnavailableError{Ref: *stored.VersionRef}
		}
		return nil, memory.ErrConflict
	default:
		return nil, errors.Join(memory.ErrOperationOutcomeUnknown, commitErr)
	}
}

// markRejected persists the rejection inside the caller's transaction so the
// durable receipt records why the operation lost. The transaction commits
// with the rejected receipt; the caller surfaces ErrConflict after commit.
func markRejected(tx *gorm.DB, op *memory.Operation, reason string) error {
	updates := map[string]any{
		"status":       string(memory.OperationRejected),
		"error_reason": reason,
	}
	return requireUpdated(tx.Model(&table.MemoryOperation{}).
		Where("id = ? AND tenant_id = ? AND status = ?", op.ID, op.TenantID, string(memory.OperationPending)).
		Updates(updates))
}

// Retire disables an active entry in one local transaction.
func (s *Service) Retire(
	ctx context.Context,
	owner, actor, requestKey, entryID string,
	expectedRevision int64,
) (*memory.Entry, error) {
	scope, err := requireScope(ctx)
	if err != nil {
		return nil, err
	}
	if owner == "" || actor == "" || requestKey == "" || entryID == "" || expectedRevision < 1 {
		return nil, memory.ErrInvalid
	}
	op := &memory.Operation{
		ID: newOperationID(), TenantID: scope.TenantID, OwnerID: owner, RequestKey: requestKey,
		IntentDigest: digestIntent(memory.OperationRetire, entryID, expectedRevision, nil),
		Type:         memory.OperationRetire, EntryID: entryID, ExpectedRevision: expectedRevision,
		ActorID: actor, Status: memory.OperationPending,
	}
	var entry *memory.Entry
	var rejected bool
	callbackSucceeded := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(table.MemoryOperationFromDomain(op)).Error; err != nil {
			return err
		}
		var receipt table.MemoryOperation
		if err := table.TenantQuery(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"})).
			Where("owner_id = ? AND request_key = ?", owner, requestKey).Take(&receipt).Error; err != nil {
			return err
		}
		stored := receipt.ToDomain()
		if stored.IntentDigest != op.IntentDigest || stored.ActorID != actor {
			return memory.ErrConflict
		}
		op = stored
		var row table.MemoryEntry
		query := privateEntries(
			table.TenantQuery(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"})),
			owner, scope.ProjectID == "" && actor == owner,
		)
		if err := query.Where("id = ?", entryID).Take(&row).Error; err != nil {
			return mapError(err)
		}
		if op.Status == memory.OperationApplied {
			var err error
			entry, err = appliedEntry(op)
			callbackSucceeded = err == nil
			return err
		}
		if op.Status == memory.OperationRejected {
			return memory.ErrConflict
		}
		entry = row.ToDomain()
		if entry.RetiredAt != nil {
			rejected = true
			if err := markRejected(tx, op, "EntryRetired"); err != nil {
				return err
			}
			callbackSucceeded = true
			return nil
		}
		if entry.Revision != expectedRevision {
			rejected = true
			if err := markRejected(tx, op, "RevisionConflict"); err != nil {
				return err
			}
			callbackSucceeded = true
			return nil
		}
		now := time.Now().Unix()
		entry.RetiredAt = &now
		entry.Revision++
		if err := requireUpdated(table.TenantQuery(ctx, tx.Model(&table.MemoryEntry{})).
			Where("id = ? AND revision = ? AND retired_at IS NULL", row.ID, expectedRevision).
			Updates(map[string]any{
				"retired_at": now,
				"revision":   entry.Revision,
				"updated_at": time.Now(),
			})); err != nil {
			return err
		}
		doc := documentFromEntry(entry)
		if err := requireUpdated(table.TenantQuery(ctx, tx.Model(&table.MemoryOperation{})).
			Where("id = ? AND status = ?", op.ID, string(memory.OperationPending)).
			Updates(map[string]any{
				"status": string(memory.OperationApplied), "applied_revision": entry.Revision,
				"applied_at": now, "document": mustMarshal(doc),
				"binding": row.Binding, "version_ref": row.ActiveVersionRef,
			})); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil {
		if callbackSucceeded {
			return s.resolveCommittedOperation(ctx, op, err)
		}
		return nil, err
	}
	if rejected {
		return nil, memory.ErrConflict
	}
	return entry, nil
}

// RecallTx reads active entries matching the scope and query inside the caller's transaction.
func (s *Service) RecallTx(
	ctx context.Context,
	tx *gorm.DB,
	scope memory.Scope,
	agentID string,
	query string,
	filter memory.RecallFilter,
) ([]memory.Entry, error) {
	if scope.Owner == "" {
		return nil, memory.ErrInvalid
	}
	var rows []table.MemoryEntry
	q := table.TenantQuery(ctx, tx).Where(
		"owner_id = ? AND scope_kind = ? AND tenant_id = ? AND project_id = ? AND actor_id = ? AND retired_at IS NULL",
		scope.Owner, string(scope.Kind), scope.Tenant, scope.Project, scope.Actor,
	)
	switch scope.Kind {
	case memory.ScopeActorPrivate:
		request, err := requireScope(ctx)
		if err != nil || request.TenantID != scope.Tenant || request.ProjectID != "" ||
			scope.Project != "" || scope.Actor != scope.Owner {
			return nil, memory.ErrForbidden
		}
		q = q.Where("output_visibility = ?", memory.OutputPrivate)
	case memory.ScopeActorProject:
		request, err := requireScope(ctx)
		if err != nil || request.TenantID != scope.Tenant || request.ProjectID != scope.Project ||
			scope.Project == "" || scope.Actor != scope.Owner || !access.HasProjectRead(ctx, scope.Owner, request) {
			return nil, memory.ErrForbidden
		}
		q = q.Where("output_visibility = ?", memory.OutputProject)
	}
	if agentID != "" {
		q = q.Where("agent_id = ? OR agent_id = ''", agentID)
	} else {
		q = q.Where("agent_id = ''")
	}
	if err := q.Order("id").Find(&rows).Error; err != nil {
		return nil, mapError(err)
	}
	entries := make([]memory.Entry, 0, len(rows))
	for i := range rows {
		entry := rows[i].ToDomain()
		if entry.ValidUntil != nil && *entry.ValidUntil < time.Now().Unix() {
			continue
		}
		entries = append(entries, *entry)
	}
	return selectEntries(entries, query, filter), nil
}

// selectEntries applies tag and term matching, stable ordering and the
// entry/byte bounds of one recall. The query text is tokenized and every
// query term plus every explicit filter term must appear in the entry text;
// an empty query without explicit terms matches every active entry in scope.
func selectEntries(entries []memory.Entry, query string, filter memory.RecallFilter) []memory.Entry {
	terms := append(memory.MatchTerms(query), filter.Terms...)
	maxEntries := filter.MaxEntries
	if maxEntries == 0 {
		maxEntries = memory.DefaultRecallMaxEntries
	}
	maxBytes := filter.MaxBytes
	if maxBytes == 0 {
		maxBytes = memory.DefaultRecallMaxBytes
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Revision != entries[j].Revision {
			return entries[i].Revision > entries[j].Revision
		}
		return entries[i].ID < entries[j].ID
	})
	selected := make([]memory.Entry, 0, len(entries))
	bytes := 0
	for i := range entries {
		entry := &entries[i]
		if !entry.Matches(terms, filter.Tags) {
			continue
		}
		if maxEntries >= 0 && len(selected) >= maxEntries {
			break
		}
		size := entry.MaterialBytes()
		if maxBytes >= 0 && bytes+size > maxBytes {
			continue
		}
		selected = append(selected, *entry)
		bytes += size
	}
	return selected
}

func requireScope(ctx context.Context) (access.Scope, error) {
	scope, ok := access.FromContext(ctx)
	if !ok || scope.TenantID == "" || strings.TrimSpace(scope.TenantID) != scope.TenantID {
		return access.Scope{}, memory.ErrForbidden
	}
	return scope, nil
}

func authorizeDocument(ctx context.Context, owner, actor string, doc *memory.Document) error {
	scope, err := requireScope(ctx)
	if err != nil {
		return err
	}
	if doc.Scope.Tenant != scope.TenantID || doc.Scope.Project != scope.ProjectID {
		return memory.ErrForbidden
	}
	if doc.Scope.Kind == memory.ScopeActorPrivate {
		if scope.ProjectID != "" || actor != owner || (doc.Scope.Actor != "" && doc.Scope.Actor != actor) {
			return memory.ErrForbidden
		}
		doc.Scope.Actor = actor
	}
	doc.Scope.Owner = owner
	return nil
}

func validateDocument(doc *memory.Document) error {
	if doc.Kind != memory.KindFact && doc.Kind != memory.KindPreference && doc.Kind != memory.KindProcedure {
		return memory.ErrInvalid
	}
	if doc.SubjectKey == "" && doc.Kind != memory.KindFact {
		return memory.ErrInvalid
	}
	if len(doc.Summary) == 0 || len(doc.Summary) > 200 {
		return memory.ErrInvalid
	}
	if !json.Valid(doc.Body) || string(doc.Body) == "null" || len(mustMarshal(doc)) > 256*1024 {
		return memory.ErrInvalid
	}
	if doc.OutputVisibility == "" {
		doc.OutputVisibility = memory.OutputPrivate
	}
	if doc.OutputVisibility != memory.OutputPrivate && doc.OutputVisibility != memory.OutputProject {
		return memory.ErrInvalid
	}
	if doc.Scope.Kind == memory.ScopeActorPrivate && doc.OutputVisibility != memory.OutputPrivate {
		return memory.ErrInvalid
	}
	if doc.Source.Class != memory.SourceHumanConfirmed {
		return memory.ErrInvalid
	}
	if len(doc.Source.Evidence) != 0 {
		return memory.ErrInvalid
	}
	return nil
}

func validateWriteScope(scope memory.Scope, owner, actor string) error {
	if scope.Owner == owner {
		switch scope.Kind {
		case memory.ScopeOwnerPrivate:
			if scope.Actor == "" {
				return nil
			}
		case memory.ScopeActorPrivate:
			if actor == owner && actor != "" && scope.Actor == actor && scope.Project == "" {
				return nil
			}
		}
	}
	return memory.ErrInvalid
}

func privateEntries(tx *gorm.DB, owner string, actorPrivate bool) *gorm.DB {
	query := tx.Where("owner_id = ?", owner)
	if actorPrivate {
		return query.Where(
			"((scope_kind = ? AND actor_id = '') OR (scope_kind = ? AND project_id = '' AND actor_id = ?))",
			memory.ScopeOwnerPrivate, memory.ScopeActorPrivate, owner,
		)
	}
	return query.Where("scope_kind = ? AND actor_id = ''", memory.ScopeOwnerPrivate)
}

func revisionConflict(entry *memory.Entry, op *memory.Operation, doc *memory.Document) string {
	if entry.Scope != doc.Scope || entry.AgentID != doc.AgentID || entry.Kind != doc.Kind ||
		entry.SubjectKey != doc.SubjectKey {
		return "IdentityConflict"
	}
	if entry.Revision != op.ExpectedRevision {
		return "RevisionConflict"
	}
	if op.Type == memory.OperationUpdate && entry.RetiredAt != nil {
		return "EntryRetired"
	}
	if op.Type == memory.OperationRestore && entry.RetiredAt == nil {
		return "EntryActive"
	}
	if op.Type == memory.OperationRestore && doc.ValidUntil != nil && *doc.ValidUntil <= time.Now().Unix() {
		return "EntryExpired"
	}
	return ""
}

func requireUpdated(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return memory.ErrPending
	}
	return nil
}

func appliedEntry(op *memory.Operation) (*memory.Entry, error) {
	doc, err := operationDocument(op)
	if err != nil || op.AppliedRevision == nil {
		return nil, memory.ErrInvalid
	}
	entry := entryFromDocument(doc, op.OwnerID)
	entry.Revision = *op.AppliedRevision
	entry.Binding = op.Binding
	entry.ActiveVersionRef = op.VersionRef
	if op.Type == memory.OperationRetire {
		entry.RetiredAt = op.AppliedAt
	}
	return entry, nil
}

func documentFromEntry(entry *memory.Entry) *memory.Document {
	return &memory.Document{
		SchemaVersion: schemaVersion, EntryID: entry.ID, Scope: entry.Scope,
		AgentID: entry.AgentID, Kind: entry.Kind, SubjectKey: entry.SubjectKey,
		Summary: entry.Summary, Body: entry.Body, Conditions: entry.Conditions,
		OutputVisibility: entry.OutputVisibility, Source: entry.Source, ValidUntil: entry.ValidUntil,
	}
}

func entryFromDocument(doc *memory.Document, owner string) *memory.Entry {
	scope := doc.Scope
	scope.Owner = owner
	return &memory.Entry{
		ID:               doc.EntryID,
		Scope:            scope,
		AgentID:          doc.AgentID,
		Kind:             doc.Kind,
		SubjectKey:       doc.SubjectKey,
		Summary:          doc.Summary,
		Body:             doc.Body,
		Conditions:       doc.Conditions,
		OutputVisibility: doc.OutputVisibility,
		Source:           doc.Source,
		ValidUntil:       doc.ValidUntil,
		Revision:         1,
	}
}

func operationFromDocument(
	doc *memory.Document,
	owner, actor, requestKey string,
	opType memory.OperationType,
	expectedRevision int64,
) *memory.Operation {
	digestEntryID := doc.EntryID
	if opType == memory.OperationCreate {
		digestEntryID = ""
	}
	return &memory.Operation{
		ID:               newOperationID(),
		TenantID:         doc.Scope.Tenant,
		OwnerID:          owner,
		RequestKey:       requestKey,
		IntentDigest:     digestIntent(opType, digestEntryID, expectedRevision, doc),
		Type:             opType,
		EntryID:          doc.EntryID,
		ExpectedRevision: expectedRevision,
		ActorID:          actor,
		Document:         mustMarshal(doc),
		Status:           memory.OperationPending,
	}
}

func operationDocument(op *memory.Operation) (*memory.Document, error) {
	var doc memory.Document
	if err := json.Unmarshal(op.Document, &doc); err != nil {
		return nil, memory.ErrInvalid
	}
	return &doc, nil
}

func digestIntent(opType memory.OperationType, entryID string, expectedRevision int64, doc *memory.Document) string {
	// The digest covers only caller-supplied intent so a replayed request key
	// with identical arguments matches even though the service assigns the
	// entry id, schema version and source actor on the first attempt.
	var intent *memory.Document
	if doc != nil {
		redacted := *doc
		redacted.EntryID = ""
		redacted.SchemaVersion = 0
		redacted.Source.Actor = ""
		intent = &redacted
	}
	payload, _ := json.Marshal(struct {
		Type             memory.OperationType `json:"type"`
		EntryID          string               `json:"entryID"`
		ExpectedRevision int64                `json:"expectedRevision"`
		Document         *memory.Document     `json:"document,omitempty"`
	}{Type: opType, EntryID: entryID, ExpectedRevision: expectedRevision, Document: intent})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func newEntryID() string {
	return "mem_" + durable.NewID()
}

func collectionKey(entryID string) string {
	sum := sha256.Sum256([]byte(entryID))
	return "memcol_" + hex.EncodeToString(sum[:28])
}

func newOperationID() string {
	return "mop_" + durable.NewID()
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func mapError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return memory.ErrNotFound
	}
	return err
}

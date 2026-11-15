// Package memory implements the Memory business service: CRUD, retire/restore,
// and recall for Run acceptance. The first version stores the complete payload
// locally and does not call artifact-go.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const schemaVersion = 1

// Service owns the transactional behavior for Memory CRUD and recall.
type Service struct {
	db *gorm.DB
}

// NewService borrows the database owned by the caller.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Create writes a new memory entry and its operation receipt in one transaction.
func (s *Service) Create(
	ctx context.Context,
	owner, actor, requestKey string,
	doc memory.Document,
) (*memory.Entry, error) {
	if owner == "" || actor == "" || requestKey == "" {
		return nil, memory.ErrInvalid
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	doc.SchemaVersion = schemaVersion
	doc.Source.Actor = actor
	doc.EntryID = newEntryID()
	entry := entryFromDocument(&doc, owner)
	op := operationFromDocument(&doc, owner, actor, requestKey, memory.OperationCreate, 0)

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(table.MemoryEntryFromDomain(entry)).Error; err != nil {
			return err
		}
		op.Status = memory.OperationApplied
		op.AppliedRevision = &entry.Revision
		now := time.Now().Unix()
		op.AppliedAt = &now
		return tx.Create(table.MemoryOperationFromDomain(op)).Error
	})
	if err != nil {
		return nil, mapError(err)
	}
	return entry, nil
}

// Get returns one entry if the caller is authorized.
func (s *Service) Get(ctx context.Context, owner, id string) (*memory.Entry, error) {
	var row table.MemoryEntry
	if err := s.db.WithContext(ctx).Where("owner_id = ? AND id = ?", owner, id).Take(&row).Error; err != nil {
		return nil, mapError(err)
	}
	return row.ToDomain(), nil
}

// List returns entries owned by the caller, newest first.
func (s *Service) List(ctx context.Context, owner, after string, limit int) ([]memory.Entry, error) {
	if limit < 1 || limit > 100 {
		return nil, memory.ErrInvalid
	}
	q := s.db.WithContext(ctx).Where("owner_id = ? AND id > ?", owner, after)
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
	if owner == "" || actor == "" || requestKey == "" || entryID == "" {
		return nil, memory.ErrInvalid
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	doc.SchemaVersion = schemaVersion
	doc.Source.Actor = actor
	doc.EntryID = entryID

	var entry *memory.Entry
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row table.MemoryEntry
		if err := tx.Where("owner_id = ? AND id = ?", owner, entryID).Take(&row).Error; err != nil {
			return mapError(err)
		}
		entry = row.ToDomain()
		if entry.RetiredAt != nil {
			return memory.ErrConflict
		}
		if entry.Revision != expectedRevision {
			return memory.ErrConflict
		}
		entry = entryFromDocument(&doc, owner)
		entry.ID = row.ID
		entry.Revision = row.Revision + 1
		if err := tx.Model(&table.MemoryEntry{}).
			Where("id = ?", row.ID).
			Updates(table.MemoryEntryFromDomain(entry)).
			Error; err != nil {
			return err
		}
		op := operationFromDocument(&doc, owner, actor, requestKey, memory.OperationUpdate, expectedRevision)
		op.Status = memory.OperationApplied
		op.AppliedRevision = &entry.Revision
		now := time.Now().Unix()
		op.AppliedAt = &now
		return tx.Create(table.MemoryOperationFromDomain(op)).Error
	})
	if err != nil {
		return nil, mapError(err)
	}
	return entry, nil
}

// Retire disables an active entry.
func (s *Service) Retire(
	ctx context.Context,
	owner, actor, requestKey, entryID string,
	expectedRevision int64,
) (*memory.Entry, error) {
	if owner == "" || actor == "" || requestKey == "" || entryID == "" {
		return nil, memory.ErrInvalid
	}
	var entry *memory.Entry
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row table.MemoryEntry
		if err := tx.Where("owner_id = ? AND id = ?", owner, entryID).Take(&row).Error; err != nil {
			return mapError(err)
		}
		entry = row.ToDomain()
		if entry.RetiredAt != nil {
			return memory.ErrConflict
		}
		if entry.Revision != expectedRevision {
			return memory.ErrConflict
		}
		now := time.Now().Unix()
		entry.RetiredAt = &now
		entry.Revision++
		if err := tx.Model(&table.MemoryEntry{}).Where("id = ?", row.ID).Updates(map[string]any{
			"retired_at": now,
			"revision":   entry.Revision,
		}).Error; err != nil {
			return err
		}
		op := &memory.Operation{
			ID:               newOperationID(),
			OwnerID:          owner,
			RequestKey:       requestKey,
			IntentDigest:     digestIntent(memory.OperationRetire, entryID, expectedRevision, nil),
			Type:             memory.OperationRetire,
			EntryID:          entryID,
			ExpectedRevision: expectedRevision,
			ActorID:          actor,
			Status:           memory.OperationApplied,
			AppliedRevision:  &entry.Revision,
			AppliedAt:        &now,
		}
		return tx.Create(table.MemoryOperationFromDomain(op)).Error
	})
	if err != nil {
		return nil, mapError(err)
	}
	return entry, nil
}

// Restore re-enables a retired entry with new complete content.
func (s *Service) Restore(
	ctx context.Context,
	owner, actor, requestKey, entryID string,
	expectedRevision int64,
	doc memory.Document,
) (*memory.Entry, error) {
	if owner == "" || actor == "" || requestKey == "" || entryID == "" {
		return nil, memory.ErrInvalid
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	doc.SchemaVersion = schemaVersion
	doc.Source.Actor = actor
	doc.EntryID = entryID

	var entry *memory.Entry
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row table.MemoryEntry
		if err := tx.Where("owner_id = ? AND id = ?", owner, entryID).Take(&row).Error; err != nil {
			return mapError(err)
		}
		entry = row.ToDomain()
		if entry.RetiredAt == nil {
			return memory.ErrConflict
		}
		if entry.Revision != expectedRevision {
			return memory.ErrConflict
		}
		entry = entryFromDocument(&doc, owner)
		entry.ID = row.ID
		entry.Revision = row.Revision + 1
		entry.RetiredAt = nil
		if err := tx.Model(&table.MemoryEntry{}).
			Where("id = ?", row.ID).
			Updates(table.MemoryEntryFromDomain(entry)).
			Error; err != nil {
			return err
		}
		op := operationFromDocument(&doc, owner, actor, requestKey, memory.OperationRestore, expectedRevision)
		op.Status = memory.OperationApplied
		op.AppliedRevision = &entry.Revision
		now := time.Now().Unix()
		op.AppliedAt = &now
		return tx.Create(table.MemoryOperationFromDomain(op)).Error
	})
	if err != nil {
		return nil, mapError(err)
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
) ([]memory.Entry, error) {
	if scope.Owner == "" {
		return nil, memory.ErrInvalid
	}
	var rows []table.MemoryEntry
	q := tx.WithContext(ctx).Where(
		"owner_id = ? AND scope_kind = ? AND tenant_id = ? AND project_id = ? AND actor_id = ? AND retired_at IS NULL",
		scope.Owner, string(scope.Kind), scope.Tenant, scope.Project, scope.Actor,
	)
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
	return entries, nil
}

func validateDocument(doc *memory.Document) error {
	if doc.Kind == "" || doc.SubjectKey == "" && doc.Kind != memory.KindFact {
		return memory.ErrInvalid
	}
	if len(doc.Summary) == 0 || len(doc.Summary) > 200 {
		return memory.ErrInvalid
	}
	if len(doc.Body) == 0 {
		return memory.ErrInvalid
	}
	if doc.OutputVisibility == "" {
		doc.OutputVisibility = memory.OutputPrivate
	}
	if doc.OutputVisibility != memory.OutputPrivate && doc.OutputVisibility != memory.OutputProject {
		return memory.ErrInvalid
	}
	if doc.Source.Class != memory.SourceHumanConfirmed {
		return memory.ErrInvalid
	}
	return nil
}

func entryFromDocument(doc *memory.Document, owner string) *memory.Entry {
	return &memory.Entry{
		ID:               doc.EntryID,
		Scope:            doc.Scope,
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
	return &memory.Operation{
		ID:               newOperationID(),
		OwnerID:          owner,
		RequestKey:       requestKey,
		IntentDigest:     digestIntent(opType, doc.EntryID, expectedRevision, doc),
		Type:             opType,
		EntryID:          doc.EntryID,
		ExpectedRevision: expectedRevision,
		ActorID:          actor,
		Document:         mustMarshal(doc),
		Status:           memory.OperationPending,
	}
}

func digestIntent(opType memory.OperationType, entryID string, expectedRevision int64, doc *memory.Document) string {
	payload, _ := json.Marshal(struct {
		Type             memory.OperationType `json:"type"`
		EntryID          string               `json:"entryID"`
		ExpectedRevision int64                `json:"expectedRevision"`
		Document         *memory.Document     `json:"document,omitempty"`
	}{Type: opType, EntryID: entryID, ExpectedRevision: expectedRevision, Document: doc})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func newEntryID() string {
	return "mem_" + durable.NewID()
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

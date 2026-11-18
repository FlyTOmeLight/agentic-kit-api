package memory

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

type versionReader interface {
	ReadVersion(context.Context, memory.VersionRef) (*memory.Document, error)
}

func historyQuery(db *gorm.DB, entry *memory.Entry) *gorm.DB {
	return db.Model(&table.MemoryOperation{}).
		Where("tenant_id = ? AND owner_id = ? AND entry_id = ? AND status = ? AND op_type IN ?",
			entry.Scope.Tenant, entry.Scope.Owner, entry.ID, string(memory.OperationApplied),
			[]string{
				string(memory.OperationCreate),
				string(memory.OperationUpdate),
				string(memory.OperationRestore),
			}).
		Where("applied_revision IS NOT NULL AND applied_at IS NOT NULL AND applied_revision <= ?", entry.Revision).
		Where("version_ref IS NOT NULL AND version_ref <> 'null'::jsonb")
}

func versionSummary(entry *memory.Entry, row *table.MemoryOperation) (memory.VersionSummary, error) {
	op := row.ToDomain()
	if op.VersionRef == nil || op.AppliedRevision == nil || op.AppliedAt == nil || entry.Binding == nil {
		return memory.VersionSummary{}, fmt.Errorf("%w: invalid version receipt", memory.ErrUnavailable)
	}
	ref, binding := op.VersionRef, entry.Binding
	if ref.StoreID != binding.StoreID || ref.TenantID != binding.TenantID ||
		ref.ProjectID != binding.ProjectID || ref.UserID != binding.UserID || ref.ArtifactID != binding.ArtifactID ||
		ref.VersionID == "" || ref.Sequence <= 0 || ref.VersionURI == "" {
		return memory.VersionSummary{}, fmt.Errorf(
			"%w: version receipt does not match entry binding",
			memory.ErrUnavailable,
		)
	}
	return memory.VersionSummary{
		VersionRef: *ref, AppliedRevision: *op.AppliedRevision, Actor: op.ActorID, AppliedAt: *op.AppliedAt,
		Active: entry.RetiredAt == nil && entry.ActiveVersionRef != nil && *entry.ActiveVersionRef == *ref,
	}, nil
}

// ListVersions lists adopted archived versions without accessing the archive.
func (s *Service) ListVersions(
	ctx context.Context,
	owner, id string,
	afterRevision int64,
	limit int,
) (*memory.VersionPage, error) {
	if afterRevision < 0 || limit < 1 || limit > 100 {
		return nil, memory.ErrInvalid
	}
	entry, err := s.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	query := historyQuery(s.db.WithContext(ctx), entry)
	if afterRevision > 0 {
		query = query.Where("applied_revision < ?", afterRevision)
	}
	var rows []table.MemoryOperation
	if err := query.Order("applied_revision DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, mapError(err)
	}
	page := &memory.VersionPage{
		Items:            make([]memory.VersionSummary, 0, limit),
		ActiveVersionRef: entry.ActiveVersionRef,
		RetiredAt:        entry.RetiredAt,
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	for i := range rows {
		item, err := versionSummary(entry, &rows[i])
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, item)
	}
	if more {
		page.NextAfterRevision = page.Items[len(page.Items)-1].AppliedRevision
	}
	return page, nil
}

// GetVersion reads an adopted version after checking the entry's ownership.
func (s *Service) GetVersion(ctx context.Context, owner, id, versionID string) (*memory.Version, error) {
	entry, err := s.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	var row table.MemoryOperation
	if err := historyQuery(s.db.WithContext(ctx), entry).
		Where("version_ref->>'versionID' = ?", versionID).
		Order("applied_revision DESC").Take(&row).Error; err != nil {
		return nil, mapError(err)
	}
	summary, err := versionSummary(entry, &row)
	if err != nil {
		return nil, err
	}
	reader, ok := s.archive.(versionReader)
	if !ok {
		return nil, memory.ErrUnavailable
	}
	doc, err := reader.ReadVersion(ctx, summary.VersionRef)
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.EntryID != entry.ID || doc.Scope != entry.Scope ||
		doc.AgentID != entry.AgentID || doc.Kind != entry.Kind || doc.SubjectKey != entry.SubjectKey ||
		doc.SchemaVersion != schemaVersion || validateDocument(doc) != nil {
		return nil, fmt.Errorf("%w: archived document identity or content is invalid", memory.ErrUnavailable)
	}
	return &memory.Version{
		VersionSummary:   summary,
		Document:         *doc,
		ActiveVersionRef: entry.ActiveVersionRef,
		RetiredAt:        entry.RetiredAt,
	}, nil
}

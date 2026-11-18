package table

import (
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

// MemoryEntry stores the local projection of one active memory. The complete
// payload projection is stored alongside for local recall without a remote read.
type MemoryEntry struct {
	ID               string          `gorm:"type:text;primaryKey;not null"`
	OwnerID          string          `gorm:"type:text;not null;index:memory_entry_owner,priority:1"`
	ScopeKind        string          `gorm:"type:text;not null;check:memory_entry_scope_kind,scope_kind IN ('OwnerPrivate','ActorPrivate','ActorProject','ProjectShared')"`
	TenantID         string          `gorm:"type:text;not null;default:''"`
	ProjectID        string          `gorm:"type:text;not null;default:''"`
	ActorID          string          `gorm:"type:text;not null;default:''"`
	AgentID          string          `gorm:"type:text;not null;default:''"`
	Kind             string          `gorm:"type:text;not null;check:memory_entry_kind,kind IN ('Fact','Preference','Procedure')"`
	SubjectKey       string          `gorm:"type:text;not null;default:''"`
	Summary          string          `gorm:"type:text;not null"`
	Body             json.RawMessage `gorm:"serializer:json;type:jsonb;not null"`
	Conditions       json.RawMessage `gorm:"serializer:json;type:jsonb;not null;default:'{}'"`
	OutputVisibility string          `gorm:"type:text;not null;default:'Private';check:memory_entry_output_visibility,output_visibility IN ('Private','Project')"`
	Source           json.RawMessage `gorm:"serializer:json;type:jsonb;not null"`
	ValidUntil       *int64          `gorm:"type:bigint"`
	ActiveVersionRef json.RawMessage `gorm:"serializer:json;type:jsonb"`
	Revision         int64           `gorm:"type:bigint;not null;default:1"`
	RetiredAt        *int64          `gorm:"type:bigint"`
	CreatedAt        time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false"`
	UpdatedAt        time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoUpdateTime:false"`
}

func (MemoryEntry) TableName() string { return "memory_entry" }

// MemoryEntrySubjectUnique enforces one active entry per (scope, agent, kind, subject).
// Entries with an empty subjectKey are not constrained.
const MemoryEntrySubjectUnique = `
CREATE UNIQUE INDEX IF NOT EXISTS memory_entry_subject_unique
ON memory_entry (owner_id, scope_kind, tenant_id, project_id, actor_id, agent_id, kind, subject_key)
WHERE subject_key != '' AND retired_at IS NULL;
`

// MemoryOperation is a durable receipt for one write intent.
// It enables idempotent replay and cross-service recovery.
type MemoryOperation struct {
	ID               string          `gorm:"type:text;primaryKey;not null"`
	TenantID         string          `gorm:"type:text;not null;default:'';uniqueIndex:memory_operation_tenant_owner_key,priority:1"`
	OwnerID          string          `gorm:"type:text;not null;index:memory_operation_owner,priority:1;uniqueIndex:memory_operation_tenant_owner_key,priority:2"`
	RequestKey       string          `gorm:"type:text;not null;uniqueIndex:memory_operation_tenant_owner_key,priority:3"`
	IntentDigest     string          `gorm:"type:text;not null"`
	OpType           string          `gorm:"type:text;not null;check:memory_operation_op_type,op_type IN ('Create','Update','Restore','Retire')"`
	EntryID          string          `gorm:"type:text;not null"`
	ExpectedRevision int64           `gorm:"type:bigint;not null"`
	ActorID          string          `gorm:"type:text;not null"`
	Document         json.RawMessage `gorm:"serializer:json;type:jsonb"`
	Status           string          `gorm:"type:text;not null;default:'Pending';check:memory_operation_status,status IN ('Pending','Applied','Rejected')"`
	AppliedRevision  *int64          `gorm:"type:bigint"`
	AppliedAt        *int64          `gorm:"type:bigint"`
	ErrorReason      string          `gorm:"type:text;not null;default:''"`
	CreatedAt        time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false"`
	UpdatedAt        time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoUpdateTime:false"`
}

func (MemoryOperation) TableName() string { return "memory_operation" }

// MemoryEntryFromDomain converts the pure domain value to its table row.
func MemoryEntryFromDomain(e *memory.Entry) *MemoryEntry {
	return &MemoryEntry{
		ID:               e.ID,
		OwnerID:          e.Scope.Owner,
		ScopeKind:        string(e.Scope.Kind),
		TenantID:         e.Scope.Tenant,
		ProjectID:        e.Scope.Project,
		ActorID:          e.Scope.Actor,
		AgentID:          e.AgentID,
		Kind:             string(e.Kind),
		SubjectKey:       e.SubjectKey,
		Summary:          e.Summary,
		Body:             e.Body,
		Conditions:       mustMarshal(e.Conditions),
		OutputVisibility: string(e.OutputVisibility),
		Source:           mustMarshal(e.Source),
		ValidUntil:       e.ValidUntil,
		ActiveVersionRef: mustMarshal(e.ActiveVersionRef),
		Revision:         e.Revision,
		RetiredAt:        e.RetiredAt,
	}
}

// ToDomain converts the table row back to the pure domain value.
func (r *MemoryEntry) ToDomain() *memory.Entry {
	var conditions memory.Conditions
	_ = json.Unmarshal(r.Conditions, &conditions)
	var source memory.Source
	_ = json.Unmarshal(r.Source, &source)
	var ref *memory.VersionRef
	if len(r.ActiveVersionRef) > 0 {
		var v memory.VersionRef
		if json.Unmarshal(r.ActiveVersionRef, &v) == nil {
			ref = &v
		}
	}
	return &memory.Entry{
		ID: r.ID,
		Scope: memory.Scope{
			Kind:    memory.ScopeKind(r.ScopeKind),
			Owner:   r.OwnerID,
			Tenant:  r.TenantID,
			Project: r.ProjectID,
			Actor:   r.ActorID,
		},
		AgentID:          r.AgentID,
		Kind:             memory.Kind(r.Kind),
		SubjectKey:       r.SubjectKey,
		Summary:          r.Summary,
		Body:             r.Body,
		Conditions:       conditions,
		OutputVisibility: memory.OutputVisibility(r.OutputVisibility),
		Source:           source,
		ValidUntil:       r.ValidUntil,
		ActiveVersionRef: ref,
		Revision:         r.Revision,
		RetiredAt:        r.RetiredAt,
	}
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// MemoryOperationFromDomain converts the operation to its table row.
func MemoryOperationFromDomain(op *memory.Operation) *MemoryOperation {
	return &MemoryOperation{
		ID:               op.ID,
		TenantID:         op.TenantID,
		OwnerID:          op.OwnerID,
		RequestKey:       op.RequestKey,
		IntentDigest:     op.IntentDigest,
		OpType:           string(op.Type),
		EntryID:          op.EntryID,
		ExpectedRevision: op.ExpectedRevision,
		ActorID:          op.ActorID,
		Document:         op.Document,
		Status:           string(op.Status),
		AppliedRevision:  op.AppliedRevision,
		AppliedAt:        op.AppliedAt,
		ErrorReason:      op.ErrorReason,
	}
}

// ToDomain converts the operation row back to the pure domain value.
func (r *MemoryOperation) ToDomain() *memory.Operation {
	return &memory.Operation{
		ID:               r.ID,
		TenantID:         r.TenantID,
		OwnerID:          r.OwnerID,
		RequestKey:       r.RequestKey,
		IntentDigest:     r.IntentDigest,
		Type:             memory.OperationType(r.OpType),
		EntryID:          r.EntryID,
		ExpectedRevision: r.ExpectedRevision,
		ActorID:          r.ActorID,
		Document:         r.Document,
		Status:           memory.OperationStatus(r.Status),
		AppliedRevision:  r.AppliedRevision,
		AppliedAt:        r.AppliedAt,
		ErrorReason:      r.ErrorReason,
	}
}

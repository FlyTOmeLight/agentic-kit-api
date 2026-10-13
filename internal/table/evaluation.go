package table

import (
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Evaluation struct {
	ID          string                `gorm:"type:text;primaryKey;not null"`
	OwnerID     string                `gorm:"type:text;not null;uniqueIndex:evaluation_request,priority:1;index:evaluation_scope,priority:1"`
	RequestKey  string                `gorm:"type:text;not null;uniqueIndex:evaluation_request,priority:2"`
	RequestHash string                `gorm:"type:text;not null"`
	TenantID    string                `gorm:"type:text;not null;index:evaluation_scope,priority:2"`
	ProjectID   string                `gorm:"type:text;not null;index:evaluation_scope,priority:3"`
	ActorID     string                `gorm:"type:text;not null"`
	Status      string                `gorm:"type:text;not null;check:evaluation_status,status IN ('Running','NeedsAttention','Completed','Canceled');index:evaluation_pending,where:status = 'Running' OR status = 'NeedsAttention'"`
	Suite       sdk.EvaluationSuite   `gorm:"serializer:json;type:jsonb;not null"`
	SuiteDigest string                `gorm:"type:text;not null"`
	Report      *sdk.EvaluationReport `gorm:"serializer:json;type:jsonb"`
	CreatedAt   time.Time             `gorm:"type:timestamptz;not null"`
	UpdatedAt   time.Time             `gorm:"type:timestamptz;not null"`
}

func (Evaluation) TableName() string { return "evaluation_experiment" }

type EvaluationTrial struct {
	ID           string                        `gorm:"type:text;primaryKey;not null"`
	ExperimentID string                        `gorm:"type:text;not null;uniqueIndex:evaluation_case,priority:1"`
	CaseID       string                        `gorm:"type:text;not null;uniqueIndex:evaluation_case,priority:2"`
	RunID        *string                       `gorm:"type:text;uniqueIndex"`
	Coding       *sdk.CodingEvaluationEvidence `gorm:"serializer:json;type:jsonb"`
	Experiment   *Evaluation                   `gorm:"foreignKey:ExperimentID;references:ID"`
	Run          *Run                          `gorm:"foreignKey:RunID;references:ID"`
}

func (EvaluationTrial) TableName() string { return "evaluation_trial" }

type EvaluationPlan struct {
	ProjectID string              `gorm:"type:text;not null;default:''"`
	ID        string              `gorm:"primaryKey;size:128"`
	OwnerID   string              `gorm:"primaryKey;size:128"`
	Revision  int64               `gorm:"not null"`
	Suite     sdk.EvaluationSuite `gorm:"serializer:json;type:jsonb;not null"`
	UpdatedAt time.Time
}

func (EvaluationPlan) TableName() string { return "evaluation_plans" }

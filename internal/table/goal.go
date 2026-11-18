package table

import (
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// Goal is the durable business objective that may span multiple runs.
type Goal struct {
	ID            string                  `gorm:"type:text;primaryKey;not null"`
	TenantID      string                  `gorm:"type:text;not null;index:durable_goal_owner_status,priority:1;uniqueIndex:durable_goal_owner_request_key_key,priority:1"`
	ProjectID     string                  `gorm:"type:text;not null"`
	OwnerID       string                  `gorm:"type:text;not null;index:durable_goal_owner_status,priority:2;uniqueIndex:durable_goal_owner_request_key_key,priority:2"`
	SessionID     string                  `gorm:"type:text;not null"`
	RequestKey    string                  `gorm:"type:text;not null;uniqueIndex:durable_goal_owner_request_key_key,priority:3"`
	RequestHash   string                  `gorm:"type:text;not null"`
	Objective     string                  `gorm:"type:text;not null"`
	Evaluator     execution.EvaluatorSpec `gorm:"serializer:json;type:jsonb;not null"`
	Budget        execution.BudgetSpec    `gorm:"serializer:json;type:jsonb;not null"`
	Status        execution.GoalStatus    `gorm:"type:text;not null;index:durable_goal_owner_status,priority:3;check:durable_goal_status_check,status IN ('Active','Paused','Blocked','Complete','Failed')"`
	ActiveRunID   string                  `gorm:"type:text;not null;default:''"`
	StallCount    int                     `gorm:"type:integer;not null;default:0"`
	LastStallMark string                  `gorm:"type:text;not null;default:''"`
	TokensUsed    int64                   `gorm:"type:bigint;not null;default:0"`
	CostUSD       float64                 `gorm:"type:double precision;not null;default:0"`
	CreatedAt     time.Time               `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false"`
	UpdatedAt     time.Time               `gorm:"type:timestamptz;not null;default:clock_timestamp();autoUpdateTime:false"`
	Session       *Session                `gorm:"foreignKey:SessionID;references:ID;constraint:durable_goal_session_id_fkey,OnUpdate:NO ACTION,OnDelete:NO ACTION"`
}

func (Goal) TableName() string { return "durable_goal" }

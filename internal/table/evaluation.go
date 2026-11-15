package table

import (
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

// Evaluation is the immutable acceptance receipt for one finished run. A run
// is evaluated at most once; retries must reproduce the same result.
type Evaluation struct {
	ID        string                     `gorm:"type:text;primaryKey;not null"`
	OwnerID   string                     `gorm:"type:text;not null"`
	GoalID    string                     `gorm:"type:text;not null;uniqueIndex:durable_evaluation_goal_run_key,priority:1"`
	RunID     string                     `gorm:"type:text;not null;uniqueIndex:durable_evaluation_goal_run_key,priority:2"`
	Status    execution.EvaluationStatus `gorm:"type:text;not null;check:durable_evaluation_status_check,status IN ('Passed','Failed','Error')"`
	Output    string                     `gorm:"type:text;not null"`
	CreatedAt time.Time                  `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false"`
}

func (Evaluation) TableName() string { return "durable_evaluation" }

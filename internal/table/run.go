// Package table defines the persisted schema used by API and worker storage.
package table

import (
	"encoding/json"
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

// ExecutionSnapshot stores the immutable configuration accepted for a run.
type ExecutionSnapshot struct {
	Toolsets   []string               `json:"toolsets,omitempty"`
	Filesystem *sdk.FilesystemBinding `json:"filesystem,omitempty"`
	Scope      *sdk.Scope             `json:"scope,omitempty"`

	AgentID       string         `json:"agentID"`
	ConfigVersion string         `json:"configVersion"`
	Query         string         `json:"query"`
	SystemPrompt  string         `json:"systemPrompt"`
	Model         sdk.ModelSpec  `json:"model"`
	Tools         []sdk.ToolSpec `json:"tools,omitempty"`
	MaxTurns      int            `json:"maxTurns"`
	History       []sdk.Message  `json:"history,omitempty"`
}

type Run struct {
	ID          string            `gorm:"type:text;primaryKey;not null"`
	OwnerID     string            `gorm:"type:text;not null;uniqueIndex:durable_run_owner_id_request_key_key,priority:1;uniqueIndex:durable_session_active,priority:1,where:status = 'Queued' OR status = 'Running' OR status = 'WaitingClarification' OR status = 'NeedsReview'"`
	SessionID   string            `gorm:"type:text;not null;uniqueIndex:durable_session_active,priority:2"`
	RequestKey  string            `gorm:"type:text;not null;uniqueIndex:durable_run_owner_id_request_key_key,priority:2"`
	RequestHash string            `gorm:"type:text;not null"`
	Spec        ExecutionSnapshot `gorm:"serializer:json;type:jsonb;not null"`
	State       json.RawMessage   `gorm:"serializer:json;type:jsonb;not null;default:'{}'"`
	Status      sdk.Status        `gorm:"type:text;not null;check:durable_run_status_check,status IN ('Queued','Running','WaitingClarification','NeedsReview','Completed','Failed','Canceled')"`
	WorkerID    string            `gorm:"type:text;not null;default:''"`
	Epoch       int64             `gorm:"type:bigint;not null;default:0"`
	LeaseUntil  *time.Time        `gorm:"type:timestamptz"`
	AvailableAt time.Time         `gorm:"type:timestamptz;not null;default:clock_timestamp();index:durable_run_queue,priority:1,where:status = 'Queued' OR status = 'Running'"`
	Deadline    time.Time         `gorm:"type:timestamptz;not null"`
	Attempt     int               `gorm:"type:integer;not null;default:0"`
	MaxAttempts int               `gorm:"type:integer;not null"`
	EventSeq    int64             `gorm:"type:bigint;not null;default:0"`
	Output      string            `gorm:"type:text;not null;default:''"`
	Error       string            `gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time         `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false;index:durable_run_queue,priority:2"`
	UpdatedAt   time.Time         `gorm:"type:timestamptz;not null;default:clock_timestamp();autoUpdateTime:false"`
}

func (Run) TableName() string { return "durable_run" }

type Step struct {
	RunID     string     `gorm:"type:text;primaryKey;not null"`
	StepID    string     `gorm:"type:text;primaryKey;not null"`
	InputHash string     `gorm:"type:text;not null"`
	Policy    sdk.Policy `gorm:"type:text;not null;check:durable_step_policy_check,policy IN ('ReadOnly','Idempotent','Unsafe')"`
	Status    string     `gorm:"type:text;not null;check:durable_step_status_check,status IN ('Started','Completed')"`
	Result    string     `gorm:"type:text;not null;default:''"`
	Run       *Run       `gorm:"foreignKey:RunID;references:ID;constraint:durable_step_run_id_fkey,OnUpdate:NO ACTION,OnDelete:NO ACTION"`
}

func (Step) TableName() string { return "durable_step" }

type Event struct {
	RunID     string          `gorm:"type:text;primaryKey;not null"`
	Seq       int64           `gorm:"type:bigint;primaryKey;autoIncrement:false;not null"`
	Kind      string          `gorm:"type:text;not null"`
	Payload   json.RawMessage `gorm:"serializer:json;type:jsonb;not null"`
	Published bool            `gorm:"not null;default:false"`
	CreatedAt time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false;index:durable_event_pending,where:NOT published"`
	Run       *Run            `gorm:"foreignKey:RunID;references:ID;constraint:durable_event_run_id_fkey,OnUpdate:NO ACTION,OnDelete:NO ACTION"`
}

func (Event) TableName() string { return "durable_event" }

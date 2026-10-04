package durable

import (
	"encoding/json"
	"time"
)

// Records are the schema source of truth. Run timestamps use the database
// clock because leases must not depend on individual worker clocks.
type runRecord struct {
	ID          string          `gorm:"type:text;primaryKey;not null"`
	OwnerID     string          `gorm:"type:text;not null;uniqueIndex:durable_run_owner_id_request_key_key,priority:1;uniqueIndex:durable_session_active,priority:1,where:status = 'Queued' OR status = 'Running' OR status = 'WaitingClarification' OR status = 'NeedsReview'"`
	SessionID   string          `gorm:"type:text;not null;uniqueIndex:durable_session_active,priority:2"`
	RequestKey  string          `gorm:"type:text;not null;uniqueIndex:durable_run_owner_id_request_key_key,priority:2"`
	RequestHash string          `gorm:"type:text;not null"`
	Spec        Spec            `gorm:"serializer:json;type:jsonb;not null"`
	State       json.RawMessage `gorm:"serializer:json;type:jsonb;not null;default:'{}'"`
	Status      Status          `gorm:"type:text;not null;check:durable_run_status_check,status IN ('Queued','Running','WaitingClarification','NeedsReview','Completed','Failed','Canceled')"`
	WorkerID    string          `gorm:"type:text;not null;default:''"`
	Epoch       int64           `gorm:"type:bigint;not null;default:0"`
	LeaseUntil  *time.Time      `gorm:"type:timestamptz"`
	AvailableAt time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();index:durable_run_queue,priority:1,where:status = 'Queued' OR status = 'Running'"`
	Deadline    time.Time       `gorm:"type:timestamptz;not null"`
	Attempt     int             `gorm:"type:integer;not null;default:0"`
	MaxAttempts int             `gorm:"type:integer;not null"`
	EventSeq    int64           `gorm:"type:bigint;not null;default:0"`
	Output      string          `gorm:"type:text;not null;default:''"`
	Error       string          `gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false;index:durable_run_queue,priority:2"`
	UpdatedAt   time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoUpdateTime:false"`
}

func (runRecord) TableName() string { return "durable_run" }
func (r runRecord) run() *Run {
	return &Run{Scope: r.Spec.Scope, AgentID: r.Spec.AgentID, ConfigVersion: r.Spec.ConfigVersion, ID: r.ID, OwnerID: r.OwnerID, SessionID: r.SessionID, Spec: r.Spec, State: r.State,
		Status: r.Status, WorkerID: r.WorkerID, Epoch: r.Epoch, LeaseUntil: r.LeaseUntil,
		Deadline: r.Deadline, Attempt: r.Attempt, MaxAttempts: r.MaxAttempts, EventSeq: r.EventSeq,
		Output: r.Output, Error: r.Error, WaitingStep: waitingStep(r.State)}
}

type stepRecord struct {
	RunID     string     `gorm:"type:text;primaryKey;not null"`
	StepID    string     `gorm:"type:text;primaryKey;not null"`
	InputHash string     `gorm:"type:text;not null"`
	Policy    Policy     `gorm:"type:text;not null;check:durable_step_policy_check,policy IN ('ReadOnly','Idempotent','Unsafe')"`
	Status    string     `gorm:"type:text;not null;check:durable_step_status_check,status IN ('Started','Completed')"`
	Result    string     `gorm:"type:text;not null;default:''"`
	Run       *runRecord `gorm:"foreignKey:RunID;references:ID;constraint:durable_step_run_id_fkey,OnUpdate:NO ACTION,OnDelete:NO ACTION"`
}

func (stepRecord) TableName() string { return "durable_step" }

type eventRecord struct {
	RunID     string          `gorm:"type:text;primaryKey;not null"`
	Seq       int64           `gorm:"type:bigint;primaryKey;autoIncrement:false;not null"`
	Kind      string          `gorm:"type:text;not null"`
	Payload   json.RawMessage `gorm:"serializer:json;type:jsonb;not null"`
	Published bool            `gorm:"not null;default:false"`
	CreatedAt time.Time       `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false;index:durable_event_pending,where:NOT published"`
	Run       *runRecord      `gorm:"foreignKey:RunID;references:ID;constraint:durable_event_run_id_fkey,OnUpdate:NO ACTION,OnDelete:NO ACTION"`
}

func (eventRecord) TableName() string { return "durable_event" }
func (e eventRecord) event() Event {
	return Event{RunID: e.RunID, Seq: e.Seq, Kind: e.Kind, Payload: e.Payload}
}

// Agent drafts are editable. Published versions are append-only through Store.
type agentRecord struct {
	ID                string      `gorm:"type:text;primaryKey;not null"`
	OwnerID           string      `gorm:"type:text;not null;index:agent_owner,priority:1"`
	Name              string      `gorm:"type:text;not null"`
	Draft             AgentConfig `gorm:"serializer:json;type:jsonb;not null"`
	Revision          int64       `gorm:"not null"`
	PublishedRevision int64       `gorm:"not null;default:0"`
	PublishedVersion  int64       `gorm:"not null;default:0"`
	CreatedAt         time.Time   `gorm:"not null"`
	UpdatedAt         time.Time   `gorm:"not null"`
}

func (agentRecord) TableName() string { return "agent" }

type versionRecord struct {
	AgentID   string       `gorm:"type:text;primaryKey;not null"`
	Number    int64        `gorm:"primaryKey;autoIncrement:false;not null"`
	Config    AgentConfig  `gorm:"serializer:json;type:jsonb;not null"`
	CreatedAt time.Time    `gorm:"not null"`
	Agent     *agentRecord `gorm:"foreignKey:AgentID;references:ID"`
}

func (versionRecord) TableName() string { return "agent_version" }

type sessionRecord struct {
	ID         string       `gorm:"type:text;primaryKey;not null"`
	OwnerID    string       `gorm:"type:text;not null;index:session_owner,priority:1"`
	AgentID    string       `gorm:"type:text;not null;index"`
	Title      string       `gorm:"type:text;not null"`
	MessageSeq int64        `gorm:"not null;default:0"`
	CreatedAt  time.Time    `gorm:"not null"`
	UpdatedAt  time.Time    `gorm:"not null"`
	Agent      *agentRecord `gorm:"foreignKey:AgentID;references:ID"`
}

func (sessionRecord) TableName() string { return "agent_session" }

type messageRecord struct {
	SessionID string         `gorm:"type:text;primaryKey;not null"`
	Seq       int64          `gorm:"primaryKey;autoIncrement:false;not null"`
	RunID     string         `gorm:"type:text;not null;index"`
	Kind      string         `gorm:"type:text;not null;check:agent_message_kind,kind IN ('User','Assistant','Final','Tool','ClarificationAnswer','ReviewResult')"`
	Message   Message        `gorm:"serializer:json;type:jsonb;not null"`
	CreatedAt time.Time      `gorm:"not null"`
	Session   *sessionRecord `gorm:"foreignKey:SessionID;references:ID"`
	Run       *runRecord     `gorm:"foreignKey:RunID;references:ID"`
}

func (messageRecord) TableName() string { return "agent_message" }

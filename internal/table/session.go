package table

import (
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Session struct {
	ID         string    `gorm:"type:text;primaryKey;not null"`
	OwnerID    string    `gorm:"type:text;not null;index:session_owner,priority:1"`
	AgentID    string    `gorm:"type:text;not null;index"`
	Title      string    `gorm:"type:text;not null"`
	MessageSeq int64     `gorm:"not null;default:0"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
	Agent      *Agent    `gorm:"foreignKey:AgentID;references:ID"`
}

func (Session) TableName() string { return "agent_session" }

type Message struct {
	SessionID string      `gorm:"type:text;primaryKey;not null"`
	Seq       int64       `gorm:"primaryKey;autoIncrement:false;not null"`
	RunID     string      `gorm:"type:text;not null;index"`
	Kind      string      `gorm:"type:text;not null;check:agent_message_kind,kind IN ('User','Assistant','Final','Tool','ClarificationAnswer','ReviewResult')"`
	Message   sdk.Message `gorm:"serializer:json;type:jsonb;not null"`
	CreatedAt time.Time   `gorm:"not null"`
	Session   *Session    `gorm:"foreignKey:SessionID;references:ID"`
	Run       *Run        `gorm:"foreignKey:RunID;references:ID"`
}

func (Message) TableName() string { return "agent_message" }

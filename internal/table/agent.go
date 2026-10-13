package table

import (
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Agent struct {
	ID                string          `gorm:"type:text;primaryKey;not null"`
	OwnerID           string          `gorm:"type:text;not null;index:agent_owner,priority:1"`
	Name              string          `gorm:"type:text;not null"`
	Draft             sdk.AgentConfig `gorm:"serializer:json;type:jsonb;not null"`
	Revision          int64           `gorm:"not null"`
	PublishedRevision int64           `gorm:"not null;default:0"`
	PublishedVersion  int64           `gorm:"not null;default:0"`
	CreatedAt         time.Time       `gorm:"not null"`
	UpdatedAt         time.Time       `gorm:"not null"`
}

func (Agent) TableName() string { return "agent" }

type AgentVersion struct {
	AgentID   string          `gorm:"type:text;primaryKey;not null"`
	Number    int64           `gorm:"primaryKey;autoIncrement:false;not null"`
	Config    sdk.AgentConfig `gorm:"serializer:json;type:jsonb;not null"`
	CreatedAt time.Time       `gorm:"not null"`
	Agent     *Agent          `gorm:"foreignKey:AgentID;references:ID"`
}

func (AgentVersion) TableName() string { return "agent_version" }

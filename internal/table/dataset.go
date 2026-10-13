package table

import (
	"time"

	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

type Dataset struct {
	ID               string               `gorm:"primaryKey;size:128"`
	OwnerID          string               `gorm:"not null;size:128;index"`
	Name             string               `gorm:"not null"`
	Revision         int64                `gorm:"not null"`
	PublishedVersion int64                `gorm:"not null;default:0"`
	Cases            []sdk.EvaluationCase `gorm:"serializer:json;type:jsonb;not null"`
	UpdatedAt        time.Time
}

func (Dataset) TableName() string { return "evaluation_dataset" }

type DatasetVersion struct {
	DatasetID string                `gorm:"primaryKey;size:128"`
	Number    int64                 `gorm:"primaryKey;autoIncrement:false"`
	Snapshot  sdk.EvaluationDataset `gorm:"serializer:json;type:jsonb;not null"`
	CreatedAt time.Time
	Dataset   *Dataset `gorm:"foreignKey:DatasetID;references:ID"`
}

func (DatasetVersion) TableName() string { return "evaluation_dataset_version" }

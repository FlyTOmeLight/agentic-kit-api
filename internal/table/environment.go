package table

import "time"

// RunEnvironment binds a run to a sandbox-center acquisition. History is
// kept: a run may hold several environments over its lifetime, and cleanup
// after run completion must still find unfinished records.
type RunEnvironment struct {
	ID         string `gorm:"type:text;primaryKey;not null"`
	RunID      string `gorm:"type:text;not null;uniqueIndex:durable_run_env_request_key,priority:1;index:durable_run_env_run,priority:1"`
	RequestKey string `gorm:"type:text;not null;uniqueIndex:durable_run_env_request_key,priority:2"`

	// RunEpoch is the claim epoch that acquired the environment. A new epoch
	// never renews a lease bound to an older epoch.
	RunEpoch int64 `gorm:"type:bigint;not null"`

	ProfileID       string `gorm:"type:text;not null"`
	ProfileRevision int64  `gorm:"type:bigint;not null;default:0"`
	SandboxID       string `gorm:"type:text;not null;default:'';index:durable_run_env_sandbox,priority:1,where:sandbox_id <> ''"`
	LeaseID         string `gorm:"type:text;not null;default:''"`
	Generation      int64  `gorm:"type:bigint;not null;default:0"`
	SavedCheckpoint string `gorm:"type:text;not null;default:''"`

	// Status: Preparing (request persisted before any network call), Active,
	// Released or Lost.
	Status string `gorm:"type:text;not null;check:durable_run_env_status_check,status IN ('Preparing','Active','Released','Lost')"`

	ReleasedAt *time.Time `gorm:"type:timestamptz"`
	CreatedAt  time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp();autoCreateTime:false"`
	UpdatedAt  time.Time  `gorm:"type:timestamptz;not null;default:clock_timestamp();autoUpdateTime:false"`
}

func (RunEnvironment) TableName() string { return "durable_run_environment" }

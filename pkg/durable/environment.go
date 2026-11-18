package durable

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

const (
	EnvironmentPreparing = "Preparing"
	EnvironmentActive    = "Active"
	EnvironmentReleased  = "Released"
	EnvironmentLost      = "Lost"
)

// RunEnvironment is the persisted binding between a run and a sandbox-center
// acquisition. It is written before any sandbox network call so a crash
// mid-acquire never leaves an untracked environment.
type RunEnvironment struct {
	ID              string
	RunID           string
	RequestKey      string
	RunEpoch        int64
	ProfileID       string
	ProfileRevision int64
	SandboxID       string
	LeaseID         string
	Generation      int64
	SavedCheckpoint string
	Status          string
	ReleasedAt      *time.Time
	CreatedAt       time.Time
}

func environmentFromRow(e table.RunEnvironment) *RunEnvironment {
	return &RunEnvironment{
		ID:              e.ID,
		RunID:           e.RunID,
		RequestKey:      e.RequestKey,
		RunEpoch:        e.RunEpoch,
		ProfileID:       e.ProfileID,
		ProfileRevision: e.ProfileRevision,
		SandboxID:       e.SandboxID,
		LeaseID:         e.LeaseID,
		Generation:      e.Generation,
		SavedCheckpoint: e.SavedCheckpoint,
		Status:          e.Status,
		ReleasedAt:      e.ReleasedAt,
		CreatedAt:       e.CreatedAt.UTC(),
	}
}

// PrepareEnvironment records the intent to acquire a sandbox under the
// caller's claim. It must run inside the lease-checked transaction BEFORE the
// acquire network call. Replay with the same request key returns the existing
// record so a retried acquire reuses the same sandbox request key.
func (s *Store) PrepareEnvironment(
	ctx context.Context,
	l Lease,
	requestKey, profileID string,
	profileRevision int64,
) (*RunEnvironment, error) {
	if requestKey == "" || profileID == "" || len(requestKey) > 128 {
		return nil, ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := lockLease(ctx, tx, l); err != nil {
		return nil, err
	}
	var existing table.RunEnvironment
	err := tx.Where("run_id = ? AND request_key = ?", l.RunID, requestKey).Take(&existing).Error
	if err == nil {
		if existing.ProfileID != profileID || existing.ProfileRevision != profileRevision {
			return nil, ErrConflict
		}
		return environmentFromRow(existing), tx.Commit().Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	row := table.RunEnvironment{
		ID:              NewID(),
		RunID:           l.RunID,
		RequestKey:      requestKey,
		RunEpoch:        l.Epoch,
		ProfileID:       profileID,
		ProfileRevision: profileRevision,
		Status:          EnvironmentPreparing,
	}
	if err = tx.Create(&row).Error; err != nil {
		return nil, err
	}
	return environmentFromRow(row), tx.Commit().Error
}

// ActivateEnvironment binds the acquired sandbox facts to a prepared record.
// Only the epoch that prepared it may activate it.
func (s *Store) ActivateEnvironment(
	ctx context.Context,
	l Lease,
	requestKey, sandboxID, leaseID string,
	generation int64,
) (*RunEnvironment, error) {
	if sandboxID == "" || leaseID == "" {
		return nil, ErrInvalid
	}
	tx := s.db.WithContext(ctx).Begin()
	if err := tx.Error; err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := lockLease(ctx, tx, l); err != nil {
		return nil, err
	}
	var row table.RunEnvironment
	err := tx.Where("run_id = ? AND request_key = ?", l.RunID, requestKey).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&row).Error
	if err != nil {
		return nil, recordError(err)
	}
	if row.RunEpoch != l.Epoch {
		return nil, ErrLeaseLost
	}
	if row.Status == EnvironmentActive {
		if row.SandboxID != sandboxID || row.LeaseID != leaseID {
			return nil, ErrConflict
		}
		return environmentFromRow(row), tx.Commit().Error
	}
	if row.Status != EnvironmentPreparing {
		return nil, ErrConflict
	}
	row.SandboxID = sandboxID
	row.LeaseID = leaseID
	row.Generation = generation
	row.Status = EnvironmentActive
	row.UpdatedAt = time.Now()
	if err = tx.Save(&row).Error; err != nil {
		return nil, err
	}
	return environmentFromRow(row), tx.Commit().Error
}

// GetEnvironment returns the environment record for an acquire request key.
func (s *Store) GetEnvironment(ctx context.Context, runID, requestKey string) (*RunEnvironment, error) {
	var row table.RunEnvironment
	err := s.db.WithContext(ctx).Where("run_id = ? AND request_key = ?", runID, requestKey).Take(&row).Error
	if err != nil {
		return nil, recordError(err)
	}
	return environmentFromRow(row), nil
}

// ActiveEnvironment returns the current non-released environment of a run, if
// any. Workers use it on takeover to reattach to the sandbox the previous
// worker acquired.
func (s *Store) ActiveEnvironment(ctx context.Context, runID string) (*RunEnvironment, error) {
	var row table.RunEnvironment
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND status IN ?", runID, []string{EnvironmentPreparing, EnvironmentActive}).
		Order("created_at DESC, id DESC").
		Take(&row).Error
	if err != nil {
		return nil, recordError(err)
	}
	return environmentFromRow(row), nil
}

// MarkEnvironmentReleased records that the sandbox was released. It is
// intentionally not lease-checked: cleanup after a lost claim must still be
// able to close the record once sandbox-center confirms destruction.
func (s *Store) MarkEnvironmentReleased(ctx context.Context, id string) error {
	tag := s.db.WithContext(ctx).Model(&table.RunEnvironment{}).
		Where("id = ? AND status IN ?", id, []string{EnvironmentPreparing, EnvironmentActive}).
		Updates(map[string]any{
			"status":      EnvironmentReleased,
			"released_at": gorm.Expr("clock_timestamp()"),
			"updated_at":  gorm.Expr("clock_timestamp()"),
		})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// UnfinishedEnvironments lists environment records whose sandbox may still
// exist, for the reconciler and for post-run cleanup.
func (s *Store) UnfinishedEnvironments(ctx context.Context, runID string) ([]*RunEnvironment, error) {
	var rows []table.RunEnvironment
	err := s.db.WithContext(ctx).
		Where("run_id = ? AND status IN ?", runID, []string{EnvironmentPreparing, EnvironmentActive}).
		Order("created_at, id").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*RunEnvironment, 0, len(rows))
	for _, row := range rows {
		out = append(out, environmentFromRow(row))
	}
	return out, nil
}

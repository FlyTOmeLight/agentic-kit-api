package sandboxcenter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrLeaseExpired = errors.New("lease expired")
	ErrLeaseClosed  = errors.New("lease closed")
	ErrInvalidInput = errors.New("invalid input")
	ErrNotReady     = errors.New("sandbox not ready")
	ErrSaveBlocked  = errors.New("required save is incomplete")
)

// Store executes durable sandbox-center transactions.
type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// DB exposes the underlying session for integration tests.
func (s *Store) DB() *gorm.DB { return s.db }

// Acquire creates or replays a sandbox acquisition. Idempotent by (OwnerID, RequestKey).
func (s *Store) Acquire(ctx context.Context, in AcquireInput) (*Sandbox, *Lease, error) {
	if in.OwnerID == "" || in.RequestKey == "" || in.ProfileID == "" || in.WorkerID == "" || in.RunID == "" {
		return nil, nil, fmt.Errorf("%w: missing required fields", ErrInvalidInput)
	}
	if in.HardExpiresAt.Before(time.Now()) {
		return nil, nil, fmt.Errorf("%w: hardExpiresAt in the past", ErrInvalidInput)
	}
	if in.RestoreFromSandboxID != "" && !in.RequiredSave {
		return nil, nil, fmt.Errorf("%w: restoreFromSandboxId requires requiredSave", ErrInvalidInput)
	}

	requestHash := hashRequest(in)
	var sb Sandbox
	var ls Lease

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.RestoreFromSandboxID != "" {
			if _, err := authorizeRun(ctx, tx, in.OwnerID, in.RunID, in.WorkerID, in.RunEpoch); err != nil {
				return err
			}
		}
		if err := authorizeRestore(tx, in); err != nil {
			return err
		}
		var existing Sandbox
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_id = ? AND request_key = ?", in.OwnerID, in.RequestKey).
			First(&existing).Error
		if err == nil {
			if existing.RequestHash != requestHash {
				return fmt.Errorf("%w: same request key with different input", ErrConflict)
			}
			sb = existing
			if existing.LeaseID != "" {
				if err := tx.Where("id = ?", existing.LeaseID).First(&ls).Error; err != nil {
					return err
				}
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if _, err := authorizeRun(ctx, tx, in.OwnerID, in.RunID, in.WorkerID, in.RunEpoch); err != nil {
			return err
		}
		checkpoint, err := resolveRestoreCheckpoint(tx, in)
		if err != nil {
			return err
		}

		// New acquisition
		sb = Sandbox{
			ID:                   fmt.Sprintf("sb-%s", newID()),
			TenantID:             in.TenantID,
			OwnerID:              in.OwnerID,
			RequestKey:           in.RequestKey,
			RequestHash:          requestHash,
			ProfileID:            in.ProfileID,
			ProfileRevision:      in.ProfileRevision,
			Status:               SandboxStatusProvisioning,
			RequiredSave:         in.RequiredSave,
			RestoreFromSandboxID: in.RestoreFromSandboxID,
			ResumeCheckpoint:     checkpoint,
			Generation:           0,
			Version:              1,
			CreatedAt:            time.Now().UTC(),
			UpdatedAt:            time.Now().UTC(),
		}
		if err := tx.Create(&sb).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &sb, &ls, nil
}

// ActivateForRun binds a ready pod to a sandbox under the caller's run claim
// and clamps the lease deadline to the authoritative run validity window.
func (s *Store) ActivateForRun(
	ctx context.Context,
	sandboxID, podUID, runID, workerID string,
	runEpoch int64,
	hardExpiresAt time.Time,
) (*Lease, error) {
	var ls Lease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sb Sandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sandboxID).
			First(&sb).
			Error; err != nil {
			return err
		}
		if sb.Status != SandboxStatusProvisioning {
			return fmt.Errorf("%w: sandbox not provisioning", ErrConflict)
		}
		if sb.PodUID != "" && sb.PodUID != podUID {
			return fmt.Errorf("%w: pod identity changed", ErrConflict)
		}
		auth, err := authorizeRun(ctx, tx, sb.OwnerID, runID, workerID, runEpoch)
		if err != nil {
			return err
		}
		hardExpiresAt = clampToRunAuth(hardExpiresAt, auth)
		now := time.Now().UTC()
		sb.Status = SandboxStatusActive
		sb.PodUID = podUID
		sb.Generation++
		sb.LeaseID = fmt.Sprintf("lease-%s", newID())
		sb.Version++
		sb.UpdatedAt = now
		if err := tx.Save(&sb).Error; err != nil {
			return err
		}

		ls = Lease{
			ID:                sb.LeaseID,
			SandboxID:         sb.ID,
			SandboxGeneration: sb.Generation,
			HolderRunID:       runID,
			HolderRunEpoch:    runEpoch,
			WorkerID:          workerID,
			Revision:          1,
			Status:            LeaseStatusActive,
			HardExpiresAt:     hardExpiresAt,
			RenewedAt:         now,
			Version:           1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		return tx.Create(&ls).Error
	})
	if err != nil {
		return nil, err
	}
	return &ls, nil
}

// Renew extends an active lease with monotonic revision. Same revision with
// same request key returns original receipt; different revision or params conflict.
func (s *Store) Renew(
	ctx context.Context,
	leaseID string,
	revision int64,
	requestKey string,
	requestHash string,
	hardExpiresAt time.Time,
) (*Lease, bool, error) {
	var ls Lease
	replayed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", leaseID).
			First(&ls).
			Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if ls.Status != LeaseStatusActive {
			return ErrLeaseClosed
		}
		var checkedAt time.Time
		if err := tx.Raw("SELECT clock_timestamp()").Scan(&checkedAt).Error; err != nil {
			return err
		}
		if !ls.HardExpiresAt.After(checkedAt) {
			return ErrLeaseExpired
		}
		if revision < ls.Revision {
			return fmt.Errorf("%w: old revision %d < current %d", ErrConflict, revision, ls.Revision)
		}
		if revision == ls.Revision {
			// Same revision: must be replay with identical request hash
			if requestHash == "" || requestHash != ls.RequestHash {
				return fmt.Errorf("%w: same revision with different input", ErrConflict)
			}
			replayed = true
			return nil
		}
		auth, err := authorizeRun(ctx, tx, "", ls.HolderRunID, ls.WorkerID, ls.HolderRunEpoch)
		if errors.Is(err, ErrLeaseClosed) {
			_, auth, err = authorizeEvaluation(ctx, tx, &ls)
		}
		if err != nil {
			return err
		}
		hardExpiresAt = clampToRunAuth(hardExpiresAt, auth)
		if !hardExpiresAt.After(auth.CheckedAt) {
			return ErrLeaseExpired
		}

		// New revision
		ls.Revision = revision
		ls.RequestHash = requestHash
		ls.HardExpiresAt = hardExpiresAt
		ls.RenewedAt = auth.CheckedAt.UTC()
		ls.Version++
		ls.UpdatedAt = ls.RenewedAt
		return tx.Save(&ls).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &ls, replayed, nil
}

// GetLease reads back the accepted lease fact.
func (s *Store) GetLease(ctx context.Context, leaseID string) (*Lease, error) {
	var ls Lease
	err := s.db.WithContext(ctx).Where("id = ?", leaseID).First(&ls).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ls, nil
}

// GetSandbox reads back one sandbox fact.
func (s *Store) GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error) {
	var sb Sandbox
	err := s.db.WithContext(ctx).Where("id = ?", sandboxID).First(&sb).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sb, nil
}

// BeginOperation persists the intent before dispatch. Idempotent by (LeaseID, RequestKey).
func (s *Store) BeginOperation(
	ctx context.Context,
	leaseID, requestKey, kind, inputHash string,
	claims ...EvaluationClaim,
) (*Operation, error) {
	var op Operation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Replay of a non-pending receipt does not require the run claim or
		// sandbox lease to remain active: callers must always be able to read
		// back what already happened.
		err := tx.Where("lease_id = ? AND request_key = ?", leaseID, requestKey).First(&op).Error
		if err == nil {
			if op.InputHash != inputHash || op.Kind != kind {
				return fmt.Errorf("%w: same request key with different input", ErrConflict)
			}
			if op.Status != OpStatusPending {
				return nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		ls, err := authorizeOperation(ctx, tx, leaseID, requestKey, kind, inputHash, claims...)
		if err != nil {
			return err
		}
		if op.ID != "" {
			return nil
		}

		op = Operation{
			ID:          fmt.Sprintf("op-%s", newID()),
			LeaseID:     leaseID,
			SandboxID:   ls.SandboxID,
			RequestKey:  requestKey,
			RequestHash: inputHash,
			Kind:        kind,
			InputHash:   inputHash,
			Status:      OpStatusPending,
			Version:     1,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		return tx.Create(&op).Error
	})
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// MarkDispatchStarted records that dispatch boundary was crossed.
func (s *Store) MarkDispatchStarted(
	ctx context.Context,
	operationID, executionID string,
	claims ...EvaluationClaim,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op Operation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", operationID).
			First(&op).
			Error; err != nil {
			return err
		}
		if op.Status != OpStatusPending {
			return ErrOutcomeUnknown
		}
		if _, err := authorizeOperation(
			ctx,
			tx,
			op.LeaseID,
			op.RequestKey,
			op.Kind,
			op.InputHash,
			claims...); err != nil {
			return err
		}
		now := time.Now().UTC()
		op.Status = OpStatusDispatchStarted
		op.ExecutionID = executionID
		op.DispatchedAt = &now
		op.Version++
		op.UpdatedAt = now
		return tx.Save(&op).Error
	})
}

// CompleteOperation stores the terminal receipt. Storage failure returns error.
func (s *Store) CompleteOperation(ctx context.Context, operationID string, exitCode *int, output, errStr string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op Operation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", operationID).
			First(&op).
			Error; err != nil {
			return err
		}
		if op.Status != OpStatusDispatchStarted {
			return fmt.Errorf("%w: operation not in DispatchStarted", ErrConflict)
		}
		now := time.Now().UTC()
		if errStr != "" || (exitCode != nil && *exitCode != 0) {
			op.Status = OpStatusFailed
			op.Error = errStr
		} else {
			op.Status = OpStatusSucceeded
		}
		op.ExitCode = exitCode
		op.Output = output
		op.CompletedAt = &now
		op.ReceiptStoredAt = &now
		op.Version++
		op.UpdatedAt = now
		return tx.Save(&op).Error
	})
}

// MarkOperationUnknown marks an operation Unknown without retry.
func (s *Store) MarkOperationUnknown(ctx context.Context, operationID string, reason string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op Operation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", operationID).
			First(&op).
			Error; err != nil {
			return err
		}
		if op.Status == OpStatusSucceeded || op.Status == OpStatusFailed {
			return nil // already terminal
		}
		now := time.Now().UTC()
		op.Status = OpStatusUnknown
		op.Error = reason
		op.UpdatedAt = now
		op.Version++
		return tx.Save(&op).Error
	})
}

// GetOperation reads the durable receipt.
func (s *Store) GetOperation(ctx context.Context, operationID string) (*Operation, error) {
	var op Operation
	err := s.db.WithContext(ctx).Where("id = ?", operationID).First(&op).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &op, nil
}

// Release closes admission and marks the sandbox for destruction.
func (s *Store) Release(ctx context.Context, sandboxID string) (*Sandbox, error) {
	return s.release(ctx, sandboxID, "")
}

func (s *Store) release(ctx context.Context, sandboxID, expectedStatus string) (*Sandbox, error) {
	var sb Sandbox
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sandboxID).
			First(&sb).
			Error; err != nil {
			return err
		}
		if sb.Status == SandboxStatusDestroyed || sb.Status == SandboxStatusReleasing ||
			sb.Status == SandboxStatusSaveBlocked {
			return nil
		}
		if sb.Status == SandboxStatusLost {
			return ErrConflict
		}
		if expectedStatus != "" && sb.Status != expectedStatus {
			return ErrConflict
		}
		if sb.LeaseID != "" {
			var lease Lease
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", sb.LeaseID).
				First(&lease).
				Error; err != nil {
				return err
			}
			if expectedStatus == SandboxStatusActive {
				var expired bool
				if err := tx.Raw("SELECT hard_expires_at <= clock_timestamp() FROM sandbox_lease WHERE id = ?", lease.ID).
					Scan(&expired).
					Error; err != nil {
					return err
				}
				if !expired {
					return ErrConflict
				}
			}
		}
		now := time.Now().UTC()
		sb.Status = SandboxStatusReleasing
		if sb.RequiredSave && sb.SavedCheckpoint == "" {
			sb.Status = SandboxStatusSaveBlocked
		}
		sb.UpdatedAt = now
		sb.Version++
		if err := tx.Save(&sb).Error; err != nil {
			return err
		}
		if sb.LeaseID != "" {
			return tx.Model(&Lease{}).Where("id = ? AND status = ?", sb.LeaseID, LeaseStatusActive).
				Updates(map[string]any{"status": LeaseStatusReleasing, "updated_at": now, "version": gorm.Expr("version + 1")}).
				Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &sb, nil
}

// ConfirmDestroyed marks the sandbox destroyed after provider evidence.
func (s *Store) ConfirmDestroyed(ctx context.Context, sandboxID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sb Sandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sandboxID).
			First(&sb).
			Error; err != nil {
			return err
		}
		if sb.RequiredSave && sb.SavedCheckpoint == "" {
			return ErrSaveBlocked
		}
		if sb.Status == SandboxStatusDestroyed {
			return nil
		}
		if sb.Status != SandboxStatusReleasing {
			return ErrConflict
		}
		now := time.Now().UTC()
		sb.Status = SandboxStatusDestroyed
		sb.DestroyedAt = &now
		sb.UpdatedAt = now
		sb.Version++
		if err := tx.Save(&sb).Error; err != nil {
			return err
		}
		if sb.LeaseID != "" {
			var ls Lease
			if err := tx.Where("id = ?", sb.LeaseID).First(&ls).Error; err == nil {
				if ls.Status != LeaseStatusClosed {
					ls.Status = LeaseStatusClosed
					ls.ClosedAt = &now
					ls.UpdatedAt = now
					ls.Version++
					if err := tx.Save(&ls).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// AcquireInput is the semantic request for idempotent acquisition.
type AcquireInput struct {
	TenantID             string
	OwnerID              string
	RequestKey           string
	ProfileID            string
	ProfileRevision      int64
	WorkerID             string
	RunID                string
	RunEpoch             int64
	HardExpiresAt        time.Time
	WorkspaceSpec        string
	RequiredSave         bool
	RestoreFromSandboxID string
}

// markLost closes a Provisioning sandbox that never activated: the orphan pod
// has been deleted and no lease was ever issued for this record.
func (s *Store) markLost(ctx context.Context, sandboxID string) error {
	tag := s.db.WithContext(ctx).Model(&Sandbox{}).
		Where("id = ? AND status = ?", sandboxID, SandboxStatusProvisioning).
		Updates(map[string]any{
			"status":     SandboxStatusLost,
			"updated_at": gorm.Expr("clock_timestamp()"),
			"version":    gorm.Expr("version + 1"),
		})
	if tag.Error != nil {
		return tag.Error
	}
	if tag.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func hashRequest(in AcquireInput) string {
	return strings.Join([]string{
		in.TenantID, in.OwnerID, in.ProfileID,
		fmt.Sprint(in.ProfileRevision), in.WorkerID, in.RunID, fmt.Sprint(in.RunEpoch),
		in.WorkspaceSpec, fmt.Sprint(in.RequiredSave),
		in.RestoreFromSandboxID,
	}, "\x1F")
}

func hashRenew(ls Lease, revision int64) string {
	return strings.Join([]string{
		ls.ID, ls.SandboxID, ls.HolderRunID, ls.WorkerID,
		fmt.Sprint(ls.SandboxGeneration), fmt.Sprint(revision),
	}, "\x1F")
}

// PendingReconciliation lists sandboxes whose durable state may diverge from
// the cluster: provisioned pods whose activation failed (Provisioning past
// the grace window), sandboxes mid-release whose pod deletion was not
// confirmed, and active sandboxes past their hard lease deadline.
func (s *Store) PendingReconciliation(
	ctx context.Context,
	provisioningGrace time.Duration,
	limit int,
) ([]Sandbox, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []Sandbox
	err := s.db.WithContext(ctx).
		Where(
			`status = ? AND created_at < clock_timestamp() - ?*interval '1 millisecond'`+
				` OR status = ?`+
				` OR status = ?`+
				` OR (status = ? AND lease_id IN (SELECT id FROM sandbox_lease WHERE status = ? AND hard_expires_at <= clock_timestamp()))`+
				` OR (status IN ? AND EXISTS (SELECT 1 FROM durable_run_environment env WHERE env.sandbox_id = sandbox.id AND (env.status IN ? OR (env.status = ? AND env.saved_checkpoint = '' AND sandbox.saved_checkpoint <> ''))))`,
			SandboxStatusProvisioning, provisioningGrace.Milliseconds(),
			SandboxStatusReleasing,
			SandboxStatusSaveBlocked,
			SandboxStatusActive, LeaseStatusActive,
			[]string{SandboxStatusDestroyed, SandboxStatusLost},
			[]string{envStatusPreparing, envStatusActive}, envStatusReleased,
		).
		Order("updated_at, id").
		Limit(limit).
		Find(&out).Error
	return out, err
}

func newID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

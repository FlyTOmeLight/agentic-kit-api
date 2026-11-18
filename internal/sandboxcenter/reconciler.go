// reconciler.go closes the gap between durable sandbox state and provider
// evidence. It only ever deletes or marks records from cluster facts; it
// never re-runs operations or resurrects a closed lease.
package sandboxcenter

import (
	"context"
	"errors"
	"time"

	"github.com/FlyTOmeLight/server-go/logger"
)

// ReconcilerConfig bounds the sweep. ProvisioningGrace is how long a pod may
// exist without a durable activation before the reconciler deletes it as an
// orphan; it must exceed the worst-case Provision latency.
type ReconcilerConfig struct {
	Interval          time.Duration
	ProvisioningGrace time.Duration
	BatchSize         int
}

// Reconciler sweeps sandboxes whose durable state may diverge from the
// cluster and converges them with provider evidence.
type Reconciler struct {
	store    *Store
	provider *Provider
	saver    *Service
	cfg      ReconcilerConfig
	log      *logger.Logger
}

func NewReconciler(store *Store, provider *Provider, cfg ReconcilerConfig, log *logger.Logger) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.ProvisioningGrace <= 0 {
		cfg.ProvisioningGrace = 10 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	return &Reconciler{store: store, provider: provider, cfg: cfg, log: log}
}

// WithCheckpoints lets the reconciler drive the required-save loop after stopping a
// save-blocked sandbox.
func (r *Reconciler) WithCheckpoints(service *Service) *Reconciler {
	r.saver = service
	return r
}

// Run sweeps until ctx is canceled; it returns ctx.Err().
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		r.Sweep(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Sweep performs one pass. Each sandbox is handled independently so one
// failure does not block the rest of the batch.
func (r *Reconciler) Sweep(ctx context.Context) {
	pending, err := r.store.PendingReconciliation(ctx, r.cfg.ProvisioningGrace, r.cfg.BatchSize)
	if err != nil {
		r.warnf(ctx, "reconcile list failed: %v", err)
		return
	}
	for _, sb := range pending {
		if ctx.Err() != nil {
			return
		}
		r.converge(ctx, sb)
	}
}

func (r *Reconciler) converge(ctx context.Context, sb Sandbox) {
	switch sb.Status {
	case SandboxStatusProvisioning:
		if sb.RequiredSave {
			if err := NewService(r.store, r.provider).recoverProvisioningIdentity(ctx, sb); err != nil {
				r.warnf(ctx, "reconcile observe orphan %s: %v", sb.ID, err)
				return
			}
			blocked, err := r.store.release(ctx, sb.ID, SandboxStatusProvisioning)
			if err != nil {
				r.warnf(ctx, "reconcile block orphan %s: %v", sb.ID, err)
				return
			}
			if blocked.ResumeCheckpoint != "" && blocked.Generation == 0 {
				r.converge(ctx, *blocked)
				return
			}
			if err := NewService(r.store, r.provider).stopBlocked(ctx, *blocked); err != nil {
				r.warnf(ctx, "reconcile stop blocked sandbox %s: %v", sb.ID, err)
			}
			return
		}
		// The pod may exist while the activation transaction never committed.
		// Without a durable lease the pod holds no authority: delete it and
		// mark the record lost so the caller can re-acquire with a new key.
		if err := r.provider.Destroy(ctx, sb); err != nil {
			r.warnf(ctx, "reconcile destroy orphan pod for %s: %v", sb.ID, err)
			return
		}
		if err := r.store.markLost(ctx, sb.ID); err != nil {
			r.warnf(ctx, "reconcile mark %s lost: %v", sb.ID, err)
			return
		}
		sb.Status = SandboxStatusLost
		r.closeEnvironments(ctx, sb)
	case SandboxStatusReleasing:
		if sb.SavedCheckpoint != "" {
			if err := NewService(r.store, r.provider).completeSave(ctx, sb); err != nil {
				r.warnf(ctx, "reconcile remove saver for %s: %v", sb.ID, err)
				return
			}
		}
		// Release was durably accepted but the pod deletion was not confirmed.
		if err := r.provider.Destroy(ctx, sb); err != nil {
			r.warnf(ctx, "reconcile destroy releasing pod for %s: %v", sb.ID, err)
			return
		}
		if err := r.store.ConfirmDestroyed(ctx, sb.ID); err != nil {
			r.warnf(ctx, "reconcile confirm %s destroyed: %v", sb.ID, err)
			return
		}
		sb.Status = SandboxStatusDestroyed
		r.closeEnvironments(ctx, sb)
	case SandboxStatusDestroyed, SandboxStatusLost:
		r.closeEnvironments(ctx, sb)
	case SandboxStatusActive:
		fresh, err := r.store.release(ctx, sb.ID, SandboxStatusActive)
		if err != nil {
			r.warnf(ctx, "reconcile expire %s: %v", sb.ID, err)
			return
		}
		r.converge(ctx, *fresh)
	case SandboxStatusSaveBlocked:
		service := NewService(r.store, r.provider)
		if sb.ResumeCheckpoint != "" && sb.Generation == 0 {
			if err := service.abandonRestore(ctx, sb); err != nil {
				r.warnf(ctx, "reconcile abandon restore %s: %v", sb.ID, err)
				return
			}
			if err := r.store.CloseEnvironmentsForSandbox(ctx, sb.ID, false); err != nil {
				r.warnf(ctx, "reconcile close restore environment %s: %v", sb.ID, err)
			}
			return
		}
		if r.saver != nil {
			service = r.saver
		}
		if err := service.stopBlocked(ctx, sb); err != nil {
			r.warnf(ctx, "reconcile stop blocked sandbox %s: %v", sb.ID, err)
			return
		}
		fresh, err := r.store.GetSandbox(ctx, sb.ID)
		if err != nil {
			r.warnf(ctx, "reconcile reload blocked sandbox %s: %v", sb.ID, err)
			return
		}
		if fresh.Status != SandboxStatusSaveBlocked || fresh.StoppedAt == nil {
			return
		}
		if err := service.Save(ctx, *fresh); err != nil && !errors.Is(err, ErrNotReady) {
			r.warnf(ctx, "reconcile save blocked sandbox %s: %v", sb.ID, err)
		}
	}
}

// closeEnvironments finalizes durable run-environment records bound to the
// sandbox. It runs only after the sandbox-center record already converged, so
// a failure here leaves the durable rows open and the next sweep retries.
func (r *Reconciler) closeEnvironments(ctx context.Context, sb Sandbox) {
	destroyed := sb.Status == SandboxStatusDestroyed
	if err := r.store.CloseEnvironmentsForSandbox(ctx, sb.ID, destroyed); err != nil {
		r.warnf(ctx, "reconcile close environments for %s: %v", sb.ID, err)
	}
}

func (r *Reconciler) warnf(ctx context.Context, format string, args ...any) {
	if r.log != nil {
		r.log.Warnf(ctx, format, args...)
	}
}

package sandboxcenter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) recoverProvisioningIdentity(ctx context.Context, sandbox Sandbox) error {
	podUID := sandbox.PodUID
	workspaceUID := sandbox.WorkspaceUID
	for _, target := range []struct {
		kind string
		name string
		uid  *string
	}{
		{kind: "pods", name: "sandbox-" + sandbox.ID, uid: &podUID},
		{kind: "persistentvolumeclaims", name: "workspace-" + sandbox.ID, uid: &workspaceUID},
	} {
		if target.kind == "persistentvolumeclaims" && !sandbox.RequiredSave {
			continue
		}
		uid, err := s.provider.observeIdentity(ctx, sandbox.ID, target.kind, target.name, *target.uid)
		if err != nil {
			return err
		}
		*target.uid = uid
	}
	return s.store.recordPhysicalIdentity(ctx, sandbox.ID, podUID, workspaceUID)
}

func (p *Provider) observeIdentity(ctx context.Context, sandboxID, kind, name, expectedUID string) (string, error) {
	out, err := p.kubectl(ctx, nil, "get", kind, name, "-o", "json", "--ignore-not-found")
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return expectedUID, nil
	}
	var object struct {
		Metadata struct {
			UID    string            `json:"uid"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &object); err != nil {
		return "", err
	}
	if object.Metadata.UID == "" || object.Metadata.Labels["sandbox-center/id"] != sandboxID ||
		object.Metadata.Labels["app.kubernetes.io/managed-by"] != "sandbox-center" ||
		(expectedUID != "" && expectedUID != object.Metadata.UID) {
		return "", ErrConflict
	}
	return object.Metadata.UID, nil
}

func (s *Store) recordPhysicalIdentity(ctx context.Context, sandboxID, podUID, workspaceUID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sandbox Sandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sandboxID).
			First(&sandbox).
			Error; err != nil {
			return err
		}
		if sandbox.Status != SandboxStatusProvisioning &&
			!(sandbox.Status == SandboxStatusSaveBlocked && sandbox.Generation == 0 && sandbox.StoppedAt == nil) {
			return ErrConflict
		}
		if (sandbox.PodUID != "" && sandbox.PodUID != podUID) ||
			(sandbox.WorkspaceUID != "" && sandbox.WorkspaceUID != workspaceUID) {
			return ErrConflict
		}
		return tx.Model(&Sandbox{}).Where("id = ?", sandbox.ID).Updates(map[string]any{
			"pod_uid": podUID, "workspace_uid": workspaceUID,
			"updated_at": gorm.Expr("clock_timestamp()"),
			"version":    gorm.Expr("version + 1"),
		}).Error
	})
}

func (s *Service) Release(ctx context.Context, sandboxID string) (*Sandbox, error) {
	sandbox, err := s.store.Release(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	switch sandbox.Status {
	case SandboxStatusDestroyed:
		return sandbox, s.store.CloseEnvironmentsForSandbox(ctx, sandbox.ID, true)
	case SandboxStatusSaveBlocked:
		if sandbox.ResumeCheckpoint != "" && sandbox.Generation == 0 {
			if err := s.abandonRestore(ctx, *sandbox); err != nil {
				return nil, err
			}
			if err := s.store.CloseEnvironmentsForSandbox(ctx, sandbox.ID, false); err != nil {
				return nil, err
			}
			return s.store.GetSandbox(ctx, sandbox.ID)
		}
		if err := s.stopBlocked(ctx, *sandbox); err != nil {
			return nil, err
		}
	case SandboxStatusReleasing:
		if sandbox.SavedCheckpoint != "" {
			if err := s.completeSave(ctx, *sandbox); err != nil {
				return nil, err
			}
		}
		if err := s.provider.Destroy(ctx, *sandbox); err != nil {
			return nil, err
		}
		if err := s.store.ConfirmDestroyed(ctx, sandbox.ID); err != nil {
			return nil, err
		}
		if err := s.store.CloseEnvironmentsForSandbox(ctx, sandbox.ID, true); err != nil {
			return nil, err
		}
	default:
		return nil, ErrConflict
	}
	return s.store.GetSandbox(ctx, sandbox.ID)
}

func (s *Service) stopBlocked(ctx context.Context, sandbox Sandbox) error {
	if sandbox.Status != SandboxStatusSaveBlocked {
		return ErrConflict
	}
	if sandbox.StoppedAt != nil {
		return nil
	}
	if sandbox.PodUID == "" || sandbox.WorkspaceUID == "" {
		if err := s.recoverProvisioningIdentity(ctx, sandbox); err != nil {
			return err
		}
		fresh, err := s.store.GetSandbox(ctx, sandbox.ID)
		if err != nil {
			return err
		}
		sandbox = *fresh
	}
	if err := s.store.db.WithContext(ctx).
		Model(&Sandbox{}).
		Where("id = ? AND status = ?", sandbox.ID, SandboxStatusSaveBlocked).
		Updates(map[string]any{"updated_at": gorm.Expr("clock_timestamp()"), "version": gorm.Expr("version + 1")}).
		Error; err != nil {
		return err
	}
	if err := s.provider.Stop(ctx, sandbox); err != nil {
		return fmt.Errorf("stop save-blocked sandbox: %w", err)
	}
	return s.store.confirmStopped(ctx, sandbox)
}

func (s *Store) confirmStopped(ctx context.Context, sandbox Sandbox) error {
	result := s.db.WithContext(ctx).Model(&Sandbox{}).
		Where("id = ? AND generation = ? AND pod_uid = ? AND status = ? AND stopped_at IS NULL",
			sandbox.ID, sandbox.Generation, sandbox.PodUID, SandboxStatusSaveBlocked).
		Updates(map[string]any{
			"stopped_at": gorm.Expr("clock_timestamp()"),
			"updated_at": gorm.Expr("clock_timestamp()"),
			"version":    gorm.Expr("version + 1"),
		})
	return result.Error
}

func (s *Store) confirmRequiredSave(
	ctx context.Context,
	sandboxID string,
	generation, version int64,
	checkpoint string,
) error {
	if strings.TrimSpace(checkpoint) == "" {
		return ErrInvalidInput
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sandbox Sandbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", sandboxID).
			First(&sandbox).
			Error; err != nil {
			return err
		}
		if sandbox.Generation != generation || !sandbox.RequiredSave {
			return ErrConflict
		}
		if sandbox.SavedCheckpoint == checkpoint &&
			(sandbox.Status == SandboxStatusReleasing || sandbox.Status == SandboxStatusDestroyed) {
			return nil
		}
		if sandbox.Status != SandboxStatusSaveBlocked || sandbox.StoppedAt == nil ||
			sandbox.Version != version ||
			sandbox.WorkspaceUID == "" {
			return ErrConflict
		}
		return tx.Model(&Sandbox{}).Where("id = ?", sandbox.ID).Updates(map[string]any{
			"saved_checkpoint": checkpoint,
			"status":           SandboxStatusReleasing,
			"updated_at":       gorm.Expr("clock_timestamp()"),
			"version":          gorm.Expr("version + 1"),
		}).Error
	})
}

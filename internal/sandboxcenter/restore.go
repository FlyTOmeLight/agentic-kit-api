package sandboxcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

func (s *Service) abandonRestore(ctx context.Context, sandbox Sandbox) error {
	if sandbox.Status != SandboxStatusSaveBlocked || sandbox.Generation != 0 || sandbox.ResumeCheckpoint == "" {
		return ErrConflict
	}
	if err := s.recoverProvisioningIdentity(ctx, sandbox); err != nil {
		return err
	}
	fresh, err := s.store.GetSandbox(ctx, sandbox.ID)
	if err != nil {
		return err
	}
	if fresh.PodUID != "" {
		if err := s.provider.destroyObject(ctx, "pods", "sandbox-"+fresh.ID, fresh.PodUID); err != nil {
			return err
		}
	}
	if fresh.WorkspaceUID != "" {
		if err := s.provider.destroyObject(
			ctx,
			"persistentvolumeclaims",
			"workspace-"+fresh.ID,
			fresh.WorkspaceUID,
		); err != nil {
			return err
		}
	}
	result := s.store.db.WithContext(ctx).Model(&Sandbox{}).
		Where("id = ? AND status = ? AND generation = 0 AND version = ?",
			fresh.ID, SandboxStatusSaveBlocked, fresh.Version).
		Updates(map[string]any{
			"status": SandboxStatusLost, "updated_at": gorm.Expr("clock_timestamp()"),
			"version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func authorizeRestore(tx *gorm.DB, in AcquireInput) error {
	var filesystem string
	if err := tx.Raw(`SELECT COALESCE(spec -> 'filesystem', 'null'::jsonb)::text
		FROM durable_run WHERE id = ? FOR SHARE`, in.RunID).Scan(&filesystem).Error; err != nil {
		return err
	}
	var binding *execution.FilesystemBinding
	if filesystem != "" {
		if err := json.Unmarshal([]byte(filesystem), &binding); err != nil {
			return fmt.Errorf("%w: invalid frozen filesystem", ErrConflict)
		}
	}
	if in.RestoreFromSandboxID == "" && (binding == nil || binding.RestoreFromSandboxID == "") {
		return nil
	}
	if binding == nil || binding.RestoreFromSandboxID != in.RestoreFromSandboxID {
		return fmt.Errorf("%w: restore must match frozen filesystem source", ErrConflict)
	}
	workspace := path.Clean(binding.Workspace)
	if binding.Provider != "Sandbox" ||
		(workspace != "/workspace" && !strings.HasPrefix(workspace, "/workspace/")) {
		return fmt.Errorf("%w: restore must match frozen /workspace filesystem", ErrConflict)
	}
	profileID := binding.ProfileID
	if profileID == "" {
		profileID = "coding"
	}
	if profileID != in.ProfileID {
		return fmt.Errorf("%w: restore profile differs from frozen filesystem", ErrConflict)
	}
	return nil
}

func resolveRestoreCheckpoint(tx *gorm.DB, in AcquireInput) (string, error) {
	if in.RestoreFromSandboxID == "" {
		return "", nil
	}
	var source Sandbox
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id = ? AND tenant_id = ? AND owner_id = ?", in.RestoreFromSandboxID, in.TenantID, in.OwnerID).
		First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if source.ProfileID != in.ProfileID || source.ProfileRevision != in.ProfileRevision ||
		source.Status != SandboxStatusDestroyed || !source.RequiredSave || source.StoppedAt == nil ||
		source.PodUID == "" || source.WorkspaceUID == "" {
		return "", fmt.Errorf(
			"%w: restore source has incompatible profile or unconfirmed destruction/save",
			ErrConflict,
		)
	}
	if err := validateCheckpointID(source.SavedCheckpoint); err != nil {
		return "", fmt.Errorf("%w: restore source checkpoint is invalid", ErrConflict)
	}
	return source.SavedCheckpoint, nil
}

const restoreWorkspaceScript = `
import hashlib
import importlib.util
import json
import os
import pathlib
import sys

checkpoint = sys.argv[1]
_, manifest_hex, layer_hex = checkpoint.split(":")
manifest_digest = "sha256:" + manifest_hex
manifest_path = pathlib.Path("/checkpoints") / manifest_hex
if manifest_path.stat().st_size > 65536:
    raise ValueError("manifest too large")
manifest_bytes = manifest_path.read_bytes()
if hashlib.sha256(manifest_bytes).hexdigest() != manifest_hex:
    raise ValueError("manifest digest mismatch")
manifest = json.loads(manifest_bytes)
if manifest["layer"]["digest"] != "sha256:" + layer_hex:
    raise ValueError("checkpoint layer digest mismatch")
destination = pathlib.Path("/volume/workspace")
if not os.path.lexists(destination):
    spec = importlib.util.spec_from_file_location("checkpoint", "/usr/local/bin/layerfs_checkpoint.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.restore("/checkpoints", manifest_digest, sys.argv[2], destination)
elif destination.is_symlink() or not destination.is_dir():
    raise ValueError("invalid restored workspace")
`

func restoreInitContainer(sb Sandbox, cfg CheckpointConfig) map[string]any {
	return map[string]any{
		"name":  "restore",
		"image": cfg.Image,
		"command": []string{
			"python3",
			"-B",
			"-c",
			restoreWorkspaceScript,
			sb.ResumeCheckpoint,
			checkpointEmptyBase,
		},
		"securityContext": map[string]any{
			"runAsUser":                int64(0),
			"readOnlyRootFilesystem":   true,
			"allowPrivilegeEscalation": false,
			"capabilities": map[string]any{
				"drop": []string{"ALL"}, "add": []string{"CHOWN", "FOWNER", "DAC_OVERRIDE"},
			},
			"seccompProfile": map[string]any{"type": "RuntimeDefault"},
		},
		"volumeMounts": []any{
			map[string]any{"name": "workspace", "mountPath": "/volume"},
			map[string]any{"name": "checkpoints", "mountPath": "/checkpoints", "readOnly": true},
		},
		"resources": map[string]any{
			"requests": map[string]string{"cpu": "50m", "memory": "64Mi"},
			"limits":   map[string]string{"cpu": "1", "memory": "256Mi"},
		},
	}
}

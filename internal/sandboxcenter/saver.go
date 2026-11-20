// saver.go closes the required-save loop: once a save-blocked sandbox is
// durably stopped, a per-sandbox saver pod packs the workspace PVC into a
// content-addressed checkpoint, the control plane confirms the checkpoint in
// the same transaction that unblocks destruction, and the saver pod is
// deleted by UID.
package sandboxcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

const (
	saverPodPrefix = "saver-"

	// checkpointEmptyBase is the empty-tree base layer digest the saver uses
	// for phase 2. Restore always validates against this same base.
	checkpointEmptyBase = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// CheckpointConfig pins the saver runtime. StoreHostPath keeps the legacy
// single-node layout; StorePVC selects a shared volume such as JuiceFS.
type CheckpointConfig struct {
	Image         string
	StoreHostPath string
	StorePVC      string
}

func (c CheckpointConfig) valid() error {
	switch {
	case c.StoreHostPath != "" && c.StorePVC != "":
		return fmt.Errorf("%w: checkpoint host path and PVC are mutually exclusive", ErrInvalidInput)
	case c.Image == "" || (c.StoreHostPath == "" && c.StorePVC == "") ||
		(c.StoreHostPath != "" && !filepath.IsAbs(c.StoreHostPath)):
		return fmt.Errorf("%w: saver image and one checkpoint store are required", ErrInvalidInput)
	}
	return nil
}

func checkpointVolume(cfg CheckpointConfig, hostPathType string) map[string]any {
	if cfg.StorePVC != "" {
		return map[string]any{"name": "checkpoints", "persistentVolumeClaim": map[string]any{
			"claimName": cfg.StorePVC,
		}}
	}
	return map[string]any{"name": "checkpoints", "hostPath": map[string]any{
		"path": cfg.StoreHostPath, "type": hostPathType,
	}}
}

// Save drives one save pass for a stopped save-blocked sandbox. It is
// idempotent: a checkpoint already confirmed for this generation is reused,
// and a stale saver pod from a previous pass is adopted.
func (s *Service) Save(ctx context.Context, sandbox Sandbox) error {
	if sandbox.Status != SandboxStatusSaveBlocked || !sandbox.RequiredSave {
		return ErrConflict
	}
	if sandbox.Generation == 0 && sandbox.ResumeCheckpoint != "" {
		return ErrConflict
	}
	if sandbox.StoppedAt == nil {
		return ErrNotReady
	}
	var podUID string
	err := s.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var acquired bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(hashtextextended(?, 0))",
			"sandbox-save:"+sandbox.ID).Scan(&acquired).Error; err != nil {
			return err
		}
		if !acquired {
			return ErrNotReady
		}
		store := NewStore(tx)
		current, err := store.GetSandbox(ctx, sandbox.ID)
		if err != nil {
			return err
		}
		if current.Generation != sandbox.Generation {
			return ErrConflict
		}
		if current.SavedCheckpoint != "" {
			if current.Status == SandboxStatusReleasing || current.Status == SandboxStatusDestroyed {
				return nil
			}
			return ErrConflict
		}
		if current.Status != SandboxStatusSaveBlocked || current.StoppedAt == nil {
			return ErrConflict
		}
		saver := *s
		saver.store = store
		return saver.saveCheckpoint(ctx, *current, &podUID)
	})
	if err != nil {
		return err
	}
	if s.checkpointConfig.StorePVC != "" || sandbox.CheckpointAttempt != "" {
		current, err := s.store.GetSandbox(ctx, sandbox.ID)
		if err != nil {
			return err
		}
		if current.Status == SandboxStatusSaveBlocked {
			return ErrNotReady
		}
		return s.completeSave(ctx, *current)
	}
	if podUID != "" {
		return s.provider.destroyObject(ctx, "pods", saverPodPrefix+sandbox.ID, podUID)
	}
	return s.completeSave(ctx, sandbox)
}

func (s *Service) saveCheckpoint(ctx context.Context, sandbox Sandbox, savedPodUID *string) error {
	cfg := s.checkpointConfig
	if err := cfg.valid(); err != nil {
		return err
	}
	if cfg.StorePVC != "" {
		return s.reconcileCheckpoint(ctx, sandbox)
	}
	if sandbox.CheckpointAttempt != "" {
		return fmt.Errorf("%w: registered checkpoint requires its PVC store", ErrConflict)
	}
	name := saverPodPrefix + sandbox.ID
	out, err := s.provider.kubectl(ctx, nil, "get", "pod", name, "-o", "json", "--ignore-not-found")
	if err != nil {
		return err
	}
	var podUID string
	var phase string
	if len(out) > 0 {
		var pod struct {
			Metadata struct {
				UID    string            `json:"uid"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		}
		if err := json.Unmarshal(out, &pod); err != nil {
			return err
		}
		if pod.Metadata.Labels["sandbox-center/id"] != sandbox.ID ||
			pod.Metadata.Labels["app.kubernetes.io/managed-by"] != "sandbox-center" ||
			pod.Metadata.Labels["app.kubernetes.io/component"] != "saver" ||
			pod.Metadata.Labels["sandbox-center/sandbox-generation"] != fmt.Sprint(sandbox.Generation) {
			return fmt.Errorf("%w: saver pod %s has foreign identity", ErrConflict, name)
		}
		podUID = pod.Metadata.UID
		phase = pod.Status.Phase
	}
	if podUID == "" {
		manifest, err := json.Marshal(saverPodManifest(sandbox, cfg))
		if err != nil {
			return err
		}
		created, err := s.provider.kubectl(ctx, manifest, "apply", "-f", "-", "-o", "json")
		if err != nil {
			return err
		}
		var pod struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(created, &pod); err != nil {
			return err
		}
		podUID = pod.Metadata.UID
		if podUID == "" {
			return errors.New("saver pod has no uid")
		}
		// Freshly created: let the reconciler adopt the result on the next
		// sweep so one Save call never blocks on pod runtime.
		return ErrNotReady
	}
	switch phase {
	case "Succeeded":
	case "Failed":
		logs, _ := s.provider.kubectl(ctx, nil, "logs", name)
		return fmt.Errorf("%w: saver pod %s failed: %s", ErrConflict, name, strings.TrimSpace(string(logs)))
	default:
		// Still scheduling or packing; the reconciler retries on the next
		// sweep.
		return ErrNotReady
	}
	result, err := s.provider.kubectl(ctx, nil, "logs", name)
	if err != nil {
		return fmt.Errorf("read checkpoint result: %w", err)
	}
	var checkpoint string
	for _, line := range strings.Split(string(result), "\n") {
		if strings.HasPrefix(line, "checkpoint: ") {
			checkpoint = strings.TrimSpace(strings.TrimPrefix(line, "checkpoint: "))
		}
	}
	if checkpoint == "" {
		return fmt.Errorf("%w: saver pod %s produced no checkpoint line", ErrNotReady, name)
	}
	if err := validateCheckpointID(checkpoint); err != nil {
		return err
	}
	*savedPodUID = podUID
	return s.store.confirmRequiredSave(ctx, sandbox.ID, sandbox.Generation, sandbox.Version, checkpoint)
}

// completeSave deletes the saver pod once the checkpoint is durably recorded.
// The sandbox itself moves to Releasing via confirmRequiredSave; the
// reconciler's existing branch then destroys pod and PVC.
func (s *Service) completeSave(ctx context.Context, sandbox Sandbox) error {
	if sandbox.CheckpointAttempt != "" {
		for _, role := range []string{"saver", "verify"} {
			pod, err := s.provider.checkpointPod(ctx, sandbox, role)
			if err != nil {
				return err
			}
			if pod != nil {
				if err := s.provider.destroyObject(
					ctx,
					"pods",
					role+"-"+sandbox.ID,
					pod.Metadata.UID,
				); err != nil {
					return err
				}
			}
		}
		return nil
	}
	name := saverPodPrefix + sandbox.ID
	out, err := s.provider.kubectl(ctx, nil, "get", "pod", name, "-o", "json", "--ignore-not-found")
	if err != nil {
		return err
	}
	if len(out) == 0 {
		return nil
	}
	var pod struct {
		Metadata struct {
			UID    string            `json:"uid"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &pod); err != nil {
		return err
	}
	if pod.Metadata.Labels["sandbox-center/id"] != sandbox.ID ||
		pod.Metadata.Labels["app.kubernetes.io/managed-by"] != "sandbox-center" ||
		pod.Metadata.Labels["app.kubernetes.io/component"] != "saver" ||
		pod.Metadata.Labels["sandbox-center/sandbox-generation"] != fmt.Sprint(sandbox.Generation) {
		return ErrConflict
	}
	return s.provider.destroyObject(ctx, "pods", name, pod.Metadata.UID)
}

func validateCheckpointID(checkpoint string) error {
	parts := strings.Split(checkpoint, ":")
	if len(parts) != 3 || parts[0] != "sha256" {
		return fmt.Errorf("%w: checkpoint must be sha256:<manifest>:<layer>", ErrInvalidInput)
	}
	for _, hex := range parts[1:] {
		if len(hex) != 64 {
			return fmt.Errorf("%w: checkpoint digest must be 64 hex characters", ErrInvalidInput)
		}
		for _, char := range hex {
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				return fmt.Errorf("%w: checkpoint digest must be lowercase hex", ErrInvalidInput)
			}
		}
	}
	return nil
}

func saverPodManifest(sb Sandbox, cfg CheckpointConfig) map[string]any {
	workspaceMount := map[string]any{"name": "workspace", "mountPath": "/workspace", "readOnly": true}
	if sb.ResumeCheckpoint != "" {
		workspaceMount["subPath"] = "workspace"
	}
	labels := map[string]string{
		"sandbox-center/id":                 sb.ID,
		"app.kubernetes.io/managed-by":      "sandbox-center",
		"app.kubernetes.io/component":       "saver",
		"app.kubernetes.io/part-of":         "agentic-sandbox",
		"sandbox-center/sandbox-generation": fmt.Sprint(sb.Generation),
	}
	var arguments []string
	if sb.CheckpointAttempt != "" {
		labels["sandbox-center/checkpoint-attempt"] = sb.CheckpointAttempt
		arguments = []string{checkpointDirectory(sb), sb.ID, fmt.Sprint(sb.Generation), sb.CheckpointAttempt}
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":   saverPodPrefix + sb.ID,
			"labels": labels,
		},
		"spec": map[string]any{
			"restartPolicy":                 "Never",
			"automountServiceAccountToken":  false,
			"terminationGracePeriodSeconds": 1,
			"activeDeadlineSeconds":         300,
			"containers": []any{map[string]any{
				"name":  "saver",
				"image": cfg.Image,
				"args":  arguments,
				"securityContext": map[string]any{
					"runAsUser":                0,
					"readOnlyRootFilesystem":   true,
					"allowPrivilegeEscalation": false,
					// DAC_OVERRIDE is required to read the workspace volume,
					// which the sandbox init container owns to the workload
					// identity 65534 with mode 0750.
					"capabilities": map[string]any{
						"drop": []string{"ALL"},
						"add":  []string{"DAC_OVERRIDE"},
					},
				},
				"volumeMounts": []any{
					workspaceMount,
					map[string]any{"name": "checkpoints", "mountPath": "/checkpoints"},
				},
				"resources": map[string]any{
					"requests": map[string]string{"cpu": "50m", "memory": "64Mi"},
					"limits":   map[string]string{"cpu": "1", "memory": "256Mi"},
				},
			}},
			"volumes": []any{
				map[string]any{
					"name": "workspace",
					"persistentVolumeClaim": map[string]any{
						"claimName": "workspace-" + sb.ID,
						"readOnly":  true,
					},
				},
				checkpointVolume(cfg, "DirectoryOrCreate"),
			},
		},
	}
}

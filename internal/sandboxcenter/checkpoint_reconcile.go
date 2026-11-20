package sandboxcenter

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"gorm.io/gorm"
)

const maxCheckpointRetries = 2

func checkpointDirectory(sandbox Sandbox) string {
	if sandbox.CheckpointAttempt == "" {
		return ""
	}
	return path.Join("saves", sandbox.ID, fmt.Sprint(sandbox.Generation), sandbox.CheckpointAttempt)
}

func (s *Service) reconcileCheckpoint(ctx context.Context, sandbox Sandbox) error {
	if sandbox.WorkspaceUID == "" {
		return fmt.Errorf("%w: missing workspace identity for checkpoint", ErrConflict)
	}
	if sandbox.CheckpointAttempt == "" {
		return s.store.db.WithContext(ctx).Model(&Sandbox{}).
			Where("id = ? AND version = ? AND status = ? AND checkpoint_attempt = ''",
				sandbox.ID, sandbox.Version, SandboxStatusSaveBlocked).
			Updates(map[string]any{
				"checkpoint_attempt": strings.ToLower(rand.Text()),
				"version":            gorm.Expr("version + 1"),
			}).Error
	}

	saver, err := s.provider.checkpointPod(ctx, sandbox, "saver")
	if err != nil {
		return err
	}
	var saverUID string
	if saver != nil {
		if saver.Status.Phase != "Succeeded" && saver.Status.Phase != "Failed" {
			return ErrNotReady
		}
		saverUID = saver.Metadata.UID
	}
	verifier, err := s.provider.checkpointPod(ctx, sandbox, "verify")
	if err != nil {
		return err
	}
	if verifier == nil {
		return s.provider.createCheckpointPod(
			ctx,
			checkpointVerifierManifest(sandbox, s.checkpointConfig, saverUID),
		)
	}
	name := "verify-" + sandbox.ID
	if verifier.Metadata.Annotations["sandbox-center/saver-uid"] != saverUID {
		if err := s.provider.destroyObject(ctx, "pods", name, verifier.Metadata.UID); err != nil {
			return err
		}
		return ErrNotReady
	}
	if verifier.Status.Phase == "Failed" {
		logs, _ := s.provider.kubectl(ctx, nil, "logs", name)
		return fmt.Errorf(
			"%w: checkpoint verification failed: %s",
			ErrConflict,
			strings.TrimSpace(string(logs)),
		)
	}
	if verifier.Status.Phase != "Succeeded" {
		return ErrNotReady
	}
	for _, container := range verifier.Status.ContainerStatuses {
		if container.Name != "verify" || container.State.Terminated == nil {
			continue
		}
		message := container.State.Terminated.Message
		if message == "absent" && saver == nil {
			workspaceUID, err := s.provider.objectUID(
				ctx,
				"persistentvolumeclaims",
				"workspace-"+sandbox.ID,
			)
			if err != nil {
				return err
			}
			if workspaceUID != sandbox.WorkspaceUID {
				return fmt.Errorf("%w: checkpoint workspace UID changed or disappeared", ErrConflict)
			}
			if err := s.provider.destroyObject(ctx, "pods", name, verifier.Metadata.UID); err != nil {
				return err
			}
			return s.provider.createCheckpointPod(ctx, saverPodManifest(sandbox, s.checkpointConfig))
		}
		if message == "absent" || message == "incomplete" {
			return s.retryCheckpoint(ctx, sandbox, saverUID, verifier.Metadata.UID)
		}
		if !strings.HasPrefix(message, "checkpoint: ") {
			return fmt.Errorf("%w: saver attempt has no committed checkpoint (%s)", ErrConflict, message)
		}
		checkpoint := strings.TrimPrefix(message, "checkpoint: ")
		if err := validateCheckpointID(checkpoint); err != nil {
			return err
		}
		return s.store.confirmRequiredSave(ctx, sandbox.ID, sandbox.Generation, sandbox.Version, checkpoint)
	}
	return fmt.Errorf("%w: checkpoint verifier has no result", ErrConflict)
}

func (s *Service) retryCheckpoint(ctx context.Context, sandbox Sandbox, saverUID, verifierUID string) error {
	if sandbox.CheckpointRetryCount >= maxCheckpointRetries {
		return fmt.Errorf(
			"%w: checkpoint retry limit reached for attempt %s",
			ErrConflict,
			sandbox.CheckpointAttempt,
		)
	}
	if saverUID != "" {
		if err := s.provider.destroyObject(ctx, "pods", saverPodPrefix+sandbox.ID, saverUID); err != nil {
			return err
		}
	}
	if err := s.provider.destroyObject(ctx, "pods", "verify-"+sandbox.ID, verifierUID); err != nil {
		return err
	}
	return s.store.rotateCheckpointAttempt(ctx, sandbox)
}

func (s *Store) rotateCheckpointAttempt(ctx context.Context, sandbox Sandbox) error {
	result := s.db.WithContext(ctx).Model(&Sandbox{}).
		Where("id = ? AND generation = ? AND version = ? AND status = ? AND checkpoint_attempt = ?",
			sandbox.ID, sandbox.Generation, sandbox.Version, SandboxStatusSaveBlocked, sandbox.CheckpointAttempt).
		Where("checkpoint_retry_count < ?", maxCheckpointRetries).
		Updates(map[string]any{
			"checkpoint_attempt":     strings.ToLower(rand.Text()),
			"checkpoint_retry_count": gorm.Expr("checkpoint_retry_count + 1"),
			"version":                gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

type checkpointPod struct {
	Metadata struct {
		UID         string            `json:"uid"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name  string `json:"name"`
			State struct {
				Terminated *struct {
					Message string `json:"message"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (p *Provider) checkpointPod(ctx context.Context, sandbox Sandbox, role string) (*checkpointPod, error) {
	name := role + "-" + sandbox.ID
	out, err := p.kubectl(ctx, nil, "get", "pod", name, "-o", "json", "--ignore-not-found")
	if err != nil || len(out) == 0 {
		return nil, err
	}
	var pod checkpointPod
	if err := json.Unmarshal(out, &pod); err != nil {
		return nil, err
	}
	labels := pod.Metadata.Labels
	if pod.Metadata.UID == "" || labels["sandbox-center/id"] != sandbox.ID ||
		labels["app.kubernetes.io/managed-by"] != "sandbox-center" ||
		labels["app.kubernetes.io/component"] != role ||
		labels["sandbox-center/sandbox-generation"] != fmt.Sprint(sandbox.Generation) ||
		labels["sandbox-center/checkpoint-attempt"] != sandbox.CheckpointAttempt {
		return nil, fmt.Errorf("%w: checkpoint pod %s has foreign identity", ErrConflict, name)
	}
	return &pod, nil
}

func (p *Provider) createCheckpointPod(ctx context.Context, pod map[string]any) error {
	manifest, err := json.Marshal(pod)
	if err != nil {
		return err
	}
	if _, err := p.kubectl(ctx, manifest, "create", "-f", "-"); err != nil {
		return err
	}
	return ErrNotReady
}

func checkpointVerifierManifest(sandbox Sandbox, cfg CheckpointConfig, saverUID string) map[string]any {
	pod := saverPodManifest(sandbox, cfg)
	metadata := pod["metadata"].(map[string]any)
	metadata["name"] = "verify-" + sandbox.ID
	metadata["annotations"] = map[string]string{"sandbox-center/saver-uid": saverUID}
	metadata["labels"].(map[string]string)["app.kubernetes.io/component"] = "verify"
	spec := pod["spec"].(map[string]any)
	spec["volumes"] = []any{checkpointVolume(cfg, "Directory")}
	container := spec["containers"].([]any)[0].(map[string]any)
	container["name"] = "verify"
	container["command"] = []string{"python3", "-B", "/usr/local/bin/layerfs_checkpoint.py", "verify"}
	container["args"] = []string{
		path.Join("/checkpoints", checkpointDirectory(sandbox)),
		checkpointEmptyBase, sandbox.ID, fmt.Sprint(sandbox.Generation), sandbox.CheckpointAttempt,
	}
	container["volumeMounts"] = []any{map[string]any{
		"name": "checkpoints", "mountPath": "/checkpoints", "readOnly": true,
	}}
	return pod
}

// provider_k8s.go drives the minimal Kubernetes sandbox runtime for phase 2.
// It shells out to kubectl against an explicitly configured context and
// creates one Pod per sandbox with the guard + patched execd combined image.
// The guard speaks over a root-only unix socket; the control plane reaches it
// through `kubectl exec` with the pod's guard binary as the client.
package sandboxcenter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// K8sConfig pins everything the provider touches. Context is required so the
// provider can never mutate whatever cluster happens to be current.
type K8sConfig struct {
	Context   string
	Namespace string

	// GuardImage is the combined guard + patched execd image, digest-pinned.
	GuardImage string

	// RuntimeClassName stays empty until the gVisor node pool exists (stage 4).
	RuntimeClassName string

	WorkspaceStorageClass string
	WorkspaceAccessMode   string

	// CPULimit, MemoryLimit, PIDLimit mirror the verified docker probe bounds.
	CPULimit string

	MemoryLimit string
	PIDLimit    int64

	// ProvisionDeadline bounds pod creation to Running.
	ProvisionDeadline time.Duration

	// MaxLifetime is the guard instance hard lifetime. Guard itself refuses
	// anything above 24h, so this is clamped to 86400s.
	MaxLifetime time.Duration

	Checkpoints CheckpointConfig
}

func (c K8sConfig) valid() error {
	if c.Context == "" || c.Namespace == "" || c.GuardImage == "" {
		return fmt.Errorf("%w: k8s context, namespace and guard image are required", ErrInvalidInput)
	}
	switch c.WorkspaceAccessMode {
	case "", "ReadWriteOnce", "ReadWriteMany":
	default:
		return fmt.Errorf("%w: workspace access mode must be ReadWriteOnce or ReadWriteMany", ErrInvalidInput)
	}
	return nil
}

// Provider schedules and tears down sandbox pods.
type Provider struct {
	cfg K8sConfig
}

func NewProvider(cfg K8sConfig) (*Provider, error) {
	if err := cfg.valid(); err != nil {
		return nil, err
	}
	if cfg.WorkspaceAccessMode == "" {
		cfg.WorkspaceAccessMode = "ReadWriteOnce"
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "1"
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "256Mi"
	}
	if cfg.PIDLimit == 0 {
		cfg.PIDLimit = 128
	}
	if cfg.ProvisionDeadline == 0 {
		cfg.ProvisionDeadline = 90 * time.Second
	}
	if cfg.MaxLifetime <= 0 || cfg.MaxLifetime > 86400*time.Second {
		cfg.MaxLifetime = 86400 * time.Second
	}
	return &Provider{cfg: cfg}, nil
}

// LeaseDeadline clamps a requested deadline to the guard instance lifetime.
// anchor is the moment the guard instance started (lease creation in phase 2);
// the guard's own hard deadline is not later than anchor+MaxLifetime, and the
// margin covers the gap between anchor and container start.
func (p *Provider) LeaseDeadline(anchor, requested time.Time) time.Time {
	limit := anchor.Add(p.cfg.MaxLifetime - time.Minute)
	if requested.After(limit) {
		return limit.UTC()
	}
	return requested.UTC()
}

func (p *Provider) kubectl(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	base := []string{"--context", p.cfg.Context, "-n", p.cfg.Namespace, "--request-timeout=30s"}
	cmd := exec.CommandContext(ctx, "kubectl", append(base, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf(
			"kubectl %s: %w: %s",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(out)),
		)
	}
	return out, nil
}

// Kubectl exposes raw kubectl access for e2e fixtures that assert on real
// workload state (pod phases, file contents). Production code must go through
// the typed provider methods instead.
func (p *Provider) Kubectl(ctx context.Context, args ...string) (string, error) {
	out, err := p.kubectl(ctx, nil, args...)
	return string(out), err
}

// podManifest builds the single-container guard pod. /state is root-only and
// /workspace is owned by the fixed workload identity 65534; root filesystem is
// read-only, all capabilities dropped except SETUID/SETGID for the guard to
// drop privilege into the workload identity.
func (p *Provider) podManifest(sb Sandbox, admission admissionRequest) map[string]any {
	name := "sandbox-" + sb.ID
	hardSeconds := int64(p.cfg.MaxLifetime / time.Second)
	if hardSeconds < 1 {
		hardSeconds = 1
	}
	pod := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "sandbox-center",
				"sandbox-center/id":            sb.ID,
			},
		},
		"spec": map[string]any{
			"restartPolicy":                "Never",
			"automountServiceAccountToken": false,
			"initContainers": []any{
				map[string]any{
					"name":            "prepare",
					"image":           p.cfg.GuardImage,
					"imagePullPolicy": "IfNotPresent",
					// /state must be root-owned 0700 and /workspace writable by
					// the fixed workload identity 65534.
					"command": []string{
						"sh",
						"-c",
						"chmod 0700 /state && chown 65534:65534 /workspace && chmod 0750 /workspace",
					},
					"securityContext": map[string]any{
						"runAsUser":                int64(0),
						"allowPrivilegeEscalation": false,
						"capabilities": map[string]any{
							"drop": []string{"ALL"},
							"add":  []string{"CHOWN", "FOWNER"},
						},
					},
					"volumeMounts": []any{
						map[string]any{"name": "state", "mountPath": "/state"},
						map[string]any{"name": "workspace", "mountPath": "/workspace"},
					},
				},
			},
			"containers": []any{
				map[string]any{
					"name":            "guard",
					"image":           p.cfg.GuardImage,
					"imagePullPolicy": "IfNotPresent",
					"command":         []string{"/guard", "serve", fmt.Sprint(hardSeconds)},
					"env": []any{map[string]any{
						"name": "SANDBOX_POD_UID",
						"valueFrom": map[string]any{
							"fieldRef": map[string]string{"fieldPath": "metadata.uid"},
						},
					}},
					"resources": map[string]any{
						"limits": map[string]any{
							"cpu":    p.cfg.CPULimit,
							"memory": p.cfg.MemoryLimit,
						},
					},
					"securityContext": map[string]any{
						"runAsUser":                int64(0),
						"readOnlyRootFilesystem":   true,
						"allowPrivilegeEscalation": false,
						"capabilities": map[string]any{
							"drop": []string{"ALL"},
							"add":  []string{"SETUID", "SETGID"},
						},
						"seccompProfile": map[string]any{"type": "RuntimeDefault"},
					},
					"volumeMounts": []any{
						map[string]any{"name": "state", "mountPath": "/state"},
						map[string]any{"name": "workspace", "mountPath": "/workspace"},
						map[string]any{"name": "tmp", "mountPath": "/tmp"},
					},
				},
			},
			"volumes": []any{
				map[string]any{"name": "state", "emptyDir": map[string]any{}},
				map[string]any{"name": "workspace", "emptyDir": map[string]any{}},
				map[string]any{
					"name":     "tmp",
					"emptyDir": map[string]any{"sizeLimit": "16Mi", "medium": "Memory"},
				},
			},
		},
	}
	if p.cfg.RuntimeClassName != "" {
		pod["spec"].(map[string]any)["runtimeClassName"] = p.cfg.RuntimeClassName
	}
	if sb.RequiredSave {
		volumes := pod["spec"].(map[string]any)["volumes"].([]any)
		volumes[1] = map[string]any{
			"name":                  "workspace",
			"persistentVolumeClaim": map[string]any{"claimName": "workspace-" + sb.ID},
		}
	}
	if sb.ResumeCheckpoint != "" {
		spec := pod["spec"].(map[string]any)
		initializers := spec["initContainers"].([]any)
		prepare := initializers[0].(map[string]any)
		prepare["command"] = []string{"sh", "-c", "chmod 0700 /state"}
		prepare["volumeMounts"] = []any{map[string]any{"name": "state", "mountPath": "/state"}}
		spec["initContainers"] = append([]any{restoreInitContainer(sb, p.cfg.Checkpoints)}, initializers...)
		guard := spec["containers"].([]any)[0].(map[string]any)
		guard["volumeMounts"].([]any)[1].(map[string]any)["subPath"] = "workspace"
		spec["volumes"] = append(spec["volumes"].([]any), checkpointVolume(p.cfg.Checkpoints, "Directory"))
	}
	return pod
}

func (p *Provider) ensureWorkspace(ctx context.Context, sb Sandbox) (string, error) {
	spec := map[string]any{
		"accessModes": []string{p.cfg.WorkspaceAccessMode},
		"resources": map[string]any{
			"requests": map[string]string{"storage": "1Gi"},
		},
	}
	if p.cfg.WorkspaceStorageClass != "" {
		spec["storageClassName"] = p.cfg.WorkspaceStorageClass
	}
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name": "workspace-" + sb.ID,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "sandbox-center",
				"sandbox-center/id":            sb.ID,
			},
		},
		"spec": spec,
	})
	if err != nil {
		return "", err
	}
	out, err := p.kubectl(ctx, manifest, "apply", "-f", "-", "-o", "json")
	if err != nil {
		return "", err
	}
	var claim struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &claim); err != nil {
		return "", err
	}
	if claim.Metadata.UID == "" {
		return "", errors.New("workspace has no uid")
	}
	return claim.Metadata.UID, nil
}

// Provision creates the pod and returns its UID.
func (p *Provider) Provision(
	ctx context.Context,
	sb Sandbox,
	admission admissionRequest,
	checkpoints CheckpointConfig,
) (podUID string, err error) {
	if sb.RestoreFromSandboxID != "" || sb.ResumeCheckpoint != "" {
		if !sb.RequiredSave || sb.RestoreFromSandboxID == "" {
			return "", ErrInvalidInput
		}
		if err := validateCheckpointID(sb.ResumeCheckpoint); err != nil {
			return "", err
		}
		if err := checkpoints.valid(); err != nil {
			return "", err
		}
		if sb.ResumeCheckpointDir != "" &&
			(checkpoints.StorePVC == "" || !filepath.IsLocal(sb.ResumeCheckpointDir)) {
			return "", fmt.Errorf(
				"%w: restoring a registered checkpoint requires its PVC store",
				ErrInvalidInput,
			)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, p.cfg.ProvisionDeadline)
	defer cancel()

	configured := *p
	configured.cfg.Checkpoints = checkpoints
	manifest, err := json.Marshal(configured.podManifest(sb, admission))
	if err != nil {
		return "", err
	}
	out, err := p.kubectl(ctx, manifest, "apply", "-f", "-", "-o", "json")
	if err != nil {
		return "", err
	}
	var pod struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &pod); err != nil {
		return "", err
	}
	podUID = pod.Metadata.UID
	if podUID == "" {
		return "", errors.New("pod has no uid")
	}
	return podUID, nil
}

func (p *Provider) waitReady(ctx context.Context, sb Sandbox) error {
	_, err := p.kubectl(ctx, nil, "wait", "--for=condition=Ready", "pod/sandbox-"+sb.ID,
		"--timeout="+fmt.Sprint(int64(p.cfg.ProvisionDeadline.Seconds()))+"s")
	return err
}

// Destroy deletes the pod. NotFound is success: the sandbox is gone.
func (p *Provider) Destroy(ctx context.Context, sb Sandbox) error {
	if sb.Status == SandboxStatusSaveBlocked || (sb.RequiredSave && sb.SavedCheckpoint == "") {
		return ErrSaveBlocked
	}
	name := "sandbox-" + sb.ID
	podUID := sb.PodUID
	if podUID == "" {
		var err error
		podUID, err = p.observeIdentity(ctx, sb.ID, "pods", name, "")
		if err != nil {
			return err
		}
	}
	if podUID != "" {
		if err := p.destroyObject(ctx, "pods", name, podUID); err != nil {
			return err
		}
	}
	if sb.RequiredSave {
		return p.destroyObject(ctx, "persistentvolumeclaims", "workspace-"+sb.ID, sb.WorkspaceUID)
	}
	return nil
}

func (p *Provider) Stop(ctx context.Context, sb Sandbox) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	requested := false
	for {
		out, err := p.kubectl(ctx, nil, "get", "pod", "sandbox-"+sb.ID, "-o", "json")
		if err != nil {
			return err
		}
		var pod struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					Name  string `json:"name"`
					State struct {
						Terminated *struct{} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		}
		if err := json.Unmarshal(out, &pod); err != nil {
			return err
		}
		if sb.PodUID == "" || pod.Metadata.UID != sb.PodUID {
			return fmt.Errorf("%w: stop requires the original pod UID", ErrConflict)
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "guard" && status.State.Terminated != nil {
				return nil
			}
		}
		if !requested {
			_, _, _ = p.guardCall(ctx, sb, "/stop", map[string]string{"podUid": sb.PodUID})
			requested = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// PodExists reports whether the sandbox pod object still exists in the
// cluster. The reconciler uses it to close the gap between durable state and
// provider evidence: a destroyed pod is proof, a missing pod is proof, but a
// running pod past its lease deadline must be deleted before the record can
// be closed.
func (p *Provider) PodExists(ctx context.Context, sb Sandbox) (bool, error) {
	name := "sandbox-" + sb.ID
	out, err := p.kubectl(ctx, nil, "get", "pod", name, "-o", "jsonpath={.metadata.uid}", "--ignore-not-found")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// guardCall runs the guard client inside the pod: the only control-plane
// channel to the root-only unix socket without adding a TCP listener.
func (p *Provider) guardCall(ctx context.Context, sb Sandbox, path string, payload any) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	base := []string{"--context", p.cfg.Context, "-n", p.cfg.Namespace, "--request-timeout=60s"}
	cmd := exec.CommandContext(
		ctx,
		"kubectl",
		append(base, "exec", "sandbox-"+sb.ID, "--", "/guard", "call", path, string(body))...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	// The probe client prints "<status> <body>" and exits 1 for status >= 400,
	// so the exit code alone is not the control result.
	line := stdout.String()
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return nil, 0, fmt.Errorf(
			"guard %s: no control response: %v: %s",
			path,
			runErr,
			strings.TrimSpace(stderr.String()),
		)
	}
	status, parseErr := strconv.Atoi(line[:idx])
	if parseErr != nil {
		return nil, 0, fmt.Errorf("guard %s: malformed control response %q", path, line)
	}
	payloadOut := []byte(line[idx+1:])
	if status >= 400 {
		return payloadOut, status, fmt.Errorf(
			"guard %s: %d %s",
			path,
			status,
			strings.TrimSpace(string(payloadOut)),
		)
	}
	return payloadOut, status, nil
}

// admit installs or renews the guard admission after the control plane has
// durably accepted the lease fact.
func (p *Provider) admit(ctx context.Context, sb Sandbox, req admissionRequest) error {
	_, _, err := p.guardCall(ctx, sb, "/admission", req)
	return err
}

// admitRequest mirrors the guard's admissionRequest JSON contract.
type admissionRequest struct {
	SandboxID      string    `json:"sandboxId"`
	RunID          string    `json:"runId"`
	WorkerID       string    `json:"workerId"`
	Epoch          int64     `json:"epoch"`
	LeaseID        string    `json:"leaseId"`
	FencingToken   int64     `json:"fencingToken"`
	AdmissionEpoch int64     `json:"admissionEpoch"`
	Generation     int64     `json:"generation"`
	RuntimeBootID  string    `json:"runtimeBootId"`
	Revision       int64     `json:"revision"`
	Deadline       time.Time `json:"deadline"`
	RequestKey     string    `json:"requestKey"`
}

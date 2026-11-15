package kubernetesprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	execdImage    = "opensandbox/execd@sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded"
	workloadImage = "opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b"
	readerImage   = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
)

type cluster struct{ namespace string }

func (c cluster) kubectl(input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	base := []string{"--context", "orbstack", "--request-timeout=35s"}
	if c.namespace != "" {
		base = append(base, "-n", c.namespace)
	}
	cmd := exec.CommandContext(ctx, "kubectl", append(base, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

func (c cluster) must(t *testing.T, input []byte, args ...string) []byte {
	t.Helper()
	b, err := c.kubectl(input, args...)
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, b)
	}
	return b
}

func (c cluster) create(t *testing.T, object any) {
	t.Helper()
	b, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	c.must(t, b, "create", "-f", "-")
}

func await(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("observation not reached before deadline")
}

func TestPodDeadline(t *testing.T) {
	if os.Getenv("KUBERNETES_DISPOSABLE_PROBE") != "orbstack" {
		t.Skip("requires explicit local orbstack experiment")
	}
	c := cluster{namespace: fmt.Sprintf("agentic-deadline-%d", time.Now().UnixNano())}
	// Hard-coded context: never use the user's current dev/qa context.
	c.must(t, nil, "create", "namespace", c.namespace)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := c.kubectl(nil, "get", "pods", "-o", "wide")
			t.Logf("fixture pods: %s", out)
			out, _ = c.kubectl(nil, "logs", "runtime", "-c", "runtime", "--tail=15")
			t.Logf("runtime logs: %s", out)
		}
		if out, err := c.kubectl(
			nil,
			"delete",
			"namespace",
			c.namespace,
			"--wait=true",
			"--timeout=30s",
		); err != nil {
			t.Errorf("namespace cleanup: %s %v", out, err)
		}
	})
	t.Logf("namespace=%s; native Pod+execd, no OpenSandbox manager/controller", c.namespace)
	c.create(
		t,
		map[string]any{
			"apiVersion": "v1",
			"kind":       "PersistentVolumeClaim",
			"metadata":   map[string]any{"name": "workspace"},
			"spec": map[string]any{
				"accessModes":      []string{"ReadWriteOnce"},
				"storageClassName": "local-path",
				"resources":        map[string]any{"requests": map[string]string{"storage": "64Mi"}},
			},
		},
	)
	resources := map[string]any{
		"requests": map[string]string{"cpu": "50m", "memory": "32Mi"},
		"limits":   map[string]string{"cpu": "1", "memory": "256Mi"},
	}
	security := map[string]any{
		"allowPrivilegeEscalation": false,
		"capabilities":             map[string]any{"drop": []string{"ALL"}},
	}
	c.create(
		t,
		map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]any{"name": "runtime"},
			"spec": map[string]any{
				"restartPolicy":                 "Never",
				"automountServiceAccountToken":  false,
				"activeDeadlineSeconds":         30,
				"terminationGracePeriodSeconds": 1,
				"securityContext": map[string]any{
					"seccompProfile": map[string]string{"type": "RuntimeDefault"},
				},
				"volumes": []any{
					map[string]any{"name": "runtime", "emptyDir": map[string]any{}},
					map[string]any{
						"name":                  "workspace",
						"persistentVolumeClaim": map[string]string{"claimName": "workspace"},
					},
				},
				"initContainers": []any{
					map[string]any{
						"name":            "install-execd",
						"image":           execdImage,
						"imagePullPolicy": "Never",
						"command": []string{
							"/bin/sh",
							"-c",
							"cp /execd /runtime/execd",
						},
						"resources":       resources,
						"securityContext": security,
						"volumeMounts": []any{
							map[string]any{"name": "runtime", "mountPath": "/runtime"},
						},
					},
				},
				"containers": []any{
					map[string]any{
						"name":            "runtime",
						"image":           workloadImage,
						"imagePullPolicy": "Never",
						"command":         []string{"/runtime/execd", "-init"},
						"resources":       resources,
						"securityContext": security,
						"volumeMounts": []any{
							map[string]any{
								"name":      "runtime",
								"mountPath": "/runtime",
								"readOnly":  true,
							},
							map[string]any{"name": "workspace", "mountPath": "/workspace"},
						},
					},
				},
			},
		},
	)
	c.must(t, nil, "wait", "--for=condition=Ready", "pod/runtime", "--timeout=25s")
	// Whether a Pod deadline can be lengthened is a selection fact, not assumed.
	patch, patchErr := c.kubectl(
		nil,
		"patch",
		"pod",
		"runtime",
		"--type=merge",
		"-p",
		`{"spec":{"activeDeadlineSeconds":45}}`,
	)
	t.Logf("extend activeDeadlineSeconds 30->45: err=%v response=%s", patchErr, patch)
	var pod struct {
		Spec   struct{ ActiveDeadlineSeconds int }
		Status struct {
			StartTime         time.Time
			Phase, Reason     string
			ContainerStatuses []struct {
				State struct {
					Terminated *struct {
						FinishedAt time.Time
						ExitCode   int
					}
				}
			}
		}
	}
	getPod := func() {
		if err := json.Unmarshal(c.must(t, nil, "get", "pod", "runtime", "-o", "json"), &pod); err != nil {
			t.Fatal(err)
		}
	}
	getPod()
	deadline := pod.Status.StartTime.Add(time.Duration(pod.Spec.ActiveDeadlineSeconds) * time.Second)
	if time.Until(deadline) < 8*time.Second {
		t.Fatal("fixture preparation consumed deadline; no valid observation window")
	}
	script := `import os,time
if os.fork()==0:
 os.setsid()
 while True:
  with open('/workspace/ticks','a') as f: f.write(str(time.time())+'\n'); f.flush()
  time.sleep(0.1)
else:
 time.sleep(90)
`
	input, _ := json.Marshal(
		map[string]any{
			"argv":       []string{"python3", "-c", script},
			"cwd":        "/workspace",
			"background": true,
			"timeout":    90000,
		},
	)
	launch := `import sys,urllib.request; print(urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:44772/command',data=sys.argv[1].encode(),headers={'Content-Type':'application/json'}),timeout=5).read().decode())`
	out := c.must(t, nil, "exec", "runtime", "-c", "runtime", "--", "python3", "-c", launch, string(input))
	if !bytes.Contains(out, []byte(`"type":"init"`)) {
		t.Fatalf("execd did not accept command: %s", out)
	}
	t.Logf("execd accepted detached writer; deadline=%s; stream=%s", deadline.Format(time.RFC3339Nano), out)
	await(t, 4*time.Second, func() bool {
		out, err := c.kubectl(nil, "exec", "runtime", "-c", "runtime", "--", "cat", "/workspace/ticks")
		return err == nil && bytes.Count(out, []byte("\n")) >= 2
	})
	// No manager process, renewals, or deletion calls during the deadline window.
	await(t, 55*time.Second, func() bool { getPod(); return pod.Status.Phase == "Failed" })
	if pod.Status.Reason != "DeadlineExceeded" || len(pod.Status.ContainerStatuses) != 1 ||
		pod.Status.ContainerStatuses[0].State.Terminated == nil {
		t.Fatalf("not confirmed stopped by deadline: %+v", pod.Status)
	}
	ended := pod.Status.ContainerStatuses[0].State.Terminated.FinishedAt
	t.Logf(
		"phase=%s reason=%s container_finish=%s deadline_lateness=%s",
		pod.Status.Phase,
		pod.Status.Reason,
		ended.Format(time.RFC3339Nano),
		ended.Sub(deadline),
	)
	if ended.Before(deadline.Add(-time.Second)) || ended.After(deadline.Add(5*time.Second)) {
		t.Error("container stop outside experimental deadline tolerance [-1s,+5s]")
	}
	late, lateErr := c.kubectl(nil, "exec", "runtime", "-c", "runtime", "--", "true")
	t.Logf("late exec after terminal Pod: err=%v response=%s", lateErr, late)
	if lateErr == nil || !bytes.Contains(late, []byte("completed pod")) {
		t.Error("expected explicit completed-pod rejection")
	}
	c.must(t, nil, "delete", "pod", "runtime", "--wait=true", "--timeout=10s")
	c.create(
		t,
		map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata":   map[string]any{"name": "reader"},
			"spec": map[string]any{
				"restartPolicy":                 "Never",
				"automountServiceAccountToken":  false,
				"activeDeadlineSeconds":         30,
				"terminationGracePeriodSeconds": 0,
				"volumes": []any{
					map[string]any{
						"name": "workspace",
						"persistentVolumeClaim": map[string]any{
							"claimName": "workspace",
							"readOnly":  true,
						},
					},
				},
				"containers": []any{
					map[string]any{
						"name":            "reader",
						"image":           readerImage,
						"imagePullPolicy": "Never",
						"command":         []string{"sleep", "30"},
						"resources":       resources,
						"securityContext": security,
						"volumeMounts": []any{
							map[string]any{
								"name":      "workspace",
								"mountPath": "/workspace",
								"readOnly":  true,
							},
						},
					},
				},
			},
		},
	)
	c.must(t, nil, "wait", "--for=condition=Ready", "pod/reader", "--timeout=20s")
	before := c.must(t, nil, "exec", "reader", "--", "cat", "/workspace/ticks")
	time.Sleep(1200 * time.Millisecond)
	after := c.must(t, nil, "exec", "reader", "--", "cat", "/workspace/ticks")
	if len(before) == 0 || !bytes.Equal(before, after) {
		t.Fatal("workspace lost or still changing after Pod termination")
	}
	lines := strings.Fields(string(after))
	last, err := strconv.ParseFloat(lines[len(lines)-1], 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(
		"PVC retained after Pod deletion; heartbeat_bytes=%d stable; last_write_relative_deadline=%.3fs",
		len(after),
		last-float64(deadline.UnixNano())/1e9,
	)
}

package isolatedprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dockerfile = `FROM opensandbox/execd@sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded AS runtime
FROM opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b
COPY --from=runtime /execd /execd
COPY --from=runtime /opt/opensandbox/ /opt/opensandbox/
COPY --from=runtime /usr/local/bin/bwrap /usr/local/bin/bwrap
COPY isolation.toml /etc/execd/isolation.toml
ENTRYPOINT ["/execd", "-init", "-isolation-config", "/etc/execd/isolation.toml"]
`

func docker(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func mustDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	b, err := docker(args...)
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, b)
	}
	return b
}

func request(base, method, path string, value any) (int, []byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, out, err
}

func eventually(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("observation not reached before deadline")
}

func TestIsolatedSessionTeardown(t *testing.T) {
	if os.Getenv("EXECD_ISOLATION_PROBE") != "1" {
		t.Skip("requires explicit disposable Docker experiment")
	}
	name := fmt.Sprintf("agentic-isolation-%d", time.Now().UnixNano())
	dir := t.TempDir()
	for file, content := range map[string]string{"Dockerfile": dockerfile, "isolation.toml": "[hardening]\nenabled=true\n[landlock]\nenabled=true\n"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if out, err := docker("image", "rm", name); err != nil {
			t.Errorf("image cleanup: %v %s", err, out)
		}
	})
	mustDocker(t, "build", "--network", "none", "--pull=false", "-t", name, dir)
	for _, mode := range []string{"restricted", "namespace-capable"} {
		t.Run(mode, func(t *testing.T) {
			container := name + "-" + mode
			volume := container + "-workspace"
			mustDocker(t, "volume", "create", volume)
			t.Cleanup(func() {
				if t.Failed() {
					logs, _ := docker("logs", "--tail", "30", container)
					t.Logf("runtime logs: %s", logs)
				}
				if out, err := docker("rm", "-f", container); err != nil {
					t.Errorf("container cleanup: %v %s", err, out)
				}
				if out, err := docker("volume", "rm", volume); err != nil {
					t.Errorf("volume cleanup: %v %s", err, out)
				}
			})
			args := []string{
				"run",
				"-d",
				"--name",
				container,
				"--cpus",
				"1",
				"--memory",
				"256m",
				"--pids-limit",
				"128",
				"-p",
				"127.0.0.1::44772",
				"--mount",
				"type=volume,src=" + volume + ",dst=/workspace",
			}
			if mode == "restricted" {
				args = append(args, "--cap-drop=ALL", "--security-opt", "no-new-privileges=true")
			} else {
				// Documented bwrap requirements. This is not the production Hardened profile.
				args = append(
					args,
					"--cap-add=SYS_ADMIN",
					"--security-opt",
					"seccomp=unconfined",
					"--security-opt",
					"apparmor=unconfined",
					"--security-opt",
					"systempaths=unconfined",
					"--tmpfs",
					"/var/lib/execd/isolation:rw,exec,nosuid,nodev",
				)
			}
			mustDocker(t, append(args, name)...)
			port := strings.TrimSpace(string(mustDocker(t, "port", container, "44772/tcp")))
			base := "http://" + port
			eventually(
				t,
				8*time.Second,
				func() bool { code, _, err := request(base, "GET", "/ping", nil); return err == nil && code == 200 },
			)
			status, caps, err := request(base, "GET", "/v1/isolated/capabilities", nil)
			if err != nil || status != 200 {
				t.Fatalf("capabilities: %d %s %v", status, caps, err)
			}
			t.Logf("capabilities: %s", caps)
			var capabilities struct{ Available bool }
			if err := json.Unmarshal(caps, &capabilities); err != nil {
				t.Fatal(err)
			}
			if mode == "restricted" {
				if capabilities.Available {
					t.Fatal(
						"unexpected namespace support under restricted mode; inspect before interpreting",
					)
				}
				return
			}
			if !capabilities.Available {
				t.Fatal("isolated sessions unavailable under documented namespace settings")
			}
			mustDocker(t, "exec", container, "chmod", "777", "/workspace")
			create := map[string]any{
				"profile":              "strict",
				"workspace":            map[string]string{"path": "/workspace", "mode": "rw"},
				"idle_timeout_seconds": 300,
				"uid":                  65534,
				"gid":                  65534,
				"uid_mode":             "setpriv",
			}
			status, body, err := request(base, "POST", "/v1/isolated/session", create)
			var session struct {
				ID string `json:"session_id"`
			}
			if err != nil || status >= 300 || json.Unmarshal(body, &session) != nil || session.ID == "" {
				t.Fatalf("create: %d %s %v", status, body, err)
			}
			t.Logf("created session: %s", body)
			path := "/v1/isolated/session/" + session.ID
			code := "python3 - <<'PY'\nimport os,time\nif os.fork()==0:\n os.setsid()\n while True:\n  with open('/workspace/ticks','a') as f: f.write(str(time.time())+'\\n'); f.flush()\n  time.sleep(0.1)\nelse:\n time.sleep(60)\nPY"
			status, body, err = request(
				base,
				"POST",
				path+"/run",
				map[string]any{"code": code, "background": true},
			)
			if err != nil || status != 202 {
				t.Fatalf("run: %d %s %v", status, body, err)
			}
			t.Logf("background run: %s", body)
			eventually(t, 5*time.Second, func() bool {
				out, err := docker("exec", container, "cat", "/workspace/ticks")
				return err == nil && bytes.Count(out, []byte("\n")) >= 3
			})
			before := mustDocker(t, "exec", container, "cat", "/workspace/ticks")
			time.Sleep(300 * time.Millisecond)
			growing := mustDocker(t, "exec", container, "cat", "/workspace/ticks")
			if len(growing) <= len(before) {
				t.Fatal("writer not active before DELETE")
			}
			start := time.Now()
			status, body, err = request(base, "DELETE", path, nil)
			if err != nil || status >= 300 {
				t.Fatalf("delete: %d %s %v", status, body, err)
			}
			after := mustDocker(t, "exec", container, "cat", "/workspace/ticks")
			time.Sleep(1200 * time.Millisecond)
			stable := mustDocker(t, "exec", container, "cat", "/workspace/ticks")
			t.Logf(
				"DELETE status=%d observation_elapsed=%s heartbeat_bytes=%d->%d",
				status,
				time.Since(start),
				len(after),
				len(stable),
			)
			if !bytes.Equal(after, stable) {
				t.Error("detached writer survived session deletion")
			}
			status, body, err = request(
				base,
				"POST",
				path+"/run",
				map[string]string{"code": "echo late >> /workspace/ticks"},
			)
			t.Logf("late run: status=%d response=%s err=%v", status, body, err)
			if err != nil || status != 404 {
				t.Error("expected explicit session-not-found rejection")
			}
		})
	}
}

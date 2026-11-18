package opensandboxprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// Official release-1.1.1 BOM. No pulls are performed by the probe.
const (
	serverImage = "opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b"
	execdImage  = "opensandbox/execd@sha256:cda2bc35409bbf57561599b316cafb899598f7dce671332e7ceda14eb1cfeded"
	readerImage = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	probeKey    = "disposable-local-probe"
)

func docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(b)), err
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	b, err := docker(args...)
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, b)
	}
	return b
}

func await(t *testing.T, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not observed before deadline")
}

func request(ctx context.Context, method, url string, input any, key string) (int, []byte, error) {
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OPEN-SANDBOX-API-KEY", probeKey)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

func call(t *testing.T, method, url string, input any, want int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	code, b, err := request(ctx, method, url, input, "")
	if err != nil || code != want {
		t.Fatalf("%s %s: want %d got %d: %s (%v)", method, url, want, code, b, err)
	}
	return b
}

type control struct{ name, url string }

func newControl(t *testing.T) control {
	t.Helper()
	// The upstream server restores all OpenSandbox-labelled containers on startup.
	// Refuse to share a daemon with existing sandboxes rather than touching them.
	if existing := mustDocker(t, "ps", "-aq", "--filter", "label=opensandbox.io/id"); existing != "" {
		t.Fatal("existing OpenSandbox containers found; use a separate Docker daemon")
	}
	name := fmt.Sprintf("agentic-osb-probe-%d", time.Now().UnixNano())
	networkCreated, containerCreated := false, false
	t.Cleanup(func() {
		if containerCreated {
			if t.Failed() {
				logs, _ := docker("logs", "--tail", "35", name)
				t.Logf("management server logs:\n%s", logs)
				state, _ := docker("inspect", name, "--format", "{{json .State}}")
				t.Logf("management server state: %s", state)
			}
			if out, err := docker("rm", "-f", name); err != nil {
				t.Errorf("server cleanup: %s %v", out, err)
			}
		}
		if networkCreated {
			if out, err := docker("network", "rm", name); err != nil {
				t.Errorf("network cleanup: %s %v", out, err)
			}
		}
	})
	mustDocker(t, "network", "create", name)
	networkCreated = true
	config := fmt.Sprintf(`[server]
host = "0.0.0.0"
port = 8080
api_key = %q
[runtime]
type = "docker"
execd_image = %q
[docker]
network_mode = %q
host_ip = "127.0.0.1"
publish_host = "127.0.0.1"
pids_limit = 128
no_new_privileges = true
[store]
type = "sqlite"
path = "/tmp/opensandbox-probe.db"
`, probeKey, execdImage, name)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	mustDocker(
		t,
		"create",
		"--name",
		name,
		"--network",
		name,
		"--cpus",
		"1",
		"--memory",
		"512m",
		"--pids-limit",
		"128",
		"-p",
		"127.0.0.1::8080",
		"-v",
		"/var/run/docker.sock:/var/run/docker.sock",
		serverImage,
	)
	containerCreated = true
	mustDocker(t, "cp", configPath, name+":/etc/opensandbox/config.toml")
	mustDocker(t, "start", name)
	c := control{name: name, url: "http://" + mustDocker(t, "port", name, "8080/tcp")}
	c.ready(t)
	t.Logf("server_image=%s execd_image=%s", serverImage, execdImage)
	t.Logf(
		"server_package_version=%s",
		mustDocker(
			t,
			"exec",
			name,
			"python",
			"-c",
			`import importlib.metadata; print(importlib.metadata.version("opensandbox-server"))`,
		),
	)
	return c
}

func (c *control) ready(t *testing.T) {
	t.Helper()
	// Docker can allocate a different ephemeral host port after stop/start.
	c.url = "http://" + mustDocker(t, "port", c.name, "8080/tcp")
	await(t, 20*time.Second, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		code, _, err := request(ctx, "GET", c.url+"/health", nil, "")
		return err == nil && code == 200
	})
}

type sandbox struct {
	id, name, volume, url string
	expires               time.Time
}

func (c control) create(t *testing.T, ttl int) sandbox {
	t.Helper()
	volume := fmt.Sprintf("agentic-osb-work-%d", time.Now().UnixNano())
	mustDocker(t, "volume", "create", volume)
	f := sandbox{volume: volume}
	t.Cleanup(func() {
		// The response may be lost after creation. The freshly created, unique
		// volume identifies this fixture even when no sandbox ID was received.
		containers, err := docker("ps", "-aq", "--filter", "volume="+volume)
		if err != nil {
			t.Errorf("find fixture containers: %s %v", containers, err)
		}
		for _, id := range strings.Fields(containers) {
			if out, err := docker("rm", "-f", id); err != nil {
				t.Errorf("sandbox cleanup: %s %v", out, err)
			}
		}
		if out, err := docker("volume", "rm", volume); err != nil {
			t.Errorf("workspace cleanup: %s %v", out, err)
		}
	})
	input := map[string]any{
		"image":          map[string]any{"uri": serverImage},
		"timeout":        ttl,
		"resourceLimits": map[string]string{"cpu": "1", "memory": "256Mi"},
		"entrypoint":     []string{"sleep", "infinity"},
		"metadata":       map[string]string{"agentic-probe": c.name},
		"volumes": []any{
			map[string]any{
				"name":      "workspace",
				"mountPath": "/workspace",
				"pvc": map[string]any{
					"claimName":                  volume,
					"createIfNotExists":          false,
					"deleteOnSandboxTermination": false,
				},
			},
		},
	}
	var result struct {
		ID        string    `json:"id"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	data := call(t, "POST", c.url+"/v1/sandboxes", input, 202)
	if err := json.Unmarshal(data, &result); err != nil || result.ID == "" || result.ExpiresAt.IsZero() {
		t.Fatalf("invalid creation response: %s (%v)", data, err)
	}
	f.id, f.name, f.expires = result.ID, "sandbox-"+result.ID, result.ExpiresAt
	await(t, 20*time.Second, func() bool {
		out, err := docker("port", f.name, "44772/tcp")
		if err == nil && strings.HasPrefix(out, "127.0.0.1:") {
			f.url = "http://" + out
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			code, _, err := request(ctx, "GET", f.url+"/ping", nil, "")
			return err == nil && code == 200
		}
		return false
	})
	t.Logf("sandbox=%s expires=%s", f.id, f.expires.Format(time.RFC3339Nano))
	return f
}

func (f sandbox) read(t *testing.T, file string) string {
	t.Helper()
	return mustDocker(
		t,
		"run",
		"--rm",
		"--network",
		"none",
		"--read-only",
		"--cap-drop",
		"ALL",
		"-v",
		f.volume+":/workspace:ro",
		readerImage,
		"cat",
		"/workspace/"+file,
	)
}

func (f sandbox) size(file string) int {
	b, err := docker("exec", f.name, "cat", "/workspace/"+file)
	if err != nil {
		return 0
	}
	return len(b)
}

func commandInput(script string, timeout int, background bool) map[string]any {
	return map[string]any{
		"argv":       []string{"python3", "-c", script},
		"cwd":        "/workspace",
		"timeout":    timeout,
		"background": background,
	}
}

func executionID(t *testing.T, data []byte) string {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		var event struct{ Type, Text string }
		if err := json.Unmarshal(
			[]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))),
			&event,
		); err == nil && event.Type == "init" &&
			event.Text != "" {
			return event.Text
		}
	}
	t.Fatalf("no init execution ID: %s", data)
	return ""
}

func (f sandbox) start(t *testing.T, script string, timeout int) string {
	t.Helper()
	return executionID(t, call(t, "POST", f.url+"/command", commandInput(script, timeout, true), 200))
}

func (f sandbox) finished(t *testing.T, id string) []byte {
	t.Helper()
	var data []byte
	await(t, 10*time.Second, func() bool {
		data = call(t, "GET", f.url+"/command/status/"+id, nil, 200)
		var state struct{ Running bool }
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return !state.Running
	})
	return data
}

func writer(detached bool) string {
	flag := "False"
	if detached {
		flag = "True"
	}
	return fmt.Sprintf(`import os,time
child=os.fork()
if child==0:
 if %s: os.setsid()
 with open('/workspace/child','w') as f: f.write(str(os.getpid()))
 while True:
  with open('/workspace/ticks','a') as f: f.write('tick\n'); f.flush()
  time.sleep(0.1)
else:
 time.sleep(90)
`, flag)
}

func TestOpenSandbox(t *testing.T) {
	if os.Getenv("OPENSANDBOX_DISPOSABLE_PROBE") != "1" {
		t.Skip("requires explicit disposable Docker experiment")
	}
	c := newControl(t)
	t.Run("command_stop", func(t *testing.T) {
		for _, mode := range []string{"kill", "timeout"} {
			for _, detached := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/detached=%t", mode, detached), func(t *testing.T) {
					f := c.create(t, 90)
					timeout := 60000
					if mode == "timeout" {
						timeout = 3000
					}
					id := f.start(t, writer(detached), timeout)
					await(t, 4*time.Second, func() bool { return f.size("ticks") > 8 })
					if mode == "kill" {
						call(t, "DELETE", f.url+"/command?id="+id, nil, 200)
					}
					state := f.finished(t, id)
					before := len(mustDocker(t, "exec", f.name, "cat", "/workspace/ticks"))
					time.Sleep(1200 * time.Millisecond)
					after := len(mustDocker(t, "exec", f.name, "cat", "/workspace/ticks"))
					t.Logf("status=%s heartbeat_bytes=%d->%d", state, before, after)
					if before == 0 {
						t.Fatal("probe observation failure: heartbeat unexpectedly empty")
					}
					if before != after {
						t.Error("command termination did not stop writer")
					}
				})
			}
		}
	})
	t.Run("expiry_retains_workspace", func(t *testing.T) {
		f := c.create(t, 60)
		f.start(t, writer(true), 120000)
		await(t, 4*time.Second, func() bool { return f.size("ticks") > 8 })
		await(t, 70*time.Second, func() bool {
			return mustDocker(t, "ps", "-aq", "--filter", "label=opensandbox.io/id="+f.id) == ""
		})
		lateness := time.Since(f.expires)
		if lateness < -time.Second || lateness > 5*time.Second {
			t.Errorf("environment removal outside experimental expiry tolerance [-1s,5s]: %s", lateness)
		}
		before := f.read(t, "ticks")
		time.Sleep(1200 * time.Millisecond)
		after := f.read(t, "ticks")
		if before == "" || before != after {
			t.Fatal("writer did not stop or workspace was lost")
		}
		t.Logf(
			"environment removed %s after expiresAt; detached heartbeat stable at %d bytes; independent volume retained",
			lateness,
			len(after),
		)
	})
	t.Run("management_crash_deadline", func(t *testing.T) {
		f := c.create(t, 60)
		f.start(t, writer(true), 120000)
		await(t, 4*time.Second, func() bool { return f.size("ticks") > 8 })
		mustDocker(t, "kill", "--signal", "KILL", c.name)
		if code := mustDocker(t, "wait", c.name); code != "137" {
			t.Fatalf("management exit: %s", code)
		}
		// Restore even if an observation fails; recreation is not a lease renewal.
		restored := false
		defer func() {
			if !restored {
				mustDocker(t, "start", c.name)
				c.ready(t)
			}
		}()
		await(t, 70*time.Second, func() bool { return time.Now().After(f.expires.Add(time.Second)) })
		before := f.read(t, "ticks")
		time.Sleep(1200 * time.Millisecond)
		after := f.read(t, "ticks")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		code, b, err := request(
			ctx,
			"POST",
			f.url+"/command",
			commandInput("print('late-execution')", 1000, true),
			"",
		)
		cancel()
		t.Logf(
			"past expiresAt with management down: heartbeat_bytes=%d->%d; new command HTTP=%d body=%s err=%v",
			len(before),
			len(after),
			code,
			b,
			err,
		)
		if before == "" || before != after || code == 200 {
			t.Error("management crash left expired environment writable")
		}
		mustDocker(t, "start", c.name)
		c.ready(t)
		restored = true
		await(t, 10*time.Second, func() bool {
			return mustDocker(t, "ps", "-aq", "--filter", "label=opensandbox.io/id="+f.id) == ""
		})
		stopped := f.read(t, "ticks")
		time.Sleep(1200 * time.Millisecond)
		if stopped == "" || stopped != f.read(t, "ticks") {
			t.Fatal("recovery cleanup failed to stop writer and retain workspace")
		}
		t.Log("management restart reconciled expired environment; stopped writer and preserved workspace")
	})
	t.Run("completed_result_after_restart", func(t *testing.T) {
		f := c.create(t, 90)
		id := f.start(
			t,
			"from pathlib import Path; Path('/workspace/receipt').write_text('once'); print('durable-output')",
			5000,
		)
		before := f.finished(t, id)
		logs := call(t, "GET", f.url+"/command/"+id+"/logs", nil, 200)
		if !bytes.Contains(logs, []byte("durable-output")) {
			t.Fatalf("missing original output: %s", logs)
		}
		oldStart := mustDocker(t, "inspect", f.name, "--format", "{{.State.StartedAt}}")
		mustDocker(t, "restart", "--time", "1", f.name)
		f.url = "http://" + mustDocker(t, "port", f.name, "44772/tcp")
		await(t, 10*time.Second, func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			code, _, err := request(ctx, "GET", f.url+"/ping", nil, "")
			return err == nil && code == 200
		})
		if newStart := mustDocker(
			t,
			"inspect",
			f.name,
			"--format",
			"{{.State.StartedAt}}",
		); oldStart == newStart {
			t.Fatal("container did not restart")
		}
		if receipt := f.read(t, "receipt"); receipt != "once" {
			t.Fatalf("workspace receipt lost: %s", receipt)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		status, body, err := request(ctx, "GET", f.url+"/command/status/"+id, nil, "")
		outputStatus, output, outputErr := request(ctx, "GET", f.url+"/command/"+id+"/logs", nil, "")
		t.Logf(
			"before=%s; after status=%d body=%s err=%v; logs=%d body=%s err=%v; workspace survived",
			before,
			status,
			body,
			err,
			outputStatus,
			output,
			outputErr,
		)
		if err != nil || status != 200 || !bytes.Equal(body, before) || outputErr != nil ||
			outputStatus != 200 ||
			!bytes.Equal(output, logs) {
			t.Error("completed execution receipt/output did not survive restart")
		}
	})
	t.Run("lost_response_dedup", func(t *testing.T) {
		f := c.create(t, 90)
		input := commandInput(
			"import time; f=open('/workspace/count','a'); f.write('once\\n'); f.close(); time.sleep(2); print('done')",
			5000,
			false,
		)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, _, err := request(ctx, "POST", f.url+"/command", input, "stable-operation"); done <- err }()
		await(t, 3*time.Second, func() bool { return f.size("count") > 0 })
		cancel() // Acceptance is observed on disk; deliberately lose the response/ID.
		select {
		case err := <-done:
			t.Logf("first response lost: %v", err)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("response-loss injection did not occur: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled HTTP request did not return")
		}
		ctx2, cancel2 := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel2()
		lookup, body, err := request(ctx2, "GET", f.url+"/command/status/stable-operation", nil, "")
		t.Logf("lookup by caller identity: status=%d body=%s err=%v", lookup, body, err)
		// Intentional duplicate only in this disposable probe: test whether the
		// conventional header is honored. Production must not retry Unknown.
		code, response, err := request(ctx2, "POST", f.url+"/command", input, "stable-operation")
		if err != nil || code != 200 {
			t.Fatalf("retry response: %d %s %v", code, response, err)
		}
		count := strings.Count(f.read(t, "count"), "once")
		t.Logf("same request/key after lost response: side_effect_count=%d", count)
		if count != 1 {
			t.Error(
				"repeating execution with same Idempotency-Key duplicated side effect (not a supported upstream contract)",
			)
		}
	})
}

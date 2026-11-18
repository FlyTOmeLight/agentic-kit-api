// These probes run INSIDE a disposable AIO container. Local process/file checks
// independently verify the HTTP result; never point them at a shared workspace.
package probes

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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type bashResult struct {
	SessionID string          `json:"session_id"`
	CommandID string          `json:"command_id"`
	Status    string          `json:"status"`
	Command   json.RawMessage `json:"command"`
	Stdout    string          `json:"stdout"`
	Content   string          `json:"content"`
}

func endpoint(t *testing.T) string {
	t.Helper()
	if os.Getenv("AIO_DISPOSABLE_PROBE") != "1" {
		t.Skip("requires AIO_DISPOSABLE_PROBE=1 inside a disposable AIO container")
	}
	return "http://127.0.0.1:8080"
}

func post(t *testing.T, path string, input any) bashResult {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Post(endpoint(t)+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Success bool       `json:"success"`
		Data    bashResult `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || response.StatusCode != 200 || !envelope.Success {
		t.Fatalf("%s: status=%d body=%s decode=%v", path, response.StatusCode, body, err)
	}
	return envelope.Data
}

func until(t *testing.T, duration time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not observed before deadline")
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// Linux /proc stat: process names can contain spaces and parentheses.
func processIdentity(pid int) (string, string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", ""
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return "", ""
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", ""
	}
	return fields[19], fields[0]
}

func recordedProcesses(dir string, visit func(int, string)) {
	data, _ := os.ReadFile(dir + "/pids")
	for _, field := range strings.Fields(string(data)) {
		parts := strings.SplitN(field, ":", 2)
		if len(parts) != 2 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err == nil && pid > 1 {
			visit(pid, parts[1])
		}
	}
}

func stopped(t *testing.T, dir string) {
	t.Helper()
	count := 0
	recordedProcesses(dir, func(pid int, original string) {
		count++
		identity, status := processIdentity(pid)
		if identity == original && status != "Z" && status != "X" {
			t.Errorf("original process still alive: pid=%d status=%s", pid, status)
		}
	})
	if count != 2 {
		t.Errorf("expected identities for parent and child, got %d", count)
	}
}

// The child writes a heartbeat independently of its parent and stdout pipe.
// start_new_session exercises descendants outside the parent's process group.
func writer(t *testing.T, detached bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// testing's parent temp directory must also be reachable by the API user.
	if err := os.Chmod(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	flag := "False"
	if detached {
		flag = "True"
	}
	source := fmt.Sprintf(`import os, subprocess, sys, time
child = subprocess.Popen([sys.executable, "-c", "import time\nf=open('ticks','a',buffering=1)\nwhile True:\n f.write('tick\\n'); time.sleep(0.1)"], start_new_session=%s, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
def identity(pid):
    stat = open('/proc/'+str(pid)+'/stat').read().rsplit(')',1)[1].split()
    return str(pid)+':'+stat[19]
with open('pids','w') as f: f.write(identity(os.getpid())+' '+identity(child.pid))
print('accepted', flush=True)
while True: time.sleep(1)
`, flag)
	post(t, "/v1/file/write", map[string]any{"file": dir + "/writer.py", "content": source})
	t.Cleanup(func() {
		recordedProcesses(dir, func(pid int, original string) {
			if identity, _ := processIdentity(pid); identity == original {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		})
	})
	return dir, "python3 " + quote(dir+"/writer.py")
}

func output(t *testing.T, command bashResult) bashResult {
	t.Helper()
	page := post(
		t,
		"/v1/bash/output",
		map[string]any{"session_id": command.SessionID, "command_id": command.CommandID},
	)
	var info bashResult
	if err := json.Unmarshal(page.Command, &info); err != nil || info.CommandID == "" {
		t.Fatalf("missing command status: %s (%v)", page.Command, err)
	}
	info.Stdout = page.Stdout
	return info
}

func TestAIOStopsWriters(t *testing.T) {
	endpoint(t)
	for _, mode := range []string{"kill", "hard_timeout"} {
		for _, detached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/detached=%t", mode, detached), func(t *testing.T) {
				dir, command := writer(t, detached)
				timeout := 20
				if mode == "hard_timeout" {
					timeout = 3
				}
				start := post(
					t,
					"/v1/bash/exec",
					map[string]any{
						"command":      command,
						"exec_dir":     dir,
						"async_mode":   true,
						"hard_timeout": timeout,
					},
				)
				until(t, 2*time.Second, func() bool { return fileSize(dir+"/ticks") > 0 })
				if mode == "kill" {
					post(
						t,
						"/v1/bash/kill",
						map[string]any{"session_id": start.SessionID, "signal": "SIGKILL"},
					)
				} else {
					// No HTTP requests during this interval: the client does not
					// trigger cancellation or send renewal/keepalive traffic.
					time.Sleep(4 * time.Second)
				}
				var state bashResult
				until(t, 3*time.Second, func() bool {
					state = output(t, start)
					return state.Status != "running" && state.Status != "pending"
				})
				before := fileSize(dir + "/ticks")
				// Observe longer than ten heartbeat intervals after terminal status.
				time.Sleep(1200 * time.Millisecond)
				after := fileSize(dir + "/ticks")
				stopped(t, dir)
				content := post(t, "/v1/file/read", map[string]any{"file": dir + "/ticks"})
				t.Logf(
					"status=%s heartbeat_bytes=%d->%d retained_read_bytes=%d",
					state.Status,
					before,
					after,
					len(content.Content),
				)
				if before == 0 || before != after || content.Content == "" {
					t.Errorf(
						"terminal command did not establish a stopped writer with retained files",
					)
				}
				if mode == "hard_timeout" {
					post(
						t,
						"/v1/file/write",
						map[string]any{
							"file":    dir + "/after-timeout",
							"content": "still writable",
						},
					)
					next := post(
						t,
						"/v1/bash/exec",
						map[string]any{
							"session_id":   start.SessionID,
							"command":      "printf still-executable",
							"exec_dir":     dir,
							"hard_timeout": 2,
						},
					)
					t.Logf(
						"after timeout: file write accepted; new command status=%s stdout=%q (no lease-wide admission barrier)",
						next.Status,
						next.Stdout,
					)
				}
			})
		}
	}
}

func TestAIOClientDisconnect(t *testing.T) {
	base := endpoint(t)
	dir, command := writer(t, false)
	data, _ := json.Marshal(
		map[string]any{
			"command":      command,
			"exec_dir":     dir,
			"async_mode":   false,
			"timeout":      15,
			"hard_timeout": 5,
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/bash/exec", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	until(t, 3*time.Second, func() bool { return fileSize(dir+"/ticks") > 0 })
	cancel() // Server acceptance was observed; discard the response intentionally.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("request completed before cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not disconnect")
	}
	before := fileSize(dir + "/ticks")
	until(t, 2*time.Second, func() bool { return fileSize(dir+"/ticks") > before })
	t.Log("writer continued after HTTP client disconnected; response/command ID was lost")
	// Bounded local observation of hard_timeout without issuing a remote kill.
	time.Sleep(5500 * time.Millisecond)
	before = fileSize(dir + "/ticks")
	time.Sleep(1200 * time.Millisecond)
	if after := fileSize(dir + "/ticks"); before == 0 || before != after {
		t.Errorf("writer continued after hard_timeout: %d->%d", before, after)
	}
	stopped(t, dir)
}

// These two tests are invoked separately with a python-server restart between
// them. The state file is a probe receipt, not a proposed production ledger.
const restartReceipt = "/tmp/agentic-aio-probe-restart.json"

type restartEvidence struct {
	Command bashResult `json:"command"`
	Daemon  string     `json:"daemon"`
}

func daemonIdentity(t *testing.T) string {
	t.Helper()
	data, err := exec.Command("supervisorctl", "pid", "python-server").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid daemon PID: %s", data)
	}
	identity, _ := processIdentity(pid)
	if identity == "" {
		t.Fatal("daemon identity unavailable")
	}
	return fmt.Sprintf("%d:%s", pid, identity)
}

func TestAIOSeedRestart(t *testing.T) {
	endpoint(t)
	if os.Getenv("AIO_RESTART_PHASE") != "seed" {
		t.Skip("requires explicit seed phase before external daemon restart")
	}
	r := post(t, "/v1/bash/exec", map[string]any{"command": "printf restart-evidence", "hard_timeout": 2})
	if r.Status != "completed" || r.Stdout != "restart-evidence" {
		t.Fatalf("unexpected result: %+v", r)
	}
	data, err := json.Marshal(restartEvidence{Command: r, Daemon: daemonIdentity(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restartReceipt, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("stored session=%s command=%s before runtime restart", r.SessionID, r.CommandID)
}

func TestAIOQueryAfterRestart(t *testing.T) {
	endpoint(t)
	if os.Getenv("AIO_RESTART_PHASE") != "query" {
		t.Skip("requires explicit query phase after external daemon restart")
	}
	data, err := os.ReadFile(restartReceipt)
	if err != nil {
		t.Fatal(err)
	}
	var receipt restartEvidence
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	current := daemonIdentity(t)
	if receipt.Daemon == "" || current == receipt.Daemon {
		t.Fatal("daemon restart not established")
	}
	t.Logf("daemon identity changed: %s -> %s", receipt.Daemon, current)
	prior := receipt.Command
	retained := post(t, "/v1/file/read", map[string]any{"file": restartReceipt})
	if retained.Content != string(data) {
		t.Fatal("restart evidence file changed")
	}
	t.Log("file retained across runtime restart")
	r := output(t, prior)
	if r.CommandID != prior.CommandID || r.Status != "completed" || r.Stdout != "restart-evidence" {
		t.Fatalf("original command result not recoverable: %+v", r)
	}
}

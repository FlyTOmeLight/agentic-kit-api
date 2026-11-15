package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const baseImage = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(data)), err
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := docker(args...)
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return out
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not observed before deadline")
}

type fixture struct{ name, state, work, image string }

func newFixture(t *testing.T, image string) fixture {
	t.Helper()
	name := fmt.Sprintf("supervisor-probe-%d", time.Now().UnixNano())
	f := fixture{name: name, state: name + "-state", work: name + "-work", image: image}
	// Register before creation so partial setup is cleaned too. Fail cleanup visibly.
	createdContainer := false
	createdState := false
	createdWork := false
	t.Cleanup(func() {
		if createdContainer {
			if out, err := docker("rm", "-f", name); err != nil {
				t.Errorf("container cleanup: %s %v", out, err)
			}
		}
		for volume, created := range map[string]bool{f.state: createdState, f.work: createdWork} {
			if created {
				if out, err := docker("volume", "rm", volume); err != nil {
					t.Errorf("volume cleanup: %s %v", out, err)
				}
			}
		}
	})
	mustDocker(t, "volume", "create", f.state)
	createdState = true
	mustDocker(t, "volume", "create", f.work)
	createdWork = true
	mustDocker(
		t,
		"run",
		"--rm",
		"--network",
		"none",
		"-v",
		f.state+":/state",
		"-v",
		f.work+":/workspace",
		baseImage,
		"sh",
		"-c",
		"chmod 700 /state; chown 65534:65534 /workspace",
	)
	mustDocker(
		t,
		"create",
		"--name",
		name,
		"--network",
		"none",
		"--read-only",
		"--cap-drop",
		"ALL",
		"--cap-add",
		"SETUID",
		"--cap-add",
		"SETGID",
		"--security-opt",
		"no-new-privileges",
		"--cpus",
		"1",
		"--memory",
		"128m",
		"--pids-limit",
		"64",
		"--tmpfs",
		"/tmp:rw,nosuid,nodev,noexec,size=8m",
		"-v",
		f.state+":/state",
		"-v",
		f.work+":/workspace",
		image,
		"serve",
	)
	createdContainer = true
	mustDocker(t, "start", name)
	f.ready(t)
	return f
}

func (f fixture) ready(t *testing.T) {
	t.Helper()
	await(t, func() bool {
		out, _ := docker("exec", f.name, "/probe", "call", "/query", `{"ID":"readiness"}`)
		return strings.HasPrefix(out, "404 ")
	})
}

func (f fixture) call(t *testing.T, path string, input any, code int) string {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	out, callErr := docker("exec", f.name, "/probe", "call", path, string(data))
	if !strings.HasPrefix(out, fmt.Sprintf("%d ", code)) || (code < 400 && callErr != nil) {
		t.Fatalf("%s want %d, got %s (%v)", path, code, out, callErr)
	}
	return strings.TrimPrefix(out, fmt.Sprintf("%d ", code))
}

type observation struct {
	Receipt struct {
		ID, State string
		ExitCode  int
	}
	Output string
}

func (f fixture) query(t *testing.T, id string) observation {
	t.Helper()
	var r observation
	if err := json.Unmarshal([]byte(f.call(t, "/query", map[string]any{"ID": id}, 200)), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f fixture) finished(t *testing.T, id string) observation {
	t.Helper()
	var r observation
	await(t, func() bool { r = f.query(t, id); return r.Receipt.State == "Exited" })
	if r.Receipt.ExitCode != 0 {
		t.Fatalf("command failed: %+v", r)
	}
	return r
}

func (f fixture) readVolume(t *testing.T, file string) string {
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
		f.work+":/workspace:ro",
		baseImage,
		"cat",
		"/workspace/"+file,
	)
}

func TestSupervisorPrototype(t *testing.T) {
	binary := os.Getenv("SUPERVISOR_PROBE_BINARY")
	if binary == "" {
		t.Skip("requires explicit Linux arm64 binary and disposable Docker access")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	if err = os.WriteFile(filepath.Join(build, "probe"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(build, "Dockerfile"),
		[]byte("FROM "+baseImage+"\nCOPY probe /probe\nENTRYPOINT [\"/probe\"]\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	image := fmt.Sprintf("supervisor-probe:%d", time.Now().UnixNano())
	mustDocker(t, "build", "--network", "none", "--pull=false", "-q", "-t", image, build)
	t.Cleanup(func() {
		if out, err := docker("image", "rm", image); err != nil {
			t.Errorf("image cleanup: %s %v", out, err)
		}
	})
	t.Log(mustDocker(t, "image", "inspect", image, "--format", "{{.Id}}"))
	t.Run("expiry_protection_dedup_and_recovery", func(t *testing.T) {
		f := newFixture(t, image)
		f.call(t, "/bind", map[string]any{"Seconds": 15}, 200)
		identity := mustDocker(t, "exec", f.name, "sh", "-c", "tr '\\0' ' ' < /proc/1/cmdline")
		if !strings.Contains(identity, "/probe serve") {
			t.Fatal(identity)
		}
		// Explicitly validate credentials, capability clearing, no-new-privileges,
		// inability to stop PID 1, and the state/socket boundary as the user.
		protection := `set -eu
test "$(id -u)" = 65534
test "$(id -g)" = 65534
test "$(id -G)" = 65534
grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status
grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status
if kill -STOP 1 2>/dev/null; then exit 11; fi
if cat /state/lease.json 2>/dev/null; then exit 12; fi
if touch /state/forged 2>/dev/null; then exit 13; fi
if /probe call /query '{"ID":"protection"}' >/tmp/socket-denial 2>&1; then exit 14; fi
grep -q 'permission denied' /tmp/socket-denial
echo protected`
		f.call(t, "/start", map[string]any{"ID": "protection", "Script": protection}, 200)
		if r := f.finished(t, "protection"); !strings.Contains(r.Output, "protected") {
			t.Fatal(r)
		}
		request := map[string]any{
			"ID":     "dedup",
			"Script": "echo once >> /workspace/count; printf durable-result",
		}
		// Deliberately ignore the first acceptance body, then recover by stable ID.
		f.call(t, "/start", request, 200)
		first := f.finished(t, "dedup")
		f.call(t, "/start", request, 200)
		f.call(t, "/start", map[string]any{"ID": "dedup", "Script": "echo twice >> /workspace/count"}, 409)
		if first.Output != "durable-result" {
			t.Fatal(first)
		}
		// Session leader and intermediate parent exit; the grandchild keeps writing.
		detached := `setsid sh -c 'sh -c "while :; do echo tick >> /workspace/ticks; sleep 0.1; done" </dev/null >/dev/null 2>&1 & exit 0' & wait; echo detached-started`
		f.call(t, "/start", map[string]any{"ID": "detached", "Script": detached}, 200)
		f.finished(t, "detached") // Root-command exit must NOT be mistaken for stopped writers.
		await(t, func() bool {
			out, _ := docker("exec", f.name, "sh", "-c", "test -s /workspace/ticks && cat /workspace/ticks")
			return len(out) > 8
		})
		// No supervisor calls or renewals while waiting for the local deadline.
		if code := mustDocker(t, "wait", f.name); code != "0" {
			t.Fatalf("expiry exit=%s", code)
		}
		if running := mustDocker(t, "inspect", f.name, "--format", "{{.State.Running}}"); running != "false" {
			t.Fatal(running)
		}
		before := f.readVolume(t, "ticks")
		time.Sleep(1200 * time.Millisecond)
		after := f.readVolume(t, "ticks")
		if before == "" || before != after {
			t.Fatal("detached writer did not stop")
		}
		if count := f.readVolume(t, "count"); count != "once" {
			t.Fatalf("duplicate side effect: %q", count)
		}
		t.Logf(
			"expiry: container stopped, detached heartbeat stable (%d bytes), workspace retained",
			len(after),
		)
		mustDocker(t, "start", f.name)
		f.ready(t)
		if recovered := f.query(t, "dedup"); recovered != first {
			t.Fatalf("receipt changed: %+v -> %+v", first, recovered)
		}
		f.call(t, "/bind", map[string]any{"Seconds": 10}, 409)
		f.call(t, "/start", request, 409)
		f.call(t, "/start", map[string]any{"ID": "late", "Script": "touch /workspace/late"}, 409)
		t.Log("completed receipt/output survive restart; old lease cannot reopen; late execution rejected")
	})
	t.Run("supervisor_crash_keeps_unknown", func(t *testing.T) {
		f := newFixture(t, image)
		f.call(t, "/bind", map[string]any{"Seconds": 30}, 200)
		f.call(
			t,
			"/start",
			map[string]any{
				"ID":     "inflight",
				"Script": "echo once >> /workspace/count; while :; do echo tick >> /workspace/ticks; sleep 0.1; done",
			},
			200,
		)
		await(t, func() bool {
			out, _ := docker("exec", f.name, "sh", "-c", "test -s /workspace/ticks && cat /workspace/ticks")
			return len(out) > 8
		})
		mustDocker(t, "kill", "--signal", "KILL", f.name)
		if code := mustDocker(t, "wait", f.name); code != "137" {
			t.Fatal(code)
		}
		before := f.readVolume(t, "ticks")
		mustDocker(t, "start", f.name)
		f.ready(t)
		if r := f.query(t, "inflight"); r.Receipt.State != "Unknown" {
			t.Fatalf("inflight falsely resolved: %+v", r)
		}
		f.call(
			t,
			"/start",
			map[string]any{
				"ID":     "inflight",
				"Script": "echo once >> /workspace/count; while :; do echo tick >> /workspace/ticks; sleep 0.1; done",
			},
			409,
		)
		f.call(t, "/bind", map[string]any{"Seconds": 30}, 409)
		if count := f.readVolume(t, "count"); count != "once" {
			t.Fatal(count)
		}
		time.Sleep(1200 * time.Millisecond)
		if after := f.readVolume(t, "ticks"); before == "" || after != before {
			t.Fatal("writer survived supervisor crash")
		}
		t.Log(
			"SIGKILL stops user processes; uncommitted result becomes Unknown, no replay, pre-expiry restart stays closed",
		)
	})
}

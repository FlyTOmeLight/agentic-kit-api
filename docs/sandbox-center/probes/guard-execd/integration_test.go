package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const integrationBaseImage = "opensandbox/server@sha256:bcd5f264101262cebfa7e1ff9cd0424403e42f31a6596e4364cf90d8cb5c936b"

func docker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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

func await(t *testing.T, description string, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s not observed within %s", description, timeout)
}

type fixture struct{ name, state, work, image string }

// Helpers have explicit names too: even an interrupted docker run is covered by
// cleanup, and cleanup never scans or removes another invocation's resources.
func helper(t *testing.T, image string, options []string, command ...string) string {
	t.Helper()
	name := fmt.Sprintf("agentic-guard-execd-helper-%d", time.Now().UnixNano())
	created := false
	t.Cleanup(func() {
		if created {
			if out, err := docker("rm", "-f", name); err != nil {
				t.Errorf("helper cleanup %s: %v %s", name, err, out)
			}
		}
	})
	args := []string{
		"create",
		"--name",
		name,
		"--network",
		"none",
		"--read-only",
		"--security-opt",
		"no-new-privileges",
		"--entrypoint",
		"/bin/sh",
	}
	args = append(args, options...)
	args = append(args, image)
	args = append(args, command...)
	mustDocker(t, args...)
	created = true
	return mustDocker(t, "start", "-a", name)
}

func newFixture(t *testing.T, image string, seconds int) fixture {
	t.Helper()
	f := newFixtureRaw(t, image, seconds)
	f.admitDefault(t)
	return f
}

func newFixtureRaw(t *testing.T, image string, seconds int, env ...string) fixture {
	t.Helper()
	name := fmt.Sprintf("agentic-guard-execd-%d", time.Now().UnixNano())
	f := fixture{name: name, state: name + "-state", work: name + "-work", image: image}
	containerCreated, stateCreated, workCreated := false, false, false
	t.Cleanup(func() {
		if containerCreated {
			if out, err := docker("rm", "-f", name); err != nil {
				t.Errorf("container cleanup %s: %v %s", name, err, out)
			}
		}
		for volume, created := range map[string]bool{f.state: stateCreated, f.work: workCreated} {
			if created {
				if out, err := docker("volume", "rm", volume); err != nil {
					t.Errorf("volume cleanup %s: %v %s", volume, err, out)
				}
			}
		}
	})
	mustDocker(t, "volume", "create", f.state)
	stateCreated = true
	mustDocker(t, "volume", "create", f.work)
	workCreated = true
	helper(
		t,
		image,
		[]string{
			"--cap-drop",
			"ALL",
			"--cap-add",
			"CHOWN",
			"-v",
			f.state + ":/state",
			"-v",
			f.work + ":/workspace",
		},
		"-ec",
		"chmod 700 /state; chown 65534:65534 /workspace",
	)
	args := []string{
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
		"256m",
		"--pids-limit",
		"128",
		"--tmpfs",
		"/tmp:rw,nosuid,nodev,noexec,size=16m",
	}
	for _, item := range env {
		args = append(args, "-e", item)
	}
	args = append(args, "-v", f.state+":/state", "-v", f.work+":/workspace", image, "serve", strconv.Itoa(seconds))
	mustDocker(t, args...)
	containerCreated = true
	mustDocker(t, "start", name)
	f.ready(t)
	return f
}

func (f fixture) ready(t *testing.T) {
	t.Helper()
	await(t, "guard readiness", 8*time.Second, func() bool {
		out, err := docker("exec", f.name, "/guard", "call", "/health", `{}`)
		return err == nil && strings.HasPrefix(out, "200 ")
	})
}

func (f fixture) call(t *testing.T, path string, body any, want int) string {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	out, err := docker("exec", f.name, "/guard", "call", path, string(data))
	prefix := fmt.Sprintf("%d ", want)
	if (out != strconv.Itoa(want) && !strings.HasPrefix(out, prefix)) || (want < 400 && err != nil) ||
		(want >= 400 && err == nil) {
		t.Fatalf("%s want HTTP %d, got %q (%v)", path, want, out, err)
	}
	if out == strconv.Itoa(want) {
		return ""
	}
	return strings.TrimPrefix(out, prefix)
}

func testBinding() map[string]any {
	return map[string]any{
		"sandboxId": "sandbox-1", "runId": "run-1", "workerId": "worker-1",
		"epoch": 1, "leaseId": "lease-1", "fencingToken": 7,
		"admissionEpoch": 1, "generation": 1, "runtimeBootId": "boot-1",
	}
}

func (f fixture) admit(t *testing.T, revision int64, deadline time.Time, requestKey string, want int) string {
	t.Helper()
	body := testBinding()
	body["revision"] = revision
	body["deadline"] = deadline
	body["requestKey"] = requestKey
	return f.call(t, "/admission", body, want)
}

func (f fixture) admitDefault(t *testing.T) {
	t.Helper()
	var health struct {
		HardDeadline time.Time `json:"hardDeadline"`
	}
	if err := json.Unmarshal(
		[]byte(f.call(t, "/health", map[string]any{}, 200)),
		&health,
	); err != nil ||
		health.HardDeadline.IsZero() {
		t.Fatalf("invalid hard deadline: %+v (%v)", health, err)
	}
	deadline := health.HardDeadline.Add(-time.Second)
	if !deadline.After(time.Now()) {
		t.Fatalf("fixture lifetime too short for default admission: %s", health.HardDeadline)
	}
	f.admit(t, 1, deadline, "boot-admission", 200)
}

func (f fixture) run(t *testing.T, script, marker string) {
	t.Helper()
	operationID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	out := f.call(t, "/command", map[string]string{"operationId": operationID, "script": script}, 200)
	if !strings.Contains(out, marker) {
		t.Fatalf("command did not reach success marker %q: %s", marker, out)
	}
}

func (f fixture) readVolume(t *testing.T, file string) string {
	t.Helper()
	return helper(
		t,
		f.image,
		[]string{"--cap-drop", "ALL", "-v", f.work + ":/workspace:ro"},
		"-ec",
		"cat /workspace/"+file,
	)
}

type operationView struct {
	OperationID string `json:"operationId"`
	State       string `json:"state"`
	HTTPStatus  int    `json:"httpStatus"`
	Output      string `json:"output"`
	Truncated   bool   `json:"truncated"`
	Error       string `json:"error"`
}

func (f fixture) operation(t *testing.T, operationID string) operationView {
	t.Helper()
	out := f.call(t, "/operation", map[string]string{"operationId": operationID}, 200)
	var record operationView
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("invalid operation response: %s (%v)", out, err)
	}
	return record
}

const detachedWriter = `python3 - <<'PY'
import os,time
if os.fork() == 0:
    os.setsid()
    fd=os.open('/dev/null',os.O_RDWR)
    for n in (0,1,2): os.dup2(fd,n)
    if fd > 2: os.close(fd)
    while True:
        with open('/workspace/ticks','a') as f: f.write('tick\n'); f.flush()
        time.sleep(0.1)
else:
    print('DETACHED_WRITER_STARTED')
PY`

func (f fixture) writer(t *testing.T) {
	t.Helper()
	f.run(t, detachedWriter, "DETACHED_WRITER_STARTED")
	await(t, "detached heartbeat", 5*time.Second, func() bool {
		out, err := docker(
			"exec",
			f.name,
			"python3",
			"-c",
			"from pathlib import Path; print(Path('/workspace/ticks').stat().st_size)",
		)
		n, parseErr := strconv.Atoi(out)
		return err == nil && parseErr == nil && n >= 15
	})
}

func (f fixture) stopped(t *testing.T) {
	t.Helper()
	await(t, "container termination", 8*time.Second, func() bool {
		out, err := docker("inspect", f.name, "--format", "{{.State.Running}}")
		return err == nil && out == "false"
	})
	before := f.readVolume(t, "ticks")
	// This fixed observation window detects surviving 100 ms writers after the
	// stop condition; it is not a startup/readiness sleep or a realtime SLA.
	time.Sleep(1200 * time.Millisecond)
	after := f.readVolume(t, "ticks")
	if before == "" || before != after {
		t.Fatalf("writer survived termination: before=%d after=%d bytes", len(before), len(after))
	}
	t.Logf("container stopped; detached heartbeat stable at %d bytes; independent workspace retained", len(after))
}

func TestGuardExecdIntegration(t *testing.T) {
	guard, execd := os.Getenv("GUARD_EXECD_BINARY"), os.Getenv("PATCHED_EXECD_BINARY")
	if guard == "" || execd == "" {
		t.Skip(
			"requires GUARD_EXECD_BINARY and PATCHED_EXECD_BINARY Linux arm64 binaries and disposable Docker access",
		)
	}
	build := t.TempDir()
	for name, source := range map[string]string{"guard": guard, "execd": execd} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(build, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dockerfile := "FROM " + integrationBaseImage + "\nCOPY guard /guard\nCOPY execd /execd\nENTRYPOINT [\"/guard\"]\n"
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	image := fmt.Sprintf("agentic-guard-execd:%d", time.Now().UnixNano())
	imageCreated := false
	t.Cleanup(func() {
		if imageCreated {
			if out, err := docker("image", "rm", image); err != nil {
				t.Errorf("image cleanup: %v %s", err, out)
			}
		}
	})
	mustDocker(t, "build", "--network", "none", "--pull=false", "-q", "-t", image, build)
	imageCreated = true
	t.Logf("image=%s", mustDocker(t, "image", "inspect", image, "--format", "{{.Id}}"))

	t.Run("protected_control_and_identity", func(t *testing.T) {
		f := newFixture(t, image, 30)
		cmdline := mustDocker(
			t,
			"exec",
			f.name,
			"python3",
			"-c",
			"print(open('/proc/1/cmdline','rb').read().replace(b'\\0',b' ').decode())",
		)
		if !strings.HasPrefix(cmdline, "/guard serve ") {
			t.Fatalf("guard is not namespace PID 1: %q", cmdline)
		}
		mustDocker(
			t,
			"exec",
			f.name,
			"python3",
			"-c",
			"open('/state/trusted-receipt','w').write('trusted-test-evidence')",
		)
		protection := `python3 - <<'PY'
import os,socket,errno,signal
assert os.getuid()==65534 and os.getgid()==65534
assert set(os.getgroups()) <= {65534}, os.getgroups()
status=dict(line.split(':',1) for line in open('/proc/self/status') if ':' in line)
assert int(status['CapEff'].strip(),16)==0
assert status['NoNewPrivs'].strip()=='1'
try: os.kill(1,signal.SIGSTOP)
except PermissionError: pass
else: raise AssertionError('user can stop guard')
for path in ('/state/guard.sock','/state/execd.sock'):
    with socket.socket(socket.AF_UNIX) as s:
        try: s.connect(path)
        except OSError as e: assert e.errno==errno.EACCES,(path,e)
        else: raise AssertionError('user can access '+path)
try: open('/state/forged','w').close()
except PermissionError: pass
else: raise AssertionError('user can modify private state')
try: open('/state/trusted-receipt').read()
except PermissionError: pass
else: raise AssertionError('user can read private receipts')
print('CONTROL_AND_IDENTITY_PROTECTED')
PY`
		f.run(t, protection, "CONTROL_AND_IDENTITY_PROTECTED")
		mustDocker(
			t,
			"exec",
			f.name,
			"python3",
			"-c",
			"import socket,errno; s=socket.socket(); s.settimeout(1); result=s.connect_ex(('127.0.0.1',44772)); assert result==errno.ECONNREFUSED,result",
		)
		f.call(t, "/internal/init", map[string]any{"accessToken": "attacker"}, 404)
		for _, field := range []string{"uid", "gid", "body", "cwd", "env", "argv"} {
			f.call(
				t,
				"/command",
				map[string]any{
					"operationId": "protect-" + field,
					"script":      "touch /workspace/injected",
					field:         0,
				},
				400,
			)
		}
		mustDocker(t, "exec", f.name, "test", "!", "-e", "/workspace/injected")
		f.call(
			t,
			"/file-write",
			map[string]string{"operationId": "file-protected", "content": "identity-scoped-file"},
			200,
		)
		f.run(t, `python3 - <<'PY'
from pathlib import Path
p=Path('/workspace/guard-file')
assert p.read_text()=='identity-scoped-file'
assert p.stat().st_uid==65534 and p.stat().st_gid==65534
p.unlink()
p.symlink_to('/state/boot-started')
print('FILE_IDENTITY_AND_SYMLINK_READY')
PY`, "FILE_IDENTITY_AND_SYMLINK_READY")
		markerBefore := mustDocker(
			t,
			"exec",
			f.name,
			"python3",
			"-c",
			"print(open('/state/boot-started','rb').read().hex())",
		)
		out, err := docker(
			"exec",
			f.name,
			"/guard",
			"call",
			"/file-write",
			`{"operationId":"file-forged-boot","content":"forged-boot"}`,
		)
		if err == nil || (!strings.HasPrefix(out, "403 ") && !strings.HasPrefix(out, "500 ")) {
			t.Fatalf("identity-scoped upload must reject private-state symlink: %q (%v)", out, err)
		}
		markerAfter := mustDocker(
			t,
			"exec",
			f.name,
			"python3",
			"-c",
			"print(open('/state/boot-started','rb').read().hex())",
		)
		if markerAfter != markerBefore {
			t.Fatal("file API modified private boot marker")
		}
	})

	t.Run("expiry_stops_detached_writer_restart_stays_closed", func(t *testing.T) {
		f := newFixture(t, image, 20)
		f.writer(t)
		var lease struct {
			Deadline time.Time `json:"deadline"`
		}
		if err := json.Unmarshal(
			[]byte(f.call(t, "/health", map[string]any{}, 200)),
			&lease,
		); err != nil ||
			lease.Deadline.IsZero() {
			t.Fatalf("invalid guard deadline: %+v (%v)", lease, err)
		}
		started := time.Now()
		if code := mustDocker(t, "wait", f.name); code != "0" {
			t.Fatalf("expiry must exit cleanly: %s", code)
		}
		if time.Now().Before(lease.Deadline.Add(-time.Second)) {
			t.Fatalf("container exited before its deadline: %s", lease.Deadline)
		}
		t.Logf(
			"deadline termination observed after %s from writer readiness; deadline observation delay=%s",
			time.Since(started),
			time.Since(lease.Deadline),
		)
		f.stopped(t)
		mustDocker(t, "start", f.name)
		f.ready(t)
		var health struct {
			Closed bool `json:"closed"`
		}
		if err := json.Unmarshal(
			[]byte(f.call(t, "/health", map[string]any{}, 200)),
			&health,
		); err != nil ||
			!health.Closed {
			t.Fatalf("restart must remain closed: %+v (%v)", health, err)
		}
		f.call(
			t,
			"/command",
			map[string]string{"operationId": "reopened", "script": "touch /workspace/reopened"},
			409,
		)
		mustDocker(t, "exec", f.name, "test", "!", "-e", "/workspace/reopened")
	})

	t.Run("guard_crash_stops_detached_writer", func(t *testing.T) {
		f := newFixture(t, image, 30)
		f.writer(t)
		mustDocker(t, "kill", "--signal", "KILL", f.name)
		f.stopped(t)
	})

	t.Run("execd_crash_stops_detached_writer", func(t *testing.T) {
		f := newFixture(t, image, 30)
		f.writer(t)
		var health struct {
			ExecdPID int `json:"execdPID"`
		}
		if err := json.Unmarshal(
			[]byte(f.call(t, "/health", map[string]any{}, 200)),
			&health,
		); err != nil ||
			health.ExecdPID <= 1 {
			t.Fatalf("invalid execd identity: %+v (%v)", health, err)
		}
		// The guard may terminate this docker-exec client before it can return
		// after the successful kill. Its exit status alone is not the signal's
		// outcome; require the guard's explicit execd-failure exit code instead.
		out, err := docker(
			"exec",
			f.name,
			"python3",
			"-c",
			fmt.Sprintf("import os,signal; os.kill(%d,signal.SIGKILL)", health.ExecdPID),
		)
		var exitErr *exec.ExitError
		if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 137) {
			t.Fatalf("unexpected execd kill failure: %v %s", err, out)
		}
		t.Logf("execd kill client: output=%q error=%v", out, err)
		f.stopped(t)
		if code := mustDocker(t, "inspect", f.name, "--format", "{{.State.ExitCode}}"); code != "1" {
			t.Fatalf("expected guard to terminate on execd failure, exit=%s", code)
		}
		if logs := mustDocker(
			t,
			"logs",
			f.name,
		); !strings.Contains(
			logs,
			"execd exited (signal: killed); closing the entire instance",
		) {
			t.Fatalf("missing evidence of execd SIGKILL: %s", logs)
		}
	})

	t.Run("admission_renews_and_reads_back", func(t *testing.T) {
		f := newFixtureRaw(t, image, 30)
		var health struct {
			HardDeadline time.Time `json:"hardDeadline"`
		}
		if err := json.Unmarshal(
			[]byte(f.call(t, "/health", map[string]any{}, 200)),
			&health,
		); err != nil ||
			health.HardDeadline.IsZero() {
			t.Fatalf("invalid health: %+v (%v)", health, err)
		}
		f.call(
			t,
			"/command",
			map[string]string{
				"operationId": "before-admission",
				"script":      "touch /workspace/before-admission",
			},
			409,
		)
		mustDocker(t, "exec", f.name, "test", "!", "-e", "/workspace/before-admission")

		start := time.Now()
		first := start.Add(5 * time.Second)
		second := start.Add(12 * time.Second)
		f.admit(t, 1, first, "renew-1", 200)
		var read struct {
			Bound    bool      `json:"bound"`
			Revision int64     `json:"revision"`
			Deadline time.Time `json:"deadline"`
		}
		if err := json.Unmarshal(
			[]byte(f.call(t, "/admission/read", map[string]any{}, 200)),
			&read,
		); err != nil ||
			!read.Bound || read.Revision != 1 ||
			!read.Deadline.Equal(first) {
			t.Fatalf("first admission not read back: %+v (%v)", read, err)
		}
		f.admit(t, 2, second, "renew-2", 200)
		if err := json.Unmarshal(
			[]byte(f.call(t, "/admission/read", map[string]any{}, 200)),
			&read,
		); err != nil ||
			read.Revision != 2 ||
			!read.Deadline.Equal(second) {
			t.Fatalf("renewal not read back: %+v (%v)", read, err)
		}
		replay := f.admit(t, 2, second, "renew-2", 200)
		if !strings.Contains(replay, `"replayed":true`) {
			t.Fatalf("same-revision retry must replay the accepted fact: %s", replay)
		}
		if err := json.Unmarshal(
			[]byte(f.call(t, "/admission/read", map[string]any{}, 200)),
			&read,
		); err != nil ||
			!read.Deadline.Equal(second) {
			t.Fatalf("idempotent retry changed the accepted deadline: %+v (%v)", read, err)
		}
		f.admit(t, 2, second.Add(time.Second), "renew-2", 409)
		f.admit(t, 2, second, "renew-2-different-key", 409)
		f.admit(t, 1, second.Add(2*time.Second), "renew-old", 409)
		mismatch := testBinding()
		mismatch["leaseId"] = "lease-other"
		mismatch["revision"] = 3
		mismatch["deadline"] = second
		mismatch["requestKey"] = "renew-mismatch"
		f.call(t, "/admission", mismatch, 409)
		f.admit(t, 3, health.HardDeadline.Add(time.Second), "renew-too-long", 409)
		f.run(t, "echo AFTER_RENEWAL", "AFTER_RENEWAL")

		time.Sleep(time.Until(first) + 500*time.Millisecond)
		if running := mustDocker(t, "inspect", f.name, "--format", "{{.State.Running}}"); running != "true" {
			t.Fatalf("instance stopped at the superseded deadline: %s", first)
		}
		if code := mustDocker(t, "wait", f.name); code != "0" {
			t.Fatalf("renewed deadline must exit cleanly: %s", code)
		}
		if time.Now().Before(second.Add(-time.Second)) {
			t.Fatalf("instance exited before the renewed deadline: %s", second)
		}
	})

	t.Run("operation_receipts_are_idempotent", func(t *testing.T) {
		f := newFixture(t, image, 30)
		const operationID = "run:call-1"
		command := map[string]string{
			"operationId": operationID,
			"script":      "printf 'one\\n' >> /workspace/receipt-count; echo RECEIPT_OK",
		}
		if out := f.call(t, "/command", command, 200); !strings.Contains(out, "RECEIPT_OK") {
			t.Fatalf("command did not run: %s", out)
		}
		if out := f.call(t, "/command", command, 200); !strings.Contains(out, "RECEIPT_OK") {
			t.Fatalf("idempotent replay did not return the original result: %s", out)
		}
		if count := strings.Count(f.readVolume(t, "receipt-count"), "one"); count != 1 {
			t.Fatalf("idempotent replay dispatched the command again: %d lines", count)
		}
		f.call(t, "/command", map[string]string{"operationId": operationID, "script": "echo different"}, 409)
		record := f.operation(t, operationID)
		if record.State != "Succeeded" || record.HTTPStatus != 200 ||
			!strings.Contains(record.Output, "RECEIPT_OK") ||
			record.Error != "" {
			t.Fatalf("unexpected command receipt: %+v", record)
		}

		fileOperation := map[string]string{"operationId": "file:call-1", "content": "alpha"}
		f.call(t, "/file-write", fileOperation, 200)
		f.call(t, "/file-write", fileOperation, 200)
		f.call(t, "/file-write", map[string]string{"operationId": "file:call-1", "content": "beta"}, 409)
		if body := f.readVolume(t, "guard-file"); body != "alpha" {
			t.Fatalf("file operation replay changed the file: %q", body)
		}
		if record := f.operation(t, "file:call-1"); record.State != "Succeeded" {
			t.Fatalf("unexpected file receipt: %+v", record)
		}
		f.call(t, "/operation", map[string]string{"operationId": "missing-operation"}, 404)
	})

	t.Run("receipt_storage_failure_does_not_dispatch", func(t *testing.T) {
		f := newFixtureRaw(t, image, 20, "GUARD_PROBE_FAIL_RECEIPTS=1")
		f.admitDefault(t)
		f.call(t, "/command", map[string]string{
			"operationId": "receipt-failure",
			"script":      "touch /workspace/dispatched-without-receipt",
		}, 500)
		mustDocker(t, "exec", f.name, "test", "!", "-e", "/workspace/dispatched-without-receipt")
		f.call(t, "/operation", map[string]string{"operationId": "receipt-failure"}, 404)
	})

	t.Run("guard_crash_leaves_dispatch_unknown", func(t *testing.T) {
		f := newFixture(t, image, 30)
		const operationID = "run:crash-1"
		const script = "sleep 8; echo late > /workspace/late"
		call := exec.Command("docker", "exec", f.name, "/guard", "call", "/command",
			fmt.Sprintf(`{"operationId":%q,"script":%q}`, operationID, script))
		if err := call.Start(); err != nil {
			t.Fatal(err)
		}
		check := `import glob,json,sys
for path in glob.glob('/state/operations/*.json'):
    record = json.load(open(path))
    if record.get('operationId') == sys.argv[1]:
        print(record.get('state', ''))`
		await(t, "durable DispatchStarted", 10*time.Second, func() bool {
			out, err := docker("exec", f.name, "python3", "-c", check, operationID)
			return err == nil && strings.Contains(out, "DispatchStarted")
		})
		mustDocker(t, "kill", "--signal", "KILL", f.name)
		_ = call.Wait()
		if code := mustDocker(t, "wait", f.name); code != "137" {
			t.Fatalf("expected SIGKILL exit, got %s", code)
		}
		mustDocker(t, "start", f.name)
		f.ready(t)
		record := f.operation(t, operationID)
		if record.State != "Unknown" {
			t.Fatalf("crash after dispatch must remain Unknown: %+v", record)
		}
		f.call(t, "/command", map[string]string{"operationId": operationID, "script": script}, 409)
		mustDocker(t, "exec", f.name, "test", "!", "-e", "/workspace/late")
	})
}

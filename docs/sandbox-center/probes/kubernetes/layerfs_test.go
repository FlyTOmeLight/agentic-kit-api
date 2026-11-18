package kubernetesprobe

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const layerfsHostRoot = "/var/lib/rancher/k3s/storage/agentic-layerfs-probes"

func layerfsPod(name string, security map[string]any, volumes, mounts []any, command []string) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
			"name":        name,
			"annotations": map[string]string{"kubectl.kubernetes.io/default-container": "probe"},
		},
		"spec": map[string]any{
			"nodeSelector":                  map[string]string{"kubernetes.io/hostname": "orbstack"},
			"restartPolicy":                 "Never",
			"automountServiceAccountToken":  false,
			"terminationGracePeriodSeconds": 1,
			"volumes":                       volumes,
			"containers": []any{map[string]any{
				"name": "probe", "image": workloadImage, "imagePullPolicy": "Never",
				"command": command, "securityContext": security, "volumeMounts": mounts,
				"resources": map[string]any{
					"requests": map[string]string{"cpu": "25m", "memory": "32Mi"},
					"limits":   map[string]string{"cpu": "1", "memory": "128Mi"},
				},
			}},
		},
	}
}

func layerfsHostVolume(hostPath string) map[string]any {
	return map[string]any{"name": "layers", "hostPath": map[string]string{"path": hostPath, "type": "Directory"}}
}

func TestLayeredWorkspaceMounts(t *testing.T) {
	if os.Getenv("KUBERNETES_LAYERFS_PROBE") != "orbstack" {
		t.Skip("requires explicit local orbstack layerfs experiment")
	}
	probe := cluster{namespace: fmt.Sprintf("agentic-layerfs-%d", time.Now().UnixNano())}
	hostPath := layerfsHostRoot + "/" + probe.namespace
	probe.must(t, nil, "create", "namespace", probe.namespace)
	materializerReady := false
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := probe.kubectl(nil, "get", "pods", "-o", "wide")
			t.Logf("fixture pods: %s", out)
			out, _ = probe.kubectl(nil, "logs", "materializer", "--all-containers", "--tail=20")
			t.Logf("materializer logs: %s", out)
		}
		out, err := probe.kubectl(
			nil,
			"delete",
			"pod",
			"alpha",
			"beta",
			"--ignore-not-found",
			"--wait=true",
			"--timeout=20s",
		)
		if err != nil {
			t.Errorf("consumer cleanup failed; preserving materializer and mounts: %v %s", err, out)
			return
		}
		if materializerReady {
			out, err = probe.kubectl(nil, "exec", "materializer", "--", "sh", "-ec", `
for workspace in alpha beta; do
  target=/layers/exports/$workspace/workspace
  if mountpoint -q "$target"; then umount "$target"; fi
done
python3 -c 'import pathlib,shutil
root=pathlib.Path("/layers")
assert not any(line.split()[4].startswith("/layers/") for line in pathlib.Path("/proc/self/mountinfo").read_text().splitlines()), "nested mount remains"
for child in root.iterdir():
    shutil.rmtree(child) if child.is_dir() else child.unlink()
'
`)
			if err != nil {
				t.Errorf(
					"mount cleanup failed; preserving namespace %s and %s: %v %s",
					probe.namespace,
					hostPath,
					err,
					out,
				)
				return
			}
		}
		out, err = probe.kubectl(
			nil,
			"delete",
			"pod",
			"materializer",
			"--ignore-not-found",
			"--wait=true",
			"--timeout=20s",
		)
		if err != nil {
			t.Errorf("materializer cleanup: %v %s", err, out)
			return
		}
		cleanup := layerfsPod("cleanup", map[string]any{"runAsUser": 0},
			[]any{map[string]any{"name": "layers", "hostPath": map[string]string{
				"path": layerfsHostRoot, "type": "DirectoryOrCreate",
			}}},
			[]any{map[string]any{"name": "layers", "mountPath": "/cleanup"}},
			[]string{
				"python3",
				"-c",
				"import pathlib,sys; directory=pathlib.Path(sys.argv[1]); directory.rmdir() if directory.exists() else None",
				"/cleanup/" + probe.namespace,
			})
		manifest, err := json.Marshal(cleanup)
		if err == nil {
			out, err = probe.kubectl(manifest, "create", "-f", "-")
		}
		if err == nil {
			out, err = probe.kubectl(
				nil,
				"wait",
				"--for=jsonpath={.status.phase}=Succeeded",
				"pod/cleanup",
				"--timeout=20s",
			)
		}
		if err != nil {
			t.Errorf("host directory cleanup: %v %s", err, out)
		}
		out, err = probe.kubectl(nil, "delete", "namespace", probe.namespace, "--wait=true", "--timeout=30s")
		if err != nil {
			t.Errorf("namespace cleanup: %v %s", err, out)
		}
	})
	t.Logf("namespace=%s hostPath=%s image=%s", probe.namespace, hostPath, workloadImage)
	probe.create(t, map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "base"},
		"spec": map[string]any{
			"accessModes": []string{"ReadWriteOnce"}, "storageClassName": "local-path",
			"resources": map[string]any{"requests": map[string]string{"storage": "64Mi"}},
		},
	})
	baseVolume := map[string]any{"name": "base", "persistentVolumeClaim": map[string]any{"claimName": "base"}}
	materializer := layerfsPod("materializer", map[string]any{"privileged": true, "runAsUser": 0},
		[]any{
			map[string]any{
				"name":     "layers",
				"hostPath": map[string]string{"path": hostPath, "type": "DirectoryOrCreate"},
			},
			baseVolume,
		},
		[]any{
			map[string]any{"name": "layers", "mountPath": "/layers", "mountPropagation": "Bidirectional"},
			map[string]any{"name": "base", "mountPath": "/base", "readOnly": true},
		}, []string{"sleep", "900"})
	materializer["spec"].(map[string]any)["initContainers"] = []any{map[string]any{
		"name": "seed-base", "image": workloadImage, "imagePullPolicy": "Never",
		"command": []string{"sh", "-ec", `
mkdir -p /base/opaque /base/deleted-dir /base/dir-to-file
printf original > /base/tracked
printf deleted > /base/deleted
printf old > /base/opaque/old
printf old > /base/deleted-dir/old
printf old > /base/dir-to-file/old
printf old > /base/file-to-dir
dd if=/dev/zero of=/base/unchanged-large bs=1024 count=1024
python3 -c 'from pathlib import Path
root=Path("/base")
(root/"中文删除").write_text("old")
for name in ("中文目录", "long-"+"a"*110):
    directory=root/name
    directory.mkdir()
    (directory/"old").write_text("old")
'
printf '#!/bin/sh\nprintf executable\n' > /base/executable
printf '\000\001\377' > /base/binary
ln -s tracked /base/link
chmod 0755 /base/executable
chown -R 65534:65534 /base
chmod 0750 /base
`},
		"volumeMounts": []any{map[string]any{"name": "base", "mountPath": "/base"}},
	}}
	probe.create(t, materializer)
	probe.must(t, nil, "wait", "--for=condition=Ready", "pod/materializer", "--timeout=30s")
	materializerReady = true
	checkpointScript, err := os.ReadFile("testdata/layerfs_checkpoint.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"layerfs_checkpoint.py", "layerfs_checkpoint_test.py"} {
		script, err := os.ReadFile("testdata/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		probe.must(
			t,
			script,
			"exec",
			"-i",
			"materializer",
			"--",
			"sh",
			"-ec",
			"umask 077; cat > /tmp/"+filename,
		)
	}
	t.Logf("checkpoint archive tests: %s", probe.must(t, nil, "exec", "materializer", "--",
		"env", "TMPDIR=/layers", "python3", "-B", "/tmp/layerfs_checkpoint_test.py"))
	t.Logf("backing filesystems: %s", probe.must(t, nil, "exec", "materializer", "--", "sh", "-ec",
		"stat -f -c '%T %m' /layers /base; cat /proc/self/mountinfo | grep -E ' /layers | /base '"))
	probe.must(t, nil, "exec", "materializer", "--", "sh", "-ec", `
mkdir -p /layers/private
chmod 0700 /layers/private
for workspace in alpha beta; do
  mkdir -p /layers/private/$workspace/upper /layers/private/$workspace/work /layers/exports/$workspace/workspace
  chown 65534:65534 /layers/private/$workspace/upper
  chmod 0750 /layers/private/$workspace/upper
done
`)
	baseDigest := strings.TrimSpace(
		string(probe.must(t, nil, "exec", "materializer", "--", "python3", "-c", layerfsDigest, "/base")),
	)
	probe.must(t, nil, "exec", "materializer", "--", "python3", "-c", `
import errno,pathlib
try:
    pathlib.Path("/base/forbidden").write_text("unexpected")
except OSError as error:
    assert error.errno == errno.EROFS, error
else:
    raise AssertionError("base PVC is writable")
`)
	consumer := func(name string) map[string]any {
		return layerfsPod(
			name,
			map[string]any{
				"runAsUser": 65534, "runAsGroup": 65534, "runAsNonRoot": true,
				"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
				"capabilities":   map[string]any{"drop": []string{"ALL"}},
				"seccompProfile": map[string]string{"type": "RuntimeDefault"},
			},
			[]any{layerfsHostVolume(hostPath + "/exports/" + name)},
			[]any{
				map[string]any{
					"name":             "layers",
					"mountPath":        "/views",
					"mountPropagation": "HostToContainer",
				},
			},
			[]string{"sleep", "900"},
		)
	}
	for _, name := range []string{"alpha", "beta"} {
		probe.create(t, consumer(name))
		probe.must(t, nil, "wait", "--for=condition=Ready", "pod/"+name, "--timeout=30s")
		probe.must(t, nil, "exec", name, "--", "sh", "-ec", "test ! -e /views/workspace/tracked")
	}
	probe.must(t, nil, "exec", "materializer", "--", "sh", "-ec", `
for workspace in alpha beta; do
  mount -t overlay overlay -o lowerdir=/base,upperdir=/layers/private/$workspace/upper,workdir=/layers/private/$workspace/work,index=off,metacopy=off,redirect_dir=off /layers/exports/$workspace/workspace
done
`)
	for _, name := range []string{"alpha", "beta"} {
		probe.must(t, nil, "exec", name, "--", "sh", "-ec", `
test "$(cat /views/workspace/tracked)" = original
grep ' /views/workspace .* - overlay ' /proc/self/mountinfo
test ! -e /layers && test ! -e /base
test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token
grep -E '^(CapEff|NoNewPrivs|Seccomp):' /proc/self/status
`)
	}
	probe.must(t, nil, "exec", "alpha", "--", "python3", "-c", `
import os,pathlib,shutil
root=pathlib.Path("/views/workspace")
status=dict(line.split(":",1) for line in pathlib.Path("/proc/self/status").read_text().splitlines())
assert os.getuid() == 65534 and int(status["CapEff"].strip(),16) == 0
assert status["NoNewPrivs"].strip() == "1" and status["Seccomp"].strip() == "2"
(root/"tracked").write_text("changed")
(root/"untracked").write_text("new")
(root/"deleted").unlink()
shutil.rmtree(root/"opaque")
(root/"opaque").mkdir()
(root/"opaque/new").write_text("replacement")
(root/"binary").write_bytes(bytes([255,0,128,10]))
(root/"executable").chmod(0o700)
(root/"hard-a").write_text("linked")
os.link(root/"hard-a", root/"hard-b")
os.setxattr(root/"hard-a", "user.notes", b"linked metadata")
os.setxattr(root/"tracked", "user.notes", b"checkpoint metadata")
shutil.rmtree(root/"deleted-dir")
shutil.rmtree(root/"dir-to-file")
(root/"dir-to-file").write_text("now a file")
(root/"file-to-dir").unlink()
(root/"file-to-dir").mkdir()
(root/"file-to-dir/new").write_text("now a directory")
(root/"中文删除").unlink()
for name in ("中文目录", "long-"+"a"*110):
    shutil.rmtree(root/name)
    (root/name).mkdir()
    (root/name/"new").write_text("replacement")
root.chmod(0o700)
(root/"escape").symlink_to("/layers/private/alpha/upper/tracked")
try:
    (root/"escape").read_bytes()
except OSError:
    pass
else:
    raise AssertionError("private upper exposed")
assert (root/"link").read_text() == "changed"
`)
	changedDigest := strings.TrimSpace(
		string(probe.must(t, nil, "exec", "alpha", "--", "python3", "-c", layerfsDigest, "/views/workspace")),
	)
	for _, location := range []struct{ pod, path string }{{pod: "materializer", path: "/base"}, {pod: "beta", path: "/views/workspace"}} {
		got := strings.TrimSpace(
			string(
				probe.must(
					t,
					nil,
					"exec",
					location.pod,
					"--",
					"python3",
					"-c",
					layerfsDigest,
					location.path,
				),
			),
		)
		if got != baseDigest {
			t.Fatalf("%s changed after alpha writes: got %s want %s", location.path, got, baseDigest)
		}
	}
	t.Logf("base and beta unchanged: sha256:%s; alpha: sha256:%s", baseDigest, changedDigest)
	out := probe.must(t, nil, "exec", "materializer", "--", "python3", "-c", `
import os,pathlib,stat
root=pathlib.Path("/layers/private/alpha/upper")
deleted=(root/"deleted").lstat()
whiteout=stat.S_ISCHR(deleted.st_mode) and deleted.st_rdev == 0
if not whiteout:
    whiteout=stat.S_ISREG(deleted.st_mode) and deleted.st_size == 0 and os.getxattr(root/"deleted","trusted.overlay.whiteout") == b""
assert whiteout, "missing whiteout"
assert os.getxattr(root/"opaque","trusted.overlay.opaque") == b"y"
print("whiteout=verified opaque=y")
`)
	t.Log(strings.TrimSpace(string(out)))
	probe.must(t, nil, "delete", "pod", "alpha", "--wait=true", "--timeout=20s")
	probe.must(t, nil, "exec", "materializer", "--", "sh", "-ec", `
sync
umount /layers/exports/alpha/workspace
mount -t overlay overlay -o lowerdir=/base,upperdir=/layers/private/alpha/upper,workdir=/layers/private/alpha/work,index=off,metacopy=off,redirect_dir=off /layers/exports/alpha/workspace
`)
	probe.create(t, consumer("alpha"))
	probe.must(t, nil, "wait", "--for=condition=Ready", "pod/alpha", "--timeout=30s")
	restoredDigest := strings.TrimSpace(
		string(probe.must(t, nil, "exec", "alpha", "--", "python3", "-c", layerfsDigest, "/views/workspace")),
	)
	if restoredDigest != changedDigest {
		t.Fatalf("remount lost workspace state: got %s want %s", restoredDigest, changedDigest)
	}
	probe.must(t, nil, "exec", "alpha", "--", "sh", "-ec", `
test ! -e /views/workspace/deleted && test ! -e /views/workspace/opaque/old
test "$(cat /views/workspace/opaque/new)" = replacement
test "$(/views/workspace/executable)" = executable
`)
	t.Log("consumer replacement and overlay remount retained contents, deletions, links and modes on the same node")

	digest := func(pod, root string) string {
		t.Helper()
		return strings.TrimSpace(string(probe.must(t, nil, "exec", pod, "--",
			"python3", "-B", "-c", string(checkpointScript), "digest", root)))
	}
	checkpointBaseDigest := digest("materializer", "/base")
	checkpointWorkspaceDigest := digest("alpha", "/views/workspace")
	probe.must(t, nil, "delete", "pod", "alpha", "--wait=true", "--timeout=20s")
	probe.must(t, nil, "exec", "materializer", "--", "sh", "-ec",
		"sync; umount /layers/exports/alpha/workspace")
	packStarted := time.Now()
	packed := probe.must(t, nil, "exec", "materializer", "--", "python3", "-B", "/tmp/layerfs_checkpoint.py",
		"pack", "/layers/private/alpha/upper", "/layers/checkpoints", checkpointBaseDigest)
	packElapsed := time.Since(packStarted)
	var checkpoint struct {
		ManifestDigest string `json:"manifestDigest"`
		LayerDigest    string `json:"layerDigest"`
		Bytes          int64  `json:"bytes"`
	}
	if err := json.Unmarshal(packed, &checkpoint); err != nil {
		t.Fatalf("checkpoint result: %v: %s", err, packed)
	}
	if checkpoint.Bytes <= 0 || checkpoint.Bytes >= 1024*1024 {
		t.Fatalf(
			"expected an incremental archive smaller than unchanged base file, got %d bytes",
			checkpoint.Bytes,
		)
	}
	probe.must(t, nil, "exec", "materializer", "--", "python3", "-c", `
import pathlib,sys,tarfile
with tarfile.open("/layers/checkpoints/"+sys.argv[1][7:],"r:") as archive:
    members=archive.getmembers()
    names={member.name for member in members}
    assert {".wh.deleted",".wh.deleted-dir","opaque/.wh..wh..opq"} <= names, names
    assert "unchanged-large" not in names and "link" not in names, names
    assert any(member.islnk() for member in members), names
    assert not any(member.isdev() for member in members), names
    assert not any("overlay." in key for member in members for key in member.pax_headers)
    print("OCI markers and hardlink verified; unchanged base file excluded")
`, checkpoint.LayerDigest)
	probe.must(t, nil, "exec", "materializer", "--", "python3", "-c", `
import pathlib,shutil
source=pathlib.Path("/layers/private/alpha")
assert not any(line.split()[4] == "/layers/exports/alpha/workspace"
               for line in pathlib.Path("/proc/self/mountinfo").read_text().splitlines())
shutil.rmtree(source)
pathlib.Path("/layers/private/restored").mkdir(mode=0o700)
assert not source.exists()
`)
	restoreStarted := time.Now()
	probe.must(
		t,
		nil,
		"exec",
		"materializer",
		"--",
		"python3",
		"-B",
		"/tmp/layerfs_checkpoint.py",
		"restore",
		"/layers/checkpoints",
		checkpoint.ManifestDigest,
		checkpointBaseDigest,
		"/layers/private/restored/upper",
	)
	probe.must(t, nil, "exec", "materializer", "--", "sh", "-ec", `
test ! -e /layers/private/alpha
mkdir /layers/private/restored/work
mount -t overlay overlay -o lowerdir=/base,upperdir=/layers/private/restored/upper,workdir=/layers/private/restored/work,index=off,metacopy=off,redirect_dir=off /layers/exports/alpha/workspace
`)
	probe.create(t, consumer("alpha"))
	probe.must(t, nil, "wait", "--for=condition=Ready", "pod/alpha", "--timeout=30s")
	restoreElapsed := time.Since(restoreStarted)
	if got := digest("alpha", "/views/workspace"); got != checkpointWorkspaceDigest {
		t.Fatalf(
			"fresh-upper restore changed contents, metadata or links: got %s want %s",
			got,
			checkpointWorkspaceDigest,
		)
	}
	probe.must(t, nil, "exec", "alpha", "--", "python3", "-c", `
import os,pathlib
root=pathlib.Path("/views/workspace")
for name in ("deleted", "deleted-dir", "opaque/old", "中文删除", "中文目录/old", "long-"+"a"*110+"/old"):
    assert not os.path.lexists(root/name), name
for name in ("opaque", "中文目录", "long-"+"a"*110):
    assert (root/name/"new").read_text() == "replacement"
assert (root/"dir-to-file").is_file() and (root/"file-to-dir/new").read_text() == "now a directory"
assert os.stat(root/"hard-a").st_ino == os.stat(root/"hard-b").st_ino
assert os.getxattr(root/"hard-b", "user.notes") == b"linked metadata"
(root/"hard-a").write_text("hardlink still shared")
assert (root/"hard-b").read_text() == "hardlink still shared"
`)
	for _, location := range []struct{ pod, root string }{{pod: "materializer", root: "/base"}, {pod: "beta", root: "/views/workspace"}} {
		if got := digest(location.pod, location.root); got != checkpointBaseDigest {
			t.Fatalf("checkpoint changed base or sibling: got %s want %s", got, checkpointBaseDigest)
		}
	}
	t.Logf("checkpoint=%s layer=%s bytes=%d pack_with_kubectl=%s restore_and_pod_ready=%s",
		checkpoint.ManifestDigest, checkpoint.LayerDigest, checkpoint.Bytes, packElapsed, restoreElapsed)
	t.Logf("fresh-upper restore verified after deleting original upper/work; base=%s workspace=%s",
		checkpointBaseDigest, checkpointWorkspaceDigest)
}

const layerfsDigest = `
import hashlib,json,os,pathlib,stat,sys
root=pathlib.Path(sys.argv[1])
entries=[]
for path in sorted(root.rglob("*")):
    info=path.lstat()
    kind=stat.S_IFMT(info.st_mode)
    value=None
    if stat.S_ISLNK(info.st_mode):
        value=os.readlink(path)
    elif stat.S_ISREG(info.st_mode):
        value=hashlib.sha256(path.read_bytes()).hexdigest()
    entries.append([str(path.relative_to(root)),kind,stat.S_IMODE(info.st_mode),value])
print(hashlib.sha256(json.dumps(entries,separators=(",",":")).encode()).hexdigest())
`

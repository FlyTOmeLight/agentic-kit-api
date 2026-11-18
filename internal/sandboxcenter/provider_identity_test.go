package sandboxcenter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestroyObjectUsesUIDPrecondition(t *testing.T) {
	t.Run("deletes the original object", func(t *testing.T) {
		calls, body := fakeKubectl(t, "uid-original")
		provider := &Provider{cfg: K8sConfig{Context: "fixed", Namespace: "sandbox-test"}}

		if err := provider.destroyObject(
			context.Background(),
			"pods",
			"sandbox-1",
			"uid-original",
		); err != nil {
			t.Fatal(err)
		}

		logged, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(
			string(logged),
			"delete --raw /api/v1/namespaces/sandbox-test/pods/sandbox-1 -f -",
		) {
			t.Fatalf("raw delete was not issued: %s", logged)
		}
		payload, err := os.ReadFile(body)
		if err != nil {
			t.Fatal(err)
		}
		var options struct {
			Preconditions struct {
				UID string `json:"uid"`
			} `json:"preconditions"`
		}
		if err := json.Unmarshal(payload, &options); err != nil {
			t.Fatal(err)
		}
		if options.Preconditions.UID != "uid-original" {
			t.Fatalf("delete precondition UID = %q, want uid-original", options.Preconditions.UID)
		}
	})

	t.Run("rejects a replacement with the same name", func(t *testing.T) {
		calls, _ := fakeKubectl(t, "uid-replacement")
		provider := &Provider{cfg: K8sConfig{Context: "fixed", Namespace: "sandbox-test"}}

		err := provider.destroyObject(context.Background(), "pods", "sandbox-1", "uid-original")
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("destroyObject error = %v, want ErrConflict", err)
		}
		logged, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(logged), " delete ") {
			t.Fatalf("replacement object was deleted: %s", logged)
		}
	})
}

func fakeKubectl(t *testing.T, currentUID string) (calls, body string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	body = filepath.Join(dir, "body")
	absent := filepath.Join(dir, "absent")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALL_LOG"
case " $* " in
  *" get "*)
    if [ -f "$ABSENT_FILE" ]; then exit 0; fi
    printf '%s' "$CURRENT_UID"
    ;;
  *" delete "*)
    cat > "$BODY_FILE"
    touch "$ABSENT_FILE"
    ;;
  *)
    echo "unexpected kubectl invocation: $*" >&2
    exit 99
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALL_LOG", calls)
	t.Setenv("BODY_FILE", body)
	t.Setenv("ABSENT_FILE", absent)
	t.Setenv("CURRENT_UID", currentUID)
	return calls, body
}

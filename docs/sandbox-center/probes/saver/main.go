// The sandbox saver runs inside a per-sandbox pod: it packs the stopped
// workspace PVC into a content-addressed checkpoint on the checkpoint store
// and writes the resulting checkpoint identifier to a result file.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// baseLayerDigest is the empty-tree base required by the checkpoint tool's
// fixed 71-character digest contract.
const baseLayerDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func main() {
	var err error
	if len(os.Args) == 5 {
		err = saveAttempt(os.Args[1:])
	} else if len(os.Args) == 1 {
		err = run("/workspace", "/checkpoints")
	} else {
		err = errors.New("expected checkpoint directory, sandbox ID, generation and attempt")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "save: %v\n", err)
		os.Exit(1)
	}
}

func saveAttempt(args []string) error {
	if !filepath.IsLocal(args[0]) {
		return errors.New("invalid checkpoint directory")
	}
	command := exec.Command("python3", "-B", "/usr/local/bin/layerfs_checkpoint.py",
		"save", "/workspace", filepath.Join("/checkpoints", args[0]), baseLayerDigest,
		args[1], args[2], args[3])
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func run(workspace, store string) error {
	packed, err := packOnce(workspace, store)
	if err != nil {
		return err
	}
	checkpoint := packed["manifestDigest"] + ":" + strings.TrimPrefix(packed["layerDigest"], "sha256:")
	if err := validate(packed, checkpoint, store); err != nil {
		return err
	}
	fmt.Println("checkpoint: " + checkpoint)
	return nil
}

func packOnce(workspace, store string) (map[string]string, error) {
	out, err := exec.Command("python3", "-B", "/usr/local/bin/layerfs_checkpoint.py",
		"pack", workspace, store, baseLayerDigest).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pack: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var packed map[string]any
	if err := json.Unmarshal(out, &packed); err != nil {
		return nil, fmt.Errorf("pack output %q: %w", strings.TrimSpace(string(out)), err)
	}
	result := map[string]string{}
	for _, field := range []string{"manifestDigest", "layerDigest"} {
		value, ok := packed[field].(string)
		if !ok {
			return nil, fmt.Errorf("pack output missing %s", field)
		}
		result[field] = value
	}
	return result, nil
}

func validate(packed map[string]string, checkpoint, store string) error {
	for _, field := range []string{"manifestDigest", "layerDigest"} {
		digest := packed[field]
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
			return fmt.Errorf("invalid %s %q", field, digest)
		}
		if _, err := os.Stat(filepath.Join(store, digest[7:])); err != nil {
			return fmt.Errorf("%s object missing: %w", field, err)
		}
	}
	if strings.Count(checkpoint, ":") != 2 {
		return fmt.Errorf("invalid checkpoint identifier %q", checkpoint)
	}
	return nil
}

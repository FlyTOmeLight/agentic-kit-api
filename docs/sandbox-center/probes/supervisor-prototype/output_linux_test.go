//go:build linux

package main

import (
	"os"
	"testing"
)

func TestOutputEvidence(t *testing.T) {
	t.Run("write_error_remains_visible_after_sync", func(t *testing.T) {
		path := t.TempDir() + "/output"
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		writer := &boundedOutput{file: file, remaining: 4}
		if _, err := writer.Write([]byte("test")); err == nil {
			t.Fatal("read-only descriptor accepted write")
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		if writer.err == nil {
			t.Fatal("successful Sync must not erase earlier output failure")
		}
	})
	t.Run("exact_limit_is_not_truncation", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		writer := &boundedOutput{file: file, remaining: 4}
		if n, err := writer.Write([]byte("four")); err != nil || n != 4 {
			t.Fatalf("write=%d %v", n, err)
		}
		if writer.truncated {
			t.Fatal("exact budget marked truncated")
		}
		if n, err := writer.Write([]byte("discard")); err != nil || n != 7 {
			t.Fatalf("drain=%d %v", n, err)
		}
		if !writer.truncated {
			t.Fatal("discarded output not marked")
		}
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 4 {
			t.Fatalf("output exceeds budget: %d", info.Size())
		}
	})
}

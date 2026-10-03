package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFsBlobStaysInsideRoot(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "instance")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	b := &fsBlob{root: root}
	ctx := context.Background()

	if err := b.Put(ctx, "export/2026/10/report.xlsx", "", strings.NewReader("data"), 4); err != nil {
		t.Fatalf("put: %v", err)
	}
	rc, err := b.Get(ctx, "/export/2026/10/report.xlsx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "data" {
		t.Fatalf("got %q", got)
	}

	for _, key := range []string{"", "/"} {
		if err := b.Put(ctx, key, "", strings.NewReader("x"), 1); err == nil {
			t.Errorf("key %q must be rejected", key)
		}
	}
	// Traversal attempts are clamped to the instance directory.
	for _, key := range []string{"../outside.txt", "export/../../outside.txt"} {
		if err := b.Put(ctx, key, "", strings.NewReader("x"), 1); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped the instance directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.txt")); err != nil {
		t.Fatalf("clamped file missing: %v", err)
	}
}

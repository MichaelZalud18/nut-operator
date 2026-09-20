package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopiesArePrivateAndSourceRemainsUnchanged(t *testing.T) {
	source, parent := t.TempDir(), t.TempDir()
	name := "kustomization.yaml"
	if err := os.WriteFile(filepath.Join(source, name), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := Clone(context.Background(), source, parent)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Clone(context.Background(), source, parent)
	if err != nil || a == b {
		t.Fatalf("workspaces not isolated: %q, %q, %v", a, b, err)
	}
	if err := os.WriteFile(filepath.Join(a, name), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{source, b} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "original" {
			t.Fatalf("source/other workspace changed: %q, %v", data, err)
		}
	}
}

func TestSymlinkAndNestedDestinationsFailWithoutLeakingCopies(t *testing.T) {
	source, parent := t.TempDir(), t.TempDir()
	if err := os.Symlink(parent, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(context.Background(), source, parent); err == nil {
		t.Fatal("symlink copied into workspace")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed clone leaked state")
	}
	if _, err := Clone(context.Background(), source, source); err == nil {
		t.Fatal("recursive destination accepted")
	}
}

func TestCancelledCloneDoesNotAllocate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Clone(ctx, t.TempDir(), t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

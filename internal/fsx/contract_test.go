package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteReplacesContentAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := AtomicWrite(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("content=%q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestResolveWithinRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	for _, candidate := range []string{"../outside", filepath.Join(root, "escape", "secret")} {
		if _, err := ResolveWithin(root, candidate); err == nil {
			t.Fatalf("candidate %q escaped workspace", candidate)
		}
	}
	if got, err := ResolveWithin(root, filepath.Join(root, "inside")); err != nil ||
		got != filepath.Join(root, "inside") {
		t.Fatalf("inside got=%q err=%v", got, err)
	}
}

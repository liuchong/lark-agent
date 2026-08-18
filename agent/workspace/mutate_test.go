package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTextAppliesDisjointReplacementsAgainstOriginal(t *testing.T) {
	root := t.TempDir()
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(filepath.Join(root, "router.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	report, source, err := scope.EditText("router.go", []TextEdit{
		{OldText: "alpha", NewText: "one"},
		{OldText: "gamma", NewText: "three"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one\nbeta\nthree\n" {
		t.Fatalf("file=%q", data)
	}
	if report.Replacements != 2 || source.Digest != report.Digest || report.PreviousDigest == report.Digest {
		t.Fatalf("report=%+v source=%+v", report, source)
	}
	if !strings.Contains(report.Diff, "-alpha") || !strings.Contains(report.Diff, "+one") {
		t.Fatalf("diff=%s", report.Diff)
	}
}

func TestEditTextRejectsNonUniqueAndOverlappingReplacements(t *testing.T) {
	root := t.TempDir()
	original := "repeat repeat overlap"
	if err := os.WriteFile(filepath.Join(root, "router.go"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.EditText("router.go", []TextEdit{{OldText: "repeat", NewText: "once"}}); err == nil {
		t.Fatal("non-unique old_text succeeded")
	}
	if _, _, err := scope.EditText("router.go", []TextEdit{
		{OldText: "repeat repeat", NewText: "x"},
		{OldText: "repeat overlap", NewText: "y"},
	}); err == nil {
		t.Fatal("overlapping replacements succeeded")
	}
	data, err := os.ReadFile(filepath.Join(root, "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("file changed: %q", data)
	}
}

func TestWriteTextCreatesAndOverwrites(t *testing.T) {
	root := t.TempDir()
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := scope.WriteText("service/new.go", "package service\n")
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created {
		t.Fatalf("created=%+v", created)
	}
	overwritten, _, err := scope.WriteText("service/new.go", "package other\n")
	if err != nil {
		t.Fatal(err)
	}
	if overwritten.Created || overwritten.Digest == created.Digest {
		t.Fatalf("overwritten=%+v previous=%+v", overwritten, created)
	}
	data, err := os.ReadFile(filepath.Join(root, "service", "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package other\n" {
		t.Fatalf("file=%q", data)
	}
}

func TestWriteTextRejectsEscapeAndExcludedPaths(t *testing.T) {
	root := t.TempDir()
	scope, err := NewScopeWithExcludes(root, []string{"secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.WriteText("../outside.go", "nope"); err == nil {
		t.Fatal("parent traversal write succeeded")
	}
	if _, _, err := scope.EditText("secret.txt", []TextEdit{{OldText: "a", NewText: "b"}}); err == nil {
		t.Fatal("excluded edit succeeded")
	}
}

func TestSamePathMutationsAreSerialized(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "router.go"), []byte("value=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.EditText("router.go", []TextEdit{{OldText: "value=1", NewText: "value=2"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.EditText("router.go", []TextEdit{{OldText: "value=2", NewText: "value=3"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "value=3\n" {
		t.Fatalf("file=%q", data)
	}
}

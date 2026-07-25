package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestLoadReadsOnlyWorkspaceRules(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "rule.md"), []byte("child"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	set, err := Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Files) != 2 {
		t.Fatalf("files=%+v", set.Files)
	}
	for _, file := range set.Files {
		if file.Content == "parent" {
			t.Fatal("loaded parent AGENTS.md outside workspace")
		}
	}
}

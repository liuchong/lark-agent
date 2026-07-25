package larkagent_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneModuleDoesNotImportOfficialCLI(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		assertNoOfficialImport(t, filepath.Join(root, name))
	}
	for _, dir := range []string{"agent", "cmd", "internal", "integration_test"} {
		base := filepath.Join(root, dir)
		_ = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				t.Fatal(err)
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") {
				assertNoOfficialImport(t, path)
			}
			return nil
		})
	}
}

func TestRepositoryDoesNotCopyOfficialInternalTrees(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"internal/service/im",
		"internal/event",
		"internal/larkcli",
		"events",
		"cmd/event",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("forbidden copied tree exists: %s", path)
		}
	}
}

func TestProductionCodeDoesNotExecuteLarkCLI(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range []string{"agent/cmd", "cmd", "internal"} {
		base := filepath.Join(root, dir)
		_ = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				t.Fatal(err)
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			bannedCommand := "lark" + "-cli"
			if strings.Contains(source, "exec.Command") && strings.Contains(source, bannedCommand) {
				t.Fatalf("production code can execute a local Lark command tool in %s", path)
			}
			return nil
		})
	}
}

func assertNoOfficialImport(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	forbiddenModule := "github.com/" + "larksuite/cli"
	if strings.Contains(string(data), forbiddenModule) {
		t.Fatalf("official Go dependency found in %s", path)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

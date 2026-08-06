package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestGitHistoryDefinitionReturnsBoundedLocalEvidence(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "service")
	mustRunGit(t, "", "init", repo)
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "add", "main.go")
	mustRunGit(t, repo,
		"-c", "user.name=Test User",
		"-c", "user.email=test@example.invalid",
		"commit", "-m", "fix: keep local history bounded",
	)

	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(GitDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.Execute(context.Background(), "inspect_git_history", json.RawMessage(`{
		"path":"service",
		"max_commits":50
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.Content) > 8*1024 {
		t.Fatalf("git evidence exceeds byte bound: %d", len(execution.Content))
	}
	if !strings.Contains(execution.Content, "fix: keep local history bounded") ||
		!strings.Contains(execution.Content, `"max_commits":20`) {
		t.Fatalf("content=%s", execution.Content)
	}
}

func TestGitHistoryDefinitionRejectsWorkspaceEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustRunGit(t, "", "init", outside)
	if err := os.Symlink(outside, filepath.Join(root, "linked-repo")); err != nil {
		t.Fatal(err)
	}
	metadataEscape := filepath.Join(root, "metadata-escape")
	if err := os.MkdirAll(metadataEscape, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(metadataEscape, ".git"),
		[]byte("gitdir: "+filepath.Join(outside, ".git")+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(GitDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "linked-repo", "metadata-escape"} {
		t.Run(path, func(t *testing.T) {
			_, err := registry.Execute(
				context.Background(),
				"inspect_git_history",
				json.RawMessage(`{"path":`+strconvQuote(path)+`}`),
			)
			if err == nil {
				t.Fatalf("expected workspace confinement error for %q", path)
			}
		})
	}
}

func TestGitHistoryDefinitionIgnoresInheritedRepositoryRedirects(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "service")
	mustRunGit(t, "", "init", repo)
	writeAndCommitGitFixture(t, repo, "inside.txt", "inside\n", "fix: inside workspace")

	outside := t.TempDir()
	mustRunGit(t, "", "init", outside)
	writeAndCommitGitFixture(t, outside, "outside.txt", "outside\n", "leak: outside workspace")

	t.Setenv("GIT_DIR", filepath.Join(outside, ".git"))
	t.Setenv("GIT_WORK_TREE", outside)

	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(GitDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.Execute(
		context.Background(),
		"inspect_git_history",
		json.RawMessage(`{"path":"service","max_commits":5}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execution.Content, "fix: inside workspace") ||
		strings.Contains(execution.Content, "leak: outside workspace") {
		t.Fatalf("inherited Git environment escaped workspace: %s", execution.Content)
	}
}

func writeAndCommitGitFixture(t *testing.T, repo, name, content, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "add", name)
	mustRunGit(t, repo,
		"-c", "user.name=Test User",
		"-c", "user.email=test@example.invalid",
		"commit", "-m", subject,
	)
}

func mustRunGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

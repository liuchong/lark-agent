package larkagent_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestDelegatedReadOnlyRunCanInspectWorkspaceLocalGitHistory(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "service")
	runFixtureGit(t, "", "init", repo)
	if err := os.WriteFile(filepath.Join(repo, "status.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, repo, "add", "status.txt")
	runFixtureGit(t, repo,
		"-c", "user.name=Fixture",
		"-c", "user.email=fixture@example.invalid",
		"commit", "-m", "fix: validate current behavior",
	)

	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(agenttools.GitDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	visible := registry.InfosFor(agenttools.InvocationScope{ReadOnly: true})
	if len(visible) != 1 || visible[0].Name != "inspect_git_history" {
		t.Fatalf("visible tools=%+v", visible)
	}
	ctx := agenttools.WithInvocationScope(context.Background(), agenttools.InvocationScope{ReadOnly: true})
	execution, err := registry.Execute(
		ctx,
		"inspect_git_history",
		json.RawMessage(`{"path":"service","max_commits":5}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execution.Content, "fix: validate current behavior") {
		t.Fatalf("content=%s", execution.Content)
	}
}

func TestDelegatedReadOnlyGitHistoryCannotBeRedirectedByEnvironment(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "service")
	runFixtureGit(t, "", "init", repo)
	writeGitFixtureCommit(t, repo, "inside.txt", "inside\n", "fix: bounded workspace evidence")

	outside := t.TempDir()
	runFixtureGit(t, "", "init", outside)
	writeGitFixtureCommit(t, outside, "outside.txt", "outside\n", "leak: external evidence")

	t.Setenv("GIT_DIR", filepath.Join(outside, ".git"))
	t.Setenv("GIT_WORK_TREE", outside)

	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(agenttools.GitDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agenttools.WithInvocationScope(context.Background(), agenttools.InvocationScope{ReadOnly: true})
	execution, err := registry.Execute(
		ctx,
		"inspect_git_history",
		json.RawMessage(`{"path":"service","max_commits":5}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execution.Content, "fix: bounded workspace evidence") ||
		strings.Contains(execution.Content, "leak: external evidence") {
		t.Fatalf("inherited Git environment escaped workspace: %s", execution.Content)
	}
}

func TestInitialWorkspaceContextIncludesManifestlessGitRepository(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "docs-only")
	runFixtureGit(t, "", "init", repo)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("docs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := (agentcontext.Builder{Scope: scope}).Build(
		domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_context", Content: "当前有哪些项目？"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range bundle.Environment.Projects {
		if project.Path == "docs-only" && project.Kind == "git" {
			return
		}
	}
	t.Fatalf("manifestless Git repository missing: %+v", bundle.Environment.Projects)
}

func writeGitFixtureCommit(t *testing.T, repo, name, content, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, repo, "add", name)
	runFixtureGit(t, repo,
		"-c", "user.name=Fixture",
		"-c", "user.email=fixture@example.invalid",
		"commit", "-m", subject,
	)
}

func runFixtureGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

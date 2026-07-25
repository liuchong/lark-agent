package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestWorkspaceDefinitionsReadSearchRulesAndSkills(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeToolFixture(t, filepath.Join(root, "service", "AGENTS.md"), "service rules")
	writeToolFixture(t, filepath.Join(root, "service", "router.go"), "package service\n// rate limit middleware")
	writeToolFixture(t, filepath.Join(root, "other", "AGENTS.md"), "other rules")
	writeToolFixture(t, filepath.Join(root, "service", ".agents", "skills", "inspect", "SKILL.md"), "inspect safely")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}

	search, err := registry.Execute(context.Background(), "search_workspace", []byte(`{"query":"rate limit middleware"}`))
	if err != nil || !strings.Contains(search.Content, "router.go") {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	read, err := registry.Execute(context.Background(), "read_workspace", []byte(`{"path":"service/router.go"}`))
	if err != nil || len(read.Sources) != 1 || !strings.Contains(read.Content, "rate limit") {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	rules, err := registry.Execute(context.Background(), "read_workspace_rules", []byte(`{"path":"service/router.go"}`))
	if err != nil || !strings.Contains(rules.Content, "root rules") || !strings.Contains(rules.Content, "service rules") ||
		strings.Contains(rules.Content, "other rules") {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
	skills, err := registry.Execute(context.Background(), "list_skills", []byte(`{}`))
	if err != nil || !strings.Contains(skills.Content, "service/.agents/skills/inspect/SKILL.md") {
		t.Fatalf("skills=%+v err=%v", skills, err)
	}
	loaded, err := registry.Execute(context.Background(), "load_skill", []byte(`{"path":"service/.agents/skills/inspect/SKILL.md"}`))
	if err != nil || !strings.Contains(loaded.Content, "inspect safely") || len(loaded.Sources) != 1 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestWorkspaceDefinitionsRejectEscapesAndExcludedPaths(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "secret.txt"), "hidden")
	scope, err := workspace.NewScopeWithExcludes(root, []string{"secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"path":"../outside"}`, `{"path":"secret.txt"}`} {
		if _, err := registry.Execute(context.Background(), "read_workspace", []byte(args)); err == nil {
			t.Fatalf("read_workspace accepted %s", args)
		}
	}
}

func TestExploreWorkspaceReturnsReadOnlyEvidenceSummary(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "service", "router.go"), "POST /api/sample/items")
	writeToolFixture(t, filepath.Join(root, "service", "repo.go"), "SampleDB sample items query")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}

	execution, err := registry.Execute(context.Background(), "explore_workspace", []byte(`{
		"focus":"sample items pagination persistence",
		"queries":["sample/items","SampleDB"],
		"max_results_per_query":3
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execution.Content, "sample items pagination persistence") ||
		!strings.Contains(execution.Content, "router.go") ||
		!strings.Contains(execution.Content, "repo.go") ||
		len(execution.Sources) < 2 {
		t.Fatalf("execution=%+v", execution)
	}
}

func writeToolFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

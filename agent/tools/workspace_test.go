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

func TestSearchWorkspacePathConfinesResultsToRequestedSubtree(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(
		t,
		filepath.Join(root, "sample-project", "sample-module", "request.kt"),
		"class SampleRequest\nval sampleContent: String",
	)
	writeToolFixture(
		t,
		filepath.Join(root, "Sample-Module", "request.kt"),
		"class SampleRequest\nval sampleContent: String",
	)
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}

	search, err := registry.Execute(context.Background(), "search_workspace", []byte(`{
		"query":"SampleRequest sampleContent",
		"path":"sample-project/sample-module"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(search.Content, "sample-project/sample-module/request.kt") ||
		strings.Contains(search.Content, "Sample-Module/request.kt") {
		t.Fatalf("search escaped requested subtree: %s", search.Content)
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
	if _, err := registry.Execute(
		context.Background(),
		"search_workspace",
		[]byte(`{"query":"hidden","path":"../outside"}`),
	); err == nil {
		t.Fatal("search_workspace accepted parent traversal")
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

func TestWorkspaceReadSearchAndListAcceptRangeGlobAndRegex(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "service", "router.go"), "package service\nfunc RateLimit() {}\nfunc Timeout() {}\n")
	writeToolFixture(t, filepath.Join(root, "service", "notes.md"), "RateLimit notes\n")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceDefinitions(scope)...)
	if err != nil {
		t.Fatal(err)
	}
	read, err := registry.Execute(context.Background(), "read_workspace", []byte(`{"path":"service/router.go","offset":2,"limit":1}`))
	if err != nil || !strings.Contains(read.Content, "func RateLimit()") || !strings.Contains(read.Content, `"start_line":2`) {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	search, err := registry.Execute(context.Background(), "search_workspace", []byte(`{
		"query":"func RateLimit\\(\\)",
		"regex":true,
		"glob":"**/*.go",
		"context_lines":1
	}`))
	if err != nil || !strings.Contains(search.Content, "service/router.go") || strings.Contains(search.Content, "notes.md") {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	listed, err := registry.Execute(context.Background(), "list_workspace", []byte(`{"glob":"**/*.go"}`))
	if err != nil || !strings.Contains(listed.Content, "service/router.go") || strings.Contains(listed.Content, "notes.md") {
		t.Fatalf("listed=%+v err=%v", listed, err)
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

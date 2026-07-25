package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestCodeIndexDefinitionsFallbackSearchesWorkspace(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "service", "router.go"), `package service
func RegisterRoutes() {
	POST("/api/sample/items")
}`)
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(CodeIndexDefinitions(scope, nil)...)
	if err != nil {
		t.Fatal(err)
	}

	execution, err := registry.Execute(context.Background(), "search_code_symbols", []byte(`{
		"query": "sample/items",
		"max_results": 5
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execution.Content, `"index_available":false`) {
		t.Fatalf("expected unavailable fallback marker: %s", execution.Content)
	}
	if !strings.Contains(execution.Content, "router.go") || len(execution.Sources) == 0 {
		t.Fatalf("fallback did not return workspace evidence: content=%s sources=%+v", execution.Content, execution.Sources)
	}
}

func TestTraceCodePathReportsUnavailableWithoutProvider(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(CodeIndexDefinitions(scope, nil)...)
	if err != nil {
		t.Fatal(err)
	}

	execution, err := registry.Execute(context.Background(), "trace_code_path", []byte(`{
		"symbol": "MessageRequestsApi.GetPaginationNonContacts"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		IndexAvailable bool     `json:"index_available"`
		SuggestedTools []string `json:"suggested_tools"`
	}
	if err := json.Unmarshal([]byte(execution.Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.IndexAvailable || !contains(payload.SuggestedTools, "search_workspace") || !contains(payload.SuggestedTools, "read_workspace") {
		t.Fatalf("payload=%+v", payload)
	}
}

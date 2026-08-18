package larkagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/agent/workspace"

	"github.com/cloudwego/eino/schema"
)

func TestWorkspaceStructuredEditsAndReadSearchCompleteness(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "service", "router.go"), "package service\nfunc RateLimit() int { return 10 }\nfunc Timeout() {}\n")
	mustWrite(t, filepath.Join(root, "service", "notes.md"), "RateLimit notes\n")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(append(
		agenttools.WorkspaceDefinitions(scope),
		agenttools.WorkspaceMutationDefinitions(scope, agenttools.WorkspaceMutationOptions{})...,
	)...)
	if err != nil {
		t.Fatal(err)
	}

	ownerWrite := agenttools.WithInvocationScope(context.Background(), agenttools.InvocationScope{
		Owner: true, WorkspaceWriteAllowed: true,
	})
	edited, err := registry.Execute(ownerWrite, "edit_workspace", json.RawMessage(`{
		"path":"service/router.go",
		"edits":[{"old_text":"return 10","new_text":"return 20"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var editReport map[string]any
	if err := json.Unmarshal([]byte(edited.Content), &editReport); err != nil {
		t.Fatal(err)
	}
	if editReport["digest"] == editReport["previous_digest"] {
		t.Fatalf("digest did not change: %s", edited.Content)
	}
	reread, err := registry.Execute(ownerWrite, "read_workspace", json.RawMessage(`{"path":"service/router.go"}`))
	if err != nil || !strings.Contains(reread.Content, "return 20") || len(reread.Sources) != 1 {
		t.Fatalf("reread=%+v err=%v", reread, err)
	}
	if reread.Sources[0].Digest != editReport["digest"] {
		t.Fatalf("reread digest=%s edit digest=%v", reread.Sources[0].Digest, editReport["digest"])
	}

	investigation := agenttools.InvocationScope{Owner: true}
	if infos := registry.InfosFor(investigation); toolNamesContain(infos, "edit_workspace") {
		t.Fatal("write tools visible without explicit mutation request")
	}
	if _, err := registry.Execute(
		agenttools.WithInvocationScope(context.Background(), investigation),
		"edit_workspace",
		json.RawMessage(`{"path":"service/router.go","edits":[{"old_text":"return 20","new_text":"return 30"}]}`),
	); err == nil {
		t.Fatal("investigation edit executed")
	}
	data, err := os.ReadFile(filepath.Join(root, "service", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "return 20") {
		t.Fatalf("file=%q", data)
	}

	nonOwner := agenttools.WithInvocationScope(context.Background(), agenttools.InvocationScope{ReadOnly: true})
	if _, err := registry.Execute(nonOwner, "write_workspace", json.RawMessage(`{"path":"x.go","content":"package x\n"}`)); err == nil {
		t.Fatal("non-owner write executed")
	}

	original := string(data)
	if _, err := registry.Execute(ownerWrite, "edit_workspace", json.RawMessage(`{
		"path":"service/router.go",
		"edits":[
			{"old_text":"func RateLimit() int { return 20 }","new_text":"func RateLimit() int { return 40 }"},
			{"old_text":"func RateLimit() int { return 20 }\nfunc Timeout()","new_text":"broken"}
		]
	}`)); err == nil {
		t.Fatal("overlapping edit succeeded")
	}
	afterOverlap, err := os.ReadFile(filepath.Join(root, "service", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterOverlap) != original {
		t.Fatalf("overlap changed file: %q", afterOverlap)
	}

	created, err := registry.Execute(ownerWrite, "write_workspace", json.RawMessage(`{"path":"service/internal/new.go","content":"package internal\n"}`))
	if err != nil || !strings.Contains(created.Content, `"created":true`) {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	overwritten, err := registry.Execute(ownerWrite, "write_workspace", json.RawMessage(`{"path":"service/internal/new.go","content":"package other\n"}`))
	if err != nil || strings.Contains(overwritten.Content, `"created":true`) {
		t.Fatalf("overwrite=%+v err=%v", overwritten, err)
	}

	if _, err := registry.Execute(ownerWrite, "edit_workspace", json.RawMessage(`{"path":"service/router.go","edits":[{"old_text":"return 20","new_text":"return 21"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(ownerWrite, "edit_workspace", json.RawMessage(`{"path":"service/router.go","edits":[{"old_text":"return 21","new_text":"return 22"}]}`)); err != nil {
		t.Fatal(err)
	}

	ranged, err := registry.Execute(ownerWrite, "read_workspace", json.RawMessage(`{"path":"service/router.go","offset":1,"limit":1}`))
	if err != nil || !strings.Contains(ranged.Content, "package service") || !strings.Contains(ranged.Content, `"start_line":1`) {
		t.Fatalf("ranged=%+v err=%v", ranged, err)
	}
	search, err := registry.Execute(ownerWrite, "search_workspace", json.RawMessage(`{
		"query":"func RateLimit\\(\\)",
		"regex":true,
		"glob":"**/*.go",
		"context_lines":1
	}`))
	if err != nil || !strings.Contains(search.Content, "service/router.go") || strings.Contains(search.Content, "notes.md") {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	listed, err := registry.Execute(ownerWrite, "list_workspace", json.RawMessage(`{"glob":"**/*.md"}`))
	if err != nil || !strings.Contains(listed.Content, "service/notes.md") || strings.Contains(listed.Content, "router.go") {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
}

func TestAgentLoopSkipsTruncatedWorkspaceMutations(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "router.go"), "limit := 10\n")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(append(
		agenttools.WorkspaceMutationDefinitions(scope, agenttools.WorkspaceMutationOptions{}),
		agentruntime.SubmitDecisionDefinition(),
	)...)
	if err != nil {
		t.Fatal(err)
	}
	truncated := schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
		"call_edit",
		"edit_workspace",
		`{"path":"router.go","edits":[{"old_text":"10","new_text":"99"}]`,
	)})
	truncated.ResponseMeta = &schema.ResponseMeta{FinishReason: "truncated"}
	model := &responseQualityModel{responses: []*schema.Message{
		truncated,
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"submit",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.95,
				"reply_confidence":0.9,
				"risk":"low",
				"reply_text":"截断调用未执行。",
				"reason":"truncated"
			}`,
		)}),
	}}
	_, _, err = (agentruntime.AgentLoop{
		Model: model, Tools: registry, MaxTurns: 4, SimpleMaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		WorkKind: domain.WorkKindSimpleQuestion,
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_trunc",
			SenderID:  "ou_owner",
			Content:   "请修复 router.go 里的分页上限",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "limit := 10\n" {
		t.Fatalf("truncated edit changed file: %q", data)
	}
}

func toolNamesContain(infos []*schema.ToolInfo, name string) bool {
	for _, info := range infos {
		if info != nil && info.Name == name {
			return true
		}
	}
	return false
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

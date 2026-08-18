package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestWorkspaceMutationToolsOwnerWriteGateAndExactEdits(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "router.go"), "limit := 10\n")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(append(WorkspaceDefinitions(scope), WorkspaceMutationDefinitions(scope, WorkspaceMutationOptions{})...)...)
	if err != nil {
		t.Fatal(err)
	}
	hidden := registry.InfosFor(InvocationScope{Owner: true})
	for _, info := range hidden {
		if info.Name == "edit_workspace" || info.Name == "write_workspace" {
			t.Fatalf("write tool visible without write request: %+v", hidden)
		}
	}
	denied := WithInvocationScope(context.Background(), InvocationScope{Owner: true})
	if _, err := registry.Execute(denied, "edit_workspace", []byte(`{"path":"router.go","edits":[{"old_text":"10","new_text":"20"}]}`)); err == nil {
		t.Fatal("edit executed without WorkspaceWriteAllowed")
	}
	ctx := WithInvocationScope(context.Background(), InvocationScope{Owner: true, WorkspaceWriteAllowed: true})
	edited, err := registry.Execute(ctx, "edit_workspace", []byte(`{"path":"router.go","edits":[{"old_text":"10","new_text":"20"}]}`))
	if err != nil || !strings.Contains(edited.Content, `"digest"`) {
		t.Fatalf("edit=%+v err=%v", edited, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "limit := 20\n" {
		t.Fatalf("file=%q", data)
	}
	written, err := registry.Execute(ctx, "write_workspace", []byte(`{"path":"service/new.go","content":"package service\n"}`))
	if err != nil || !strings.Contains(written.Content, `"created":true`) {
		t.Fatalf("write=%+v err=%v", written, err)
	}
}

func TestWorkspaceMutationToolsHiddenFromNonOwner(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceMutationDefinitions(scope, WorkspaceMutationOptions{})...)
	if err != nil {
		t.Fatal(err)
	}
	infos := registry.InfosFor(InvocationScope{Owner: false, ReadOnly: true, WorkspaceWriteAllowed: true})
	if len(infos) != 0 {
		t.Fatalf("infos=%+v", infos)
	}
	ctx := WithInvocationScope(context.Background(), InvocationScope{Owner: false, ReadOnly: true, WorkspaceWriteAllowed: true})
	if _, err := registry.Execute(ctx, "write_workspace", []byte(`{"path":"x.go","content":"package x\n"}`)); err == nil {
		t.Fatal("non-owner write executed")
	}
}

func TestWorkspaceMutationPlanModeDeniesWrites(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "router.go"), "limit := 10\n")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(WorkspaceMutationDefinitions(scope, WorkspaceMutationOptions{CodingPlanMode: true})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithInvocationScope(context.Background(), InvocationScope{Owner: true, WorkspaceWriteAllowed: true})
	if _, err := registry.Execute(ctx, "edit_workspace", []byte(`{"path":"router.go","edits":[{"old_text":"10","new_text":"20"}]}`)); err == nil {
		t.Fatal("plan mode edit executed")
	}
}

func TestWorkspaceMutationApprovalBlocksUntilConsumed(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, filepath.Join(root, "router.go"), "limit := 10\n")
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	approvals := &fakeShellApprovals{}
	registry, err := NewRegistry(WorkspaceMutationDefinitions(scope, WorkspaceMutationOptions{
		ApprovalRequired: true,
		Approvals:        approvals,
	})...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkItemDedup(
		WithInvocationScope(context.Background(), InvocationScope{Owner: true, WorkspaceWriteAllowed: true}),
		"work-1",
	)
	blocked, err := registry.Execute(ctx, "edit_workspace", []byte(`{"path":"router.go","edits":[{"old_text":"10","new_text":"20"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(blocked.Content), &payload); err != nil || payload["approval_required"] != true {
		t.Fatalf("blocked=%+v", blocked)
	}
	data, err := os.ReadFile(filepath.Join(root, "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "limit := 10\n" {
		t.Fatalf("file changed before approval: %q", data)
	}
}

func TestRegistryInfosOmitWriteToolsUntilAllowed(t *testing.T) {
	registry, err := NewRegistry(Definition{
		Info:               &schema.ToolInfo{Name: "edit_workspace"},
		OwnerOnly:          true,
		WorkspaceWriteOnly: true,
		SideEffect:         true,
		Execute: func(context.Context, json.RawMessage) (Execution, error) {
			return Execution{Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if infos := registry.InfosFor(InvocationScope{Owner: true}); len(infos) != 0 {
		t.Fatalf("infos=%+v", infos)
	}
	if infos := registry.InfosFor(InvocationScope{Owner: true, WorkspaceWriteAllowed: true}); len(infos) != 1 {
		t.Fatalf("infos=%+v", infos)
	}
}

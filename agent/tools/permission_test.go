package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRegistryDeniesToolPermissionBeforeExecute(t *testing.T) {
	executed := false
	registry, err := NewRegistry(Definition{
		Info:       &schema.ToolInfo{Name: "dangerous_tool"},
		Permission: ToolPermissionDeny,
		Risk:       ToolRiskHigh,
		Execute: func(context.Context, json.RawMessage) (Execution, error) {
			executed = true
			return Execution{Content: "should not run"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(context.Background(), "dangerous_tool", []byte(`{}`))
	if err == nil {
		t.Fatal("deny tool executed")
	}
	if executed {
		t.Fatal("denied tool executor was called")
	}
	if !strings.Contains(err.Error(), "tool permission denies execution") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestRegistryGeneratesToolReceipt(t *testing.T) {
	registry, err := NewRegistry(Definition{
		Info:       &schema.ToolInfo{Name: "read_only_tool"},
		Permission: ToolPermissionAllow,
		Risk:       ToolRiskLow,
		Execute: func(context.Context, json.RawMessage) (Execution, error) {
			return Execution{Content: `{"ok":true}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.Execute(context.Background(), "read_only_tool", []byte(`{"path":"router.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if execution.Receipt == nil {
		t.Fatal("missing receipt")
	}
	if execution.Receipt.ToolName != "read_only_tool" || execution.Receipt.ArgumentHash == "" || execution.Receipt.ResultDigest == "" {
		t.Fatalf("receipt=%+v", execution.Receipt)
	}
}

func TestRegistryNonOwnerScopeHidesAndDeniesProtectedTools(t *testing.T) {
	executed := false
	registry, err := NewRegistry(
		Definition{
			Info:             &schema.ToolInfo{Name: "read_workspace"},
			Permission:       ToolPermissionAllow,
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (Execution, error) {
				return Execution{Content: `{"ok":true}`}, nil
			},
		},
		Definition{
			Info:       &schema.ToolInfo{Name: "shell"},
			Permission: ToolPermissionAllow,
			SideEffect: true,
			Execute: func(context.Context, json.RawMessage) (Execution, error) {
				executed = true
				return Execution{Content: "should not run"}, nil
			},
		},
		Definition{
			Info:       &schema.ToolInfo{Name: "search_lark_messages"},
			Permission: ToolPermissionAllow,
			OwnerOnly:  true,
			Execute: func(context.Context, json.RawMessage) (Execution, error) {
				executed = true
				return Execution{Content: "should not run"}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	scope := InvocationScope{Owner: false, ReadOnly: true, ChatID: "oc_current"}
	infos := registry.InfosFor(scope)
	if len(infos) != 1 || infos[0].Name != "read_workspace" {
		t.Fatalf("infos=%+v", infos)
	}
	ctx := WithInvocationScope(context.Background(), scope)
	for _, name := range []string{"shell", "search_lark_messages"} {
		if _, err := registry.Execute(ctx, name, []byte(`{}`)); err == nil {
			t.Fatalf("%s executed for non-owner", name)
		}
	}
	if executed {
		t.Fatal("protected tool executor was called")
	}
}

func TestRegistryNonOwnerScopeConfinesSameChatTool(t *testing.T) {
	executed := false
	registry, err := NewRegistry(Definition{
		Info:             &schema.ToolInfo{Name: "get_lark_context"},
		Permission:       ToolPermissionAllow,
		NonOwnerReadOnly: true,
		SameChatArgument: "chat_id",
		Execute: func(context.Context, json.RawMessage) (Execution, error) {
			executed = true
			return Execution{Content: `{"ok":true}`}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithInvocationScope(context.Background(), InvocationScope{
		Owner:    false,
		ReadOnly: true,
		ChatID:   "oc_current",
	})
	if _, err := registry.Execute(ctx, "get_lark_context", []byte(`{"chat_id":"oc_other"}`)); err == nil {
		t.Fatal("cross-chat context executed for non-owner")
	}
	if executed {
		t.Fatal("cross-chat executor was called")
	}
	if _, err := registry.Execute(ctx, "get_lark_context", []byte(`{"chat_id":"oc_current"}`)); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("same-chat read was not executed")
	}
}

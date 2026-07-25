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

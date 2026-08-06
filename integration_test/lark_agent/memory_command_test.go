package larkagent_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	agentcmd "github.com/liuchong/lark-agent/agent/cmd"
	"github.com/liuchong/lark-agent/agent/memory"
	"github.com/liuchong/lark-agent/agent/storage"
)

func TestLocalMemoryCommandsShareDurableStateWithAgentContext(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	var out, errOut bytes.Buffer
	run := func(args ...string) {
		t.Helper()
		out.Reset()
		errOut.Reset()
		fullArgs := append([]string{"--state", statePath, "memory"}, args...)
		if code := agentcmd.Execute(strings.NewReader(""), &out, &errOut, fullArgs); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, errOut.String())
		}
	}

	run("add", "project", "示例事件服务的生产实现位于 sample-service")
	store, err := storage.OpenInspection(statePath)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.SearchMemories(context.Background(), memory.Query{
		Text:   "示例事件服务",
		Scopes: []string{"global"},
		Limit:  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != "confirmed" {
		t.Fatalf("records=%+v", records)
	}

	run("feedback", records[0].ID, "helpful", "项目定位正确")
	if !strings.Contains(out.String(), `"verdict":"helpful"`) {
		t.Fatalf("feedback output=%s", out.String())
	}

	run("delete", records[0].ID, "--confirm")
	store, err = storage.OpenInspection(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	records, err = store.SearchMemories(context.Background(), memory.Query{
		Text:   "示例事件服务",
		Scopes: []string{"global"},
		Limit:  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("deleted memory entered context=%+v", records)
	}
}

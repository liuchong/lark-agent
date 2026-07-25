package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestExportAgentRunTranscriptJSONL(t *testing.T) {
	store, err := Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	event := domain.NormalizedEvent{MessageID: "om_transcript", Content: "查代码"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "model", "config")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAgentStep(context.Background(), domain.AgentStep{
		RunID:      run.ID,
		Sequence:   1,
		Kind:       "tool",
		ToolCallID: "call_1",
		ToolName:   "search_workspace",
		InputJSON:  `{"query":"pagination"}`,
		OutputJSON: `{"ok":true}`,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	jsonl, err := store.ExportAgentRunTranscript(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(jsonl), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl=%q", jsonl)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["kind"] != "run" || !strings.Contains(lines[1], "search_workspace") {
		t.Fatalf("jsonl=%q", jsonl)
	}
}

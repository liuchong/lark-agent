package larkagent_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/storage"
)

func TestQueueCancellationAndCrossSessionApprovalRoundTrip(t *testing.T) {
	bin := buildAgentBinary(t)
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{
		"om_integration_cancel",
		"om_integration_keep",
		"om_integration_approval",
	} {
		if _, err := first.EnqueueEvent(domain.NormalizedEvent{
			MessageID: messageID,
			Content:   messageID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64, len(items))
	for _, item := range items {
		ids[item.Event.MessageID] = item.ID
	}
	approvalID, err := first.RequestShellApproval(
		context.Background(),
		domain.DedupKey(domain.NormalizedEvent{
			MessageID: "om_integration_approval",
			Content:   "om_integration_approval",
		}),
		"gofmt -w .",
		".",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	ready, err := current.MarkCurrentSessionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAgent(
		t,
		bin,
		"--state", statePath,
		"queue", "cancel",
		"--all-interrupted",
		"--keep-work-id", fmt.Sprint(ids["om_integration_keep"]),
		"--keep-work-id", fmt.Sprint(ids["om_integration_approval"]),
		"--reason", "integration audit",
	)
	if code != 0 || !strings.Contains(stdout, `"changed":1`) {
		t.Fatalf("cancel exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	code, stdout, stderr = runAgent(
		t,
		bin,
		"--state", statePath,
		"approval", "approve", fmt.Sprint(approvalID),
	)
	if code != 0 || !strings.Contains(stdout, `"action":"approve"`) {
		t.Fatalf("approve exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	items, err = current.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		switch item.Event.MessageID {
		case "om_integration_cancel":
			if item.Status != domain.StatusCancelled {
				t.Fatalf("cancelled item=%+v", item)
			}
		case "om_integration_keep":
			if item.Status != domain.StatusInterrupted {
				t.Fatalf("kept item=%+v", item)
			}
		case "om_integration_approval":
			if item.Status != domain.StatusReceived || item.SessionID != ready.ID {
				t.Fatalf("approved item=%+v ready=%+v", item, ready)
			}
		}
	}
}

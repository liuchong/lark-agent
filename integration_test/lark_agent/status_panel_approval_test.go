package larkagent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/storage"
)

func TestApprovalStatusCLIReturnsBoundedPublicPendingActions(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_panel_pending", Content: "需要审批"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("SECRET_DRAFT_BODY", 8000)
	if _, err := store.RequestShellApproval(
		context.Background(),
		domain.DedupKey(event),
		secret,
		t.TempDir(),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	bin := buildAgentBinary(t)
	code, stdout, stderr := runAgent(t, bin, "--state", statePath, "approval", "status")
	if code != 0 {
		t.Fatalf("approval status exit=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	if strings.Contains(stdout, "SECRET_DRAFT_BODY") || strings.Contains(stdout, "request_json") {
		t.Fatal("approval status leaked request bodies")
	}
	if len(stdout) > 16*1024 {
		t.Fatalf("approval status output too large: %d", len(stdout))
	}

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Counts  map[string]int `json:"counts"`
			Total   int            `json:"total"`
			Actions []struct {
				ID         int64  `json:"id"`
				WorkItemID int64  `json:"work_item_id"`
				Kind       string `json:"kind"`
				Status     string `json:"status"`
			} `json:"actions"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("parse approval status: %v\n%s", err, stdout)
	}
	if !envelope.OK || envelope.Data.Total != 1 || envelope.Data.Counts["awaiting_approval"] != 1 {
		t.Fatalf("envelope=%+v", envelope)
	}
	if len(envelope.Data.Actions) != 1 ||
		envelope.Data.Actions[0].ID <= 0 ||
		envelope.Data.Actions[0].Kind != "shell" ||
		envelope.Data.Actions[0].Status != string(domain.ActionAwaitingApproval) {
		t.Fatalf("actions=%+v", envelope.Data.Actions)
	}
}

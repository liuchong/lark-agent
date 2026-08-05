package larkagent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/app"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
)

type captureReplyHandler struct {
	calls    int
	decision domain.Decision
}

func (h *captureReplyHandler) Handle(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
) (reply.Result, error) {
	h.calls++
	h.decision = decision
	return reply.Result{Action: domain.Action{Status: domain.ActionCompleted}}, nil
}

func TestOwnerGroupStatusFastPathDoesNotDiscloseTaskSummary(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.EnqueueEvent(domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt_group_status_privacy",
		MessageID: "om_group_status_privacy",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_owner",
		Content:   "@Assistant status",
		Mentions:  []domain.Mention{{Key: "@Assistant", OpenID: "ou_bot", Name: "Assistant"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	replier := &captureReplyHandler{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{
			OwnerOpenID:         "ou_owner",
			AssistantOpenIDs:    []string{"ou_bot"},
			AssistantReplyScope: domain.ReplyScopeAllGroups,
			Mode:                domain.ModeAuto,
			StatusText: func() string {
				return "需要你处理 9 条；正在执行或自动等待 8 条。发送 `/tasks` 查看详情。"
			},
		}),
		app.WithReplyHandler(replier),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || replier.calls != 1 {
		t.Fatalf("result=%+v replier=%+v", result, replier)
	}
	if result.Decision.Kind != domain.DecisionReply ||
		result.Decision.Reason != "owner_group_control_redirect" ||
		!strings.Contains(result.Decision.ReplyText, "私聊") ||
		strings.Contains(result.Decision.ReplyText, "需要你处理") ||
		strings.Contains(result.Decision.ReplyText, "/tasks") {
		t.Fatalf("decision leaked private task state: %+v", result.Decision)
	}
}

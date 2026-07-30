package larkagent_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/app"
	"github.com/liuchong/lark-agent/agent/control"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
)

type ownerControlReplyRecorder struct {
	texts []string
}

func (r *ownerControlReplyRecorder) Handle(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
) (reply.Result, error) {
	r.texts = append(r.texts, decision.ReplyText)
	return reply.Result{Action: domain.Action{
		Status: domain.ActionCompleted,
	}}, nil
}

func TestOwnerPrivateControlCommandsCompleteWithoutModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	old, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_old_target", Content: "历史中断任务",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := old.ListWorkItems()
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	target := items[0]
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	agentRouter := router.New(router.Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		Mode:             domain.ModeAuto,
	})
	recorder := &ownerControlReplyRecorder{}
	handler := control.New(store, control.Config{
		OwnerName: "测试负责人",
		Language:  "zh-CN",
		Version:   "test-version",
	})
	daemon := app.NewDaemon(
		store,
		agentRouter,
		app.WithControlHandler(handler),
		app.WithReplyHandler(recorder),
	)
	process := func(event domain.NormalizedEvent) {
		t.Helper()
		if _, err := store.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !result.Processed {
			t.Fatalf("event %s was not processed", event.MessageID)
		}
	}

	for _, event := range []domain.NormalizedEvent{
		{
			MessageID:     "om_help_command",
			ChatID:        "oc_private",
			ChatType:      "p2p",
			ChatPartnerID: "ou_bot",
			SenderID:      "ou_owner",
			Content:       "/help",
		},
		{
			MessageID:     "om_ack_command",
			ChatID:        "oc_private",
			ChatType:      "p2p",
			ChatPartnerID: "ou_bot",
			SenderID:      "ou_owner",
			Content:       "/task acknowledge " + int64Text(target.ID) + " 已人工核对",
		},
		{
			MessageID:     "om_tasks_command",
			ChatID:        "oc_private",
			ChatType:      "p2p",
			ChatPartnerID: "ou_bot",
			SenderID:      "ou_owner",
			Content:       "/tasks",
		},
	} {
		process(event)
	}
	process(domain.NormalizedEvent{
		MessageID:     "om_resume_command",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "/task resume " + int64Text(target.ID) + " confirm",
	})
	if err := store.MarkDeadLetter(target.ID, "恢复后再次失败"); err != nil {
		t.Fatal(err)
	}
	process(domain.NormalizedEvent{
		MessageID:     "om_tasks_after_new_failure",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "/tasks",
	})
	if len(recorder.texts) != 5 {
		t.Fatalf("texts=%+v", recorder.texts)
	}
	if !strings.Contains(recorder.texts[0], "/tasks") ||
		!strings.Contains(recorder.texts[1], "已确认") ||
		!strings.Contains(recorder.texts[2], "当前没有需要你处理") ||
		!strings.Contains(recorder.texts[3], "已恢复") ||
		!strings.Contains(recorder.texts[4], "#"+int64Text(target.ID)) {
		t.Fatalf("texts=%+v", recorder.texts)
	}
}

func int64Text(value int64) string {
	return strconv.FormatInt(value, 10)
}

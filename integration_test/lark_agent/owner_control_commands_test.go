package larkagent_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/app"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/control"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
)

type ownerControlReplyRecorder struct {
	texts []string
}

type semanticOwnerControlStub struct {
	resolution app.SemanticControlResolution
}

func (s semanticOwnerControlStub) Resolve(
	_ context.Context,
	_ domain.WorkItem,
	_ agentcontext.Bundle,
) (app.SemanticControlResolution, error) {
	return s.resolution, nil
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
		MessageID: "om_old_target",
		Content:   "@_user_1 历史中断任务",
		Mentions: []domain.Mention{{
			Key: "@_user_1", Name: "测试负责人",
		}},
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
	process(domain.NormalizedEvent{
		MessageID:     "om_task_detail_after_new_failure",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "/task " + int64Text(target.ID),
	})
	if len(recorder.texts) != 6 {
		t.Fatalf("texts=%+v", recorder.texts)
	}
	if !strings.Contains(recorder.texts[0], "/tasks") ||
		!strings.Contains(recorder.texts[1], "已确认") ||
		!strings.Contains(recorder.texts[2], "当前没有需要你处理") ||
		!strings.Contains(recorder.texts[3], "已恢复") ||
		!strings.Contains(recorder.texts[4], "#"+int64Text(target.ID)) ||
		!strings.Contains(recorder.texts[4], "@测试负责人 历史中断任务") ||
		strings.Contains(recorder.texts[4], "@_user_") {
		t.Fatalf("texts=%+v", recorder.texts)
	}
	if !strings.Contains(recorder.texts[5], "@测试负责人 历史中断任务") ||
		strings.Contains(recorder.texts[5], "@_user_") {
		t.Fatalf("task detail=%q", recorder.texts[5])
	}
}

func TestOwnerPrivateNaturalLanguageCommandUsesTypedControlHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{
		MessageID:     "om_natural_tasks",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "看看最近需要我处理的任务",
	}); err != nil {
		t.Fatal(err)
	}
	recorder := &ownerControlReplyRecorder{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{
			OwnerOpenID:      "ou_owner",
			AssistantOpenIDs: []string{"ou_bot"},
			Mode:             domain.ModeAuto,
		}),
		app.WithContextBuilder(agentcontext.Builder{}),
		app.WithSemanticControlResolver(semanticOwnerControlStub{
			resolution: app.SemanticControlResolution{
				Kind: app.SemanticControlCommand,
				Command: &domain.OwnerControlCommand{
					Name: domain.OwnerControlTasks,
					View: domain.OwnerTaskViewAction,
				},
			},
		}),
		app.WithControlHandler(control.New(store, control.Config{
			OwnerName: "测试用户",
			Language:  "zh-CN",
			Version:   "test-version",
		})),
		app.WithReplyHandler(recorder),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || len(recorder.texts) != 1 ||
		!strings.Contains(recorder.texts[0], "当前没有需要你处理") {
		t.Fatalf("result=%+v texts=%+v", result, recorder.texts)
	}
}

func TestOwnerPrivateMemoryCommandsUseDurableControlJournal(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorder := &ownerControlReplyRecorder{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{
			OwnerOpenID:      "ou_owner",
			AssistantOpenIDs: []string{"ou_bot"},
			Mode:             domain.ModeAuto,
		}),
		app.WithControlHandler(control.New(store, control.Config{
			OwnerName: "测试负责人",
			Language:  "zh-CN",
		})),
		app.WithReplyHandler(recorder),
	)
	process := func(messageID, content string) {
		t.Helper()
		if _, err := store.EnqueueEvent(domain.NormalizedEvent{
			MessageID:     messageID,
			ChatID:        "oc_private",
			ChatType:      "p2p",
			ChatPartnerID: "ou_bot",
			SenderID:      "ou_owner",
			Content:       content,
		}); err != nil {
			t.Fatal(err)
		}
		result, err := daemon.RunOnce(context.Background())
		if err != nil || !result.Processed {
			t.Fatalf("processed=%v err=%v", result.Processed, err)
		}
	}

	process("om_memory_add", "/memory add project 示例修复已合入测试分支")
	records, err := store.ListMemories(context.Background(), "global", false, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	memoryID := records[0].ID
	process("om_memory_list", "/memory list")
	process("om_memory_feedback", "/memory feedback "+memoryID+" helpful 避免重复调查")
	process("om_memory_delete", "/memory delete "+memoryID+" confirm")

	records, err = store.ListMemories(context.Background(), "global", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("deleted memory remains visible: %+v", records)
	}
	feedback, err := store.ListMemoryFeedback(context.Background(), memoryID, 10)
	if err != nil || len(feedback) != 1 {
		t.Fatalf("feedback=%+v err=%v", feedback, err)
	}
	if len(recorder.texts) != 4 ||
		!strings.Contains(recorder.texts[0], "已保存并确认") ||
		!strings.Contains(recorder.texts[1], memoryID) ||
		!strings.Contains(recorder.texts[2], "helpful") ||
		!strings.Contains(recorder.texts[3], "已删除") {
		t.Fatalf("texts=%+v", recorder.texts)
	}
}

func int64Text(value int64) string {
	return strconv.FormatInt(value, 10)
}

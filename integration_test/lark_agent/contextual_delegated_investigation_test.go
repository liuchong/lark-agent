package larkagent_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/app"
	"github.com/liuchong/lark-agent/agent/control"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/investigation"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
	"github.com/liuchong/lark-agent/agent/tools"
)

type investigationMessenger struct {
	ownerNotices []tools.NotifyRequest
	progress     []tools.ReplyRequest
}

func (m *investigationMessenger) ReplyAsUser(
	context.Context,
	tools.ReplyRequest,
) (tools.ReplyResult, error) {
	panic("investigation progress must not reply as the owner")
}

func (m *investigationMessenger) NotifyOwner(
	_ context.Context,
	req tools.NotifyRequest,
) error {
	m.ownerNotices = append(m.ownerNotices, req)
	return nil
}

func (m *investigationMessenger) ReplyAsBot(
	_ context.Context,
	req tools.ReplyRequest,
) (tools.ReplyResult, error) {
	m.progress = append(m.progress, req)
	return tools.ReplyResult{
		MessageID: "om_progress",
		ChatID:    "oc_rd",
	}, nil
}

func TestDelegatedInvestigationPersistsAndResumesWithoutDuplicateProgress(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	base := store.CurrentSession().StartedAt.Add(time.Second)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_contextual_handoff",
		MessageID: "om_contextual_handoff",
		ChatID:    "oc_rd",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 你看看吧，我电脑断线了",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base,
	})

	plan := domain.DelegatedInvestigation{
		WorkItemID:    item.ID,
		TaskSummary:   "investigate production message editing returning 1408 SampleEventDisabled",
		TaskClass:     domain.TaskClassCoding,
		ContextCutoff: base.Add(3 * time.Minute),
		ContextDigest: "sha256:context",
		ContextMessages: []domain.NormalizedEvent{
			{
				MessageID: "om_context",
				ChatID:    "oc_rd",
				Content:   "生产示例事件返回 1408 SampleEventDisabled",
				Attachments: []domain.Attachment{{
					Type:      "image",
					Key:       "img_context",
					MediaType: "image/png",
					Readable:  true,
					DataURL:   "data:image/png;base64,c2VjcmV0LWJ5dGVz",
				}},
			},
			item.Event,
		},
		Status: domain.InvestigationPendingProgress,
	}
	first, created, err := store.BeginDelegatedInvestigation(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.Status != domain.InvestigationPendingProgress {
		t.Fatalf("first=%+v created=%v", first, created)
	}
	second, created, err := store.BeginDelegatedInvestigation(plan)
	if err != nil {
		t.Fatal(err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("duplicate investigation first=%+v second=%+v created=%v", first, second, created)
	}
	messenger := &investigationMessenger{}
	controller := investigation.New(store, messenger, investigation.Config{
		OwnerName: "测试负责人",
		Language:  "zh-CN",
	})
	resolution := replymatch.Resolution{
		TaskSummary:      plan.TaskSummary,
		TaskClass:        plan.TaskClass,
		ContextCutoff:    plan.ContextCutoff,
		ContextDigest:    plan.ContextDigest,
		RequiresProgress: true,
	}
	if err := controller.Begin(context.Background(), item, resolution); err != nil {
		t.Fatal(err)
	}
	if err := controller.Begin(context.Background(), item, resolution); err != nil {
		t.Fatal(err)
	}
	if len(messenger.ownerNotices) != 1 || len(messenger.progress) != 1 {
		t.Fatalf(
			"owner notices=%d progress=%d",
			len(messenger.ownerNotices),
			len(messenger.progress),
		)
	}
	ownerUUID := messenger.ownerNotices[0].IdempotencyKey
	progressUUID := messenger.progress[0].IdempotencyKey
	if len(ownerUUID) > 50 || len(progressUUID) > 50 {
		t.Fatalf(
			"public UUID limit exceeded: owner=%q progress=%q",
			ownerUUID,
			progressUUID,
		)
	}
	if ownerUUID == progressUUID {
		t.Fatalf("owner notice and progress share public UUID %q", ownerUUID)
	}
	actions, err := store.ListActionAttempts()
	if err != nil {
		t.Fatal(err)
	}
	var internalKeys []string
	for _, action := range actions {
		if action.WorkItemID == item.ID &&
			(action.Kind == "investigation_owner_notice" ||
				action.Kind == "investigation_progress") {
			internalKeys = append(internalKeys, action.IdempotencyKey)
		}
	}
	if len(internalKeys) != 2 ||
		len(internalKeys[0]) <= 50 ||
		len(internalKeys[1]) <= 50 ||
		internalKeys[0] == internalKeys[1] {
		t.Fatalf("internal investigation action keys=%q", internalKeys)
	}
	if got := messenger.progress[0].Text; !strings.Contains(got, "智能助手") ||
		!strings.Contains(got, "已通知测试负责人") ||
		!strings.Contains(got, plan.TaskSummary) {
		t.Fatalf("progress text=%q", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	resumed, ok, err := store.GetDelegatedInvestigation(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || resumed.Status != domain.InvestigationInvestigating ||
		resumed.ContextDigest != plan.ContextDigest ||
		resumed.TaskSummary != plan.TaskSummary ||
		len(resumed.ContextMessages) != 2 {
		t.Fatalf("resumed=%+v ok=%v", resumed, ok)
	}
	restoredImage := resumed.ContextMessages[0].Attachments[0]
	if restoredImage.DataURL != "" ||
		restoredImage.Readable ||
		restoredImage.UnreadableReason != "image_bytes_not_persisted" {
		t.Fatalf("restored image=%+v", restoredImage)
	}
	claimed, err := store.GetWorkItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed.InvestigationActive ||
		claimed.ContextDigest != plan.ContextDigest ||
		len(claimed.ResolvedContext) != 2 {
		t.Fatalf("claimed investigation snapshot=%+v", claimed)
	}
	controller = investigation.New(store, messenger, investigation.Config{
		OwnerName: "测试负责人",
		Language:  "zh-CN",
	})
	if err := controller.Begin(context.Background(), item, resolution); err != nil {
		t.Fatal(err)
	}
	if len(messenger.ownerNotices) != 1 || len(messenger.progress) != 1 {
		t.Fatalf("restart duplicated messages: %+v", messenger)
	}
	controlHandler := control.New(store, control.Config{
		OwnerName: "测试负责人",
		Language:  "zh-CN",
	})
	for _, command := range []domain.OwnerControlCommand{
		{
			Name:       domain.OwnerControlTasks,
			View:       domain.OwnerTaskViewAll,
			Page:       1,
			WorkItemID: item.ID,
		},
		{
			Name:       domain.OwnerControlTask,
			WorkItemID: item.ID,
		},
	} {
		decision, err := controlHandler.Handle(context.Background(), item, command)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"调查主题",
			plan.TaskSummary,
			"调查状态",
			"正在调查",
			"上下文证据",
			"/task " + int64Text(item.ID),
		} {
			if !strings.Contains(decision.ReplyText, want) {
				t.Fatalf("command=%s reply=%q missing=%q", command.Name, decision.ReplyText, want)
			}
		}
	}
	if err := controller.Finalizing(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if err := controller.Complete(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	completed, ok, err := store.GetDelegatedInvestigation(item.ID)
	if err != nil || !ok || completed.Status != domain.InvestigationCompleted {
		t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
	}
}

func TestDelegatedInvestigationTerminalFailurePersistsBlockedClosure(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := store.CurrentSession().StartedAt.Add(time.Second)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_blocked_investigation",
		MessageID: "om_blocked_investigation",
		ChatID:    "oc_rd",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 核查生产示例事件",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base,
	})
	if _, _, err := store.BeginDelegatedInvestigation(domain.DelegatedInvestigation{
		WorkItemID:    item.ID,
		TaskSummary:   "核查生产示例事件",
		TaskClass:     domain.TaskClassCoding,
		ContextCutoff: base.Add(3 * time.Minute),
		ContextDigest: "sha256:blocked-context",
		Status:        domain.InvestigationInvestigating,
	}); err != nil {
		t.Fatal(err)
	}
	controller := investigation.New(store, nil, investigation.Config{
		OwnerName: "测试负责人",
		Language:  "zh-CN",
	})
	runErr := errors.New("model did not submit a terminal decision")
	if err := controller.Block(context.Background(), item, runErr); err != nil {
		t.Fatal(err)
	}
	blocked, ok, err := store.GetDelegatedInvestigation(item.ID)
	if err != nil || !ok {
		t.Fatalf("blocked=%+v ok=%v err=%v", blocked, ok, err)
	}
	if blocked.Status != domain.InvestigationBlocked ||
		blocked.LastError != runErr.Error() {
		t.Fatalf("blocked=%+v", blocked)
	}
}

func TestDaemonDeadLetterBlocksPersistedInvestigationAfterDatabaseReload(
	t *testing.T,
) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := store.CurrentSession().StartedAt.Add(time.Second)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_deadletter_investigation",
		MessageID: "om_deadletter_investigation",
		ChatID:    "oc_rd",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 核查生产示例事件",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base,
	})
	if _, _, err := store.BeginDelegatedInvestigation(
		domain.DelegatedInvestigation{
			WorkItemID:    item.ID,
			TaskSummary:   "核查生产示例事件",
			TaskClass:     domain.TaskClassCoding,
			ContextCutoff: base.Add(3 * time.Minute),
			ContextDigest: "sha256:deadletter-context",
			Status:        domain.InvestigationInvestigating,
		},
	); err != nil {
		t.Fatal(err)
	}
	controller := investigation.New(store, nil, investigation.Config{
		OwnerName: "测试负责人",
		Language:  "zh-CN",
	})
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(nonConvergentDecider{}),
		app.WithInvestigationProgressHandler(controller),
	)
	if _, err := daemon.RunOnce(context.Background()); err == nil {
		t.Fatal("non-convergent daemon run unexpectedly succeeded")
	}
	reloaded, err := store.GetWorkItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.InvestigationActive {
		t.Fatalf("database reload unexpectedly preserved transient state: %+v", reloaded)
	}
	blocked, ok, err := store.GetDelegatedInvestigation(item.ID)
	if err != nil || !ok {
		t.Fatalf("blocked=%+v ok=%v err=%v", blocked, ok, err)
	}
	if blocked.Status != domain.InvestigationBlocked ||
		!strings.Contains(blocked.LastError, "terminal decision") {
		t.Fatalf("blocked=%+v", blocked)
	}
}

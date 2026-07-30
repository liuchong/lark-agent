package larkagent_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/app"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/agent/policy"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/router"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	"github.com/liuchong/lark-agent/agent/storage"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
)

func TestStartupConvergenceLeavesNoInterruptedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"om_safe", "om_uncertain"} {
		if _, err := first.EnqueueEvent(domain.NormalizedEvent{
			MessageID: messageID,
			Content:   "检查当前业务状态",
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	var uncertain domain.WorkItem
	for _, item := range items {
		if item.Event.MessageID == "om_uncertain" {
			uncertain = item
		}
	}
	if _, _, _, err := first.BeginShellAction(
		context.Background(),
		uncertain.DedupKey,
		"go test ./...",
		".",
	); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	if _, err := current.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := current.ConvergeInterruptedWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Resumed != 1 || report.Terminalized != 1 || report.Uncertain != 1 {
		t.Fatalf("report=%+v", report)
	}
	interrupted, _, err := current.RecoverySummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if interrupted != 0 {
		t.Fatalf("interrupted=%d", interrupted)
	}
}

type fixedDelegatedDecision struct{}

func (fixedDelegatedDecision) Decide(
	context.Context,
	agentcontext.Bundle,
) (domain.Decision, error) {
	return domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceDirectMention,
		Confidence: 0.95,
		Risk:       domain.RiskLow,
		Language:   string(agentlocale.LanguageChinese),
		ReplyText:  "我已检查公开实现，当前确认入口会先执行只读权限判断；生产配置值仍未确认。",
	}, nil
}

type capturingReply struct {
	decision domain.Decision
	order    *[]string
}

func (r *capturingReply) Handle(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
) (reply.Result, error) {
	r.decision = decision
	if r.order != nil {
		*r.order = append(*r.order, "sender_reply")
	}
	return reply.Result{Action: domain.Action{Status: domain.ActionCompleted}}, nil
}

type orderedOwnerNotifier struct {
	order          *[]string
	approvalAction domain.Action
}

func (n orderedOwnerNotifier) HandleNotification(
	_ context.Context,
	_ domain.WorkItem,
	_ domain.Decision,
	_ string,
) error {
	*n.order = append(*n.order, "owner_notice")
	return nil
}

func (n *orderedOwnerNotifier) HandleApprovalNotification(
	_ context.Context,
	_ domain.WorkItem,
	_ domain.Decision,
	action domain.Action,
) error {
	n.approvalAction = action
	*n.order = append(*n.order, "approval_notice")
	return nil
}

func TestDelegatedReplyNotifiesNamedOwnerBeforeSenderFacingAssistantReply(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := domain.NormalizedEvent{
		MessageID: "om_identity",
		ChatID:    "oc_group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请检查当前入口",
		Mentions:  []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}},
	}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	var order []string
	captured := &capturingReply{order: &order}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(fixedDelegatedDecision{}),
		app.WithDecisionPresenter(agentlocale.DelegatedPresenter{
			OwnerOpenID: "ou_owner",
			OwnerName:   "测试负责人",
			Preferred:   agentlocale.LanguageChinese,
			Fallback:    agentlocale.LanguageChinese,
		}),
		app.WithReplyHandler(captured),
		app.WithNotificationHandler(orderedOwnerNotifier{order: &order}),
	)
	if _, err := daemon.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(order, ","), "owner_notice,sender_reply"; got != want {
		t.Fatalf("delivery order=%q want %q", got, want)
	}
	for _, want := range []string{"🤖 智能助手：", "已检查", "已将处理结果通知测试负责人"} {
		if !strings.Contains(captured.decision.ReplyText, want) {
			t.Fatalf("reply missing %q: %s", want, captured.decision.ReplyText)
		}
	}
	if strings.Contains(captured.decision.ReplyText, "用户") {
		t.Fatalf("generic user identity leaked: %s", captured.decision.ReplyText)
	}
}

type lowConfidenceDelegatedDecision struct{}

func (lowConfidenceDelegatedDecision) Decide(
	context.Context,
	agentcontext.Bundle,
) (domain.Decision, error) {
	return domain.Decision{
		Kind:        domain.DecisionReply,
		Relevance:   domain.RelevanceDirectMention,
		Confidence:  0.72,
		Risk:        domain.RiskLow,
		Language:    string(agentlocale.LanguageChinese),
		ReplyText:   "我已核对群聊上下文，但还不能确认具体 OpenAI 组织。",
		OwnerAction: "确认是 API key 组织还是企业版组织",
	}, nil
}

func TestLowConfidenceDelegatedReplyPrivatelyReportsExactApprovalAction(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := domain.NormalizedEvent{
		MessageID: "om_low_confidence",
		ChatID:    "oc_group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请确认具体 OpenAI 组织",
		Mentions:  []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}},
	}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	var order []string
	notifier := &orderedOwnerNotifier{order: &order}
	messenger := &privateReplyMessenger{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(lowConfidenceDelegatedDecision{}),
		app.WithReplyHandler(reply.NewController(
			policy.NewReplyGate(policy.Config{
				Mode:               domain.ModeAuto,
				ReplyConfidenceMin: 0.85,
			}, privateReplyThreadState{}),
			messenger,
			store,
		)),
		app.WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.Decision.Kind != domain.DecisionRequestApproval {
		t.Fatalf("result=%+v", result)
	}
	if messenger.userReplies != 0 || notifier.approvalAction.ID == 0 {
		t.Fatalf("messenger=%+v notifier=%+v", messenger, notifier)
	}
	pending, err := store.ListPendingOwnerApprovals(context.Background(), 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Items) != 1 || pending.Items[0].ID != notifier.approvalAction.ID {
		t.Fatalf("pending=%+v notifier=%+v", pending, notifier)
	}
	if got, want := strings.Join(order, ","), "approval_notice"; got != want {
		t.Fatalf("notification order=%q want %q", got, want)
	}
}

type languageRepairModel struct {
	responses []*schema.Message
	inputs    [][]*schema.Message
	index     int
}

func (m *languageRepairModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if m.index >= len(m.responses) {
		return nil, errors.New("unexpected model call")
	}
	response := m.responses[m.index]
	m.index++
	return response, nil
}

func (m *languageRepairModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestChineseLanguageGateRepairsEnglishReplyAndReportsBudgets(t *testing.T) {
	model := &languageRepairModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
			"bad",
			"submit_decision",
			`{"decision":"reply","relevance_confidence":0.95,"reply_confidence":0.9,"risk":"low","reply_text":"This is a direct mention and the context selection is incomplete, so no useful evidence-backed response is possible.","reason":"missing context"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
			"good",
			"submit_decision",
			`{"decision":"reply","relevance_confidence":0.95,"reply_confidence":0.9,"risk":"low","reply_text":"我检查了当前可见上下文，但引用的前置消息不完整，因此没有编造具体结论；已保留消息编号供后续核对。","reason":"上下文不完整"}`,
		)}),
	}}
	registry, err := agenttools.NewRegistry(agentruntime.SubmitDecisionDefinition())
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:             model,
		Tools:             registry,
		MaxTurns:          3,
		MaxContextBytes:   32 * 1024,
		ContextCompaction: 0.80,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{
			OpenID:            "ou_owner",
			Name:              "测试负责人",
			Language:          string(agentlocale.LanguageChinese),
			PreferredLanguage: string(agentlocale.LanguageChinese),
			FallbackLanguage:  string(agentlocale.LanguageChinese),
		},
		Event: domain.NormalizedEvent{
			MessageID: "om_language",
			SenderID:  "ou_owner",
			Content:   "请核对这条消息",
		},
		WorkKind: domain.WorkKindSimpleQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.ReplyText == "" || strings.Contains(decision.ReplyText, "This is a direct mention") {
		t.Fatalf("decision=%+v", decision)
	}
	if !integrationTrajectoryContains(trajectory, "required language zh-CN") {
		t.Fatalf("English draft was not rejected: %+v", trajectory)
	}
	lastInput := model.inputs[len(model.inputs)-1]
	if !integrationMessagesContain(lastInput, "Context budget:") ||
		!integrationMessagesContain(lastInput, "Remaining model turns") ||
		!integrationMessagesContain(lastInput, "Required outward language: zh-CN") {
		t.Fatalf("budget prompt missing: %+v", lastInput)
	}
}

func TestUnansweredDelegatedWorkCannotFinishAsRecord(t *testing.T) {
	groupTarget := domain.NormalizedEvent{
		MessageID: "om_feedback",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 应该是示例视觉层级的问题，群聊之间需要明显分隔线，消息提示也别那么挤",
		Mentions:  []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}},
	}
	cases := map[string]agentcontext.Bundle{
		"group_mention_after_older_owner_participation": {
			Event:    groupTarget,
			WorkKind: domain.WorkKindDirectMention,
			Conversation: []domain.NormalizedEvent{
				{
					MessageID: "om_owner_before_target",
					ChatID:    "oc_group",
					ChatType:  "group",
					SenderID:  "ou_owner",
					Content:   "应该是字体文字之类的问题吧",
				},
				groupTarget,
			},
		},
		"human_private_message": {
			Event: domain.NormalizedEvent{
				MessageID: "om_private_request",
				ChatID:    "oc_private",
				ChatType:  "p2p",
				SenderID:  "ou_teammate",
				Content:   "字体和示例内容密度这两点也需要优化，你怎么看？",
			},
			WorkKind: domain.WorkKindDirectMention,
		},
	}
	for name, bundle := range cases {
		t.Run(name, func(t *testing.T) {
			model := &languageRepairModel{responses: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
					"silent_record",
					"submit_decision",
					`{"decision":"record","relevance_confidence":0.86,"risk":"low","reason":"owner participated earlier in the discussion"}`,
				)}),
				schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
					"useful_reply",
					"submit_decision",
					`{"decision":"reply","relevance_confidence":0.95,"reply_confidence":0.92,"risk":"low","reply_text":"我把这条反馈整理成了三点：字体层级、示例分隔样式和示例内容密度，已作为界面可读性问题通知测试负责人；目前还没有确认具体改版方案。","owner_action":"界面反馈包含示例视觉层级、示例分隔样式和示例内容密度三项，请确认后续设计方案。","reason":"尚未处理的代回复消息需要有效回应"}`,
				)}),
			}}
			registry, err := agenttools.NewRegistry(agentruntime.SubmitDecisionDefinition())
			if err != nil {
				t.Fatal(err)
			}
			bundle.User = agentcontext.UserProfile{
				OpenID:            "ou_owner",
				Name:              "测试负责人",
				Language:          string(agentlocale.LanguageChinese),
				PreferredLanguage: string(agentlocale.LanguageChinese),
				FallbackLanguage:  string(agentlocale.LanguageChinese),
			}
			decision, trajectory, err := (agentruntime.AgentLoop{
				Model:    model,
				Tools:    registry,
				MaxTurns: 3,
			}).Decide(context.Background(), bundle)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind != domain.DecisionReply || model.index != 2 {
				t.Fatalf("decision=%+v model_calls=%d", decision, model.index)
			}
			if !integrationTrajectoryContains(trajectory, "delegated work cannot finish") {
				t.Fatalf("record decision was not rejected: %+v", trajectory)
			}
		})
	}
}

func integrationToolCall(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func integrationTrajectoryContains(messages []*schema.Message, want string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}

func integrationMessagesContain(messages []*schema.Message, want string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}

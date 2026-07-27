package reply

import (
	"context"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/policy"
	"github.com/liuchong/lark-agent/agent/tools"
)

type fakeMessenger struct {
	replies    int
	botReplies int
	notices    int
	text       string
	notice     string
	events     []string
}

func (f *fakeMessenger) ReplyAsUser(_ context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	f.replies++
	f.text = req.Text
	f.events = append(f.events, "reply")
	return tools.ReplyResult{MessageID: "om_reply", ChatID: "oc_1"}, nil
}

func (f *fakeMessenger) ReplyAsBot(_ context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	f.botReplies++
	f.text = req.Text
	f.events = append(f.events, "bot_reply")
	return tools.ReplyResult{MessageID: "om_bot_reply", ChatID: "oc_private"}, nil
}

func (f *fakeMessenger) NotifyOwner(_ context.Context, req tools.NotifyRequest) error {
	f.notices++
	f.notice = req.Text
	f.events = append(f.events, "notify")
	return nil
}

type threadState struct {
	replied bool
}

type approvalStore struct {
	approved  bool
	requested int
	completed int
}

type auditedApprovalStore struct {
	approvalStore
	begun        int
	completed    int
	completedMsg string
}

func (s *auditedApprovalStore) BeginReplyAction(
	context.Context, string, string,
) (int64, string, string, bool, error) {
	s.begun++
	return 17, "message:om_private:reply", "", false, nil
}

func (s *auditedApprovalStore) CompleteReplyAction(
	_ context.Context, _ int64, messageID, _ string,
) error {
	s.completed++
	s.completedMsg = messageID
	return nil
}

func (s *approvalStore) RequestReplyApproval(context.Context, string, string, string, string) (int64, error) {
	s.requested++
	return 7, nil
}

func (s *approvalStore) ConsumeReplyApproval(context.Context, string, string, string, string) (int64, bool, error) {
	return 7, s.approved, nil
}

func (s *approvalStore) CompleteReplyApproval(context.Context, int64, string, string) error {
	s.completed++
	return nil
}

func (s threadState) OwnerAlreadyReplied(context.Context, domain.WorkItem) (bool, error) {
	return s.replied, nil
}

func (s threadState) MessageWithdrawn(context.Context, domain.WorkItem) (bool, error) {
	return false, nil
}

func TestControllerSendsReplyInAutoMode(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	result, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_1"}), domain.Decision{
		Kind:        domain.DecisionReply,
		Confidence:  0.99,
		Risk:        domain.RiskLow,
		Reason:      "direct_mention",
		ReplyText:   "我来跟进",
		OwnerAction: "确认示例状态变更通知契约并同步 示例客户端回调",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || m.replies != 1 || m.notices != 0 {
		t.Fatalf("result=%+v messenger=%+v", result, m)
	}
	if m.text != "🤖我来跟进" {
		t.Fatalf("reply text=%q", m.text)
	}
}

func TestControllerUsesBotIdentityForOwnerAssistantPrivateReply(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	result, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_private", ChatID: "oc_private", ChatType: "p2p", ChatPartnerID: "ou_bot",
	}), domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "现在是 05:40。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || m.botReplies != 1 || m.replies != 0 {
		t.Fatalf("result=%+v messenger=%+v", result, m)
	}
	if m.text != "现在是 05:40。" {
		t.Fatalf("bot reply text=%q", m.text)
	}
}

func TestControllerAuditsNormalBotReplyBeforeCompleting(t *testing.T) {
	m := &fakeMessenger{}
	store := &auditedApprovalStore{}
	controller := NewController(
		policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}),
		m,
		store,
	)
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_private", ChatID: "oc_private", SenderID: "ou_owner",
	})
	result, err := controller.Handle(context.Background(), item, domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		Confidence: 1, Risk: domain.RiskLow, ReplyText: "现在是 08:09。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted ||
		store.begun != 1 || store.completed != 1 || store.completedMsg != "om_bot_reply" ||
		m.botReplies != 1 {
		t.Fatalf("result=%+v store=%+v messenger=%+v", result, store, m)
	}
}

func TestControllerUsesBotIdentityForGroupAssistantRequest(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	result, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_group", ChatID: "oc_group", ChatType: "group",
		Mentions: []domain.Mention{{OpenID: "ou_bot", Name: "Assistant Bot"}},
	}), domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceAssistantRequest,
		Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "现在是 05:40。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || m.botReplies != 1 || m.replies != 0 {
		t.Fatalf("result=%+v messenger=%+v", result, m)
	}
	if m.text != "现在是 05:40。" {
		t.Fatalf("bot reply text=%q", m.text)
	}
}

func TestControllerRejectsOwnerBotReplyWithoutMessageID(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	_, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
		ChatID: "oc_private", ChatType: "p2p",
	}), domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "现在是 05:40。",
	})
	if err == nil || m.botReplies != 0 || m.replies != 0 {
		t.Fatalf("err=%v messenger=%+v", err, m)
	}
}

func TestControllerDoesNotSendWhenOwnerReplied(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{replied: true}), m)
	result, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_1"}), domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCancelled || m.replies != 0 || m.notices != 0 {
		t.Fatalf("result=%+v messenger=%+v", result, m)
	}
}

func TestControllerRejectsEmptyReplyInsteadOfSendingFallback(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	_, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_empty"}), domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
	})
	if err == nil {
		t.Fatal("empty reply was replaced with a hardcoded fallback")
	}
	if m.replies != 0 {
		t.Fatal("empty reply was sent")
	}
}

func TestControllerRendersMentionPlaceholdersBeforeReply(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_mentions",
		Mentions: []domain.Mention{
			{Key: "@_user_1", OpenID: "ou_owner", Name: "Owner"},
			{Key: "@_user_2", OpenID: "ou_peer", Name: "高建"},
		},
	})
	result, err := controller.Handle(context.Background(), item, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "我会同步给 @_user_2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || m.replies != 1 {
		t.Fatalf("result=%+v messenger=%+v", result, m)
	}
	want := `🤖我会同步给 <at user_id="ou_peer">高建</at>`
	if m.text != want {
		t.Fatalf("reply text=%q want=%q", m.text, want)
	}
}

func TestControllerDoesNotDuplicateRobotPrefix(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	_, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_robot"}), domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "🤖我来跟进",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.text != "🤖我来跟进" {
		t.Fatalf("reply text=%q", m.text)
	}
}

func TestControllerRejectsUnmappedMentionPlaceholder(t *testing.T) {
	m := &fakeMessenger{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m)
	_, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_mentions"}), domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "我会同步给 @_user_2",
	})
	if err == nil {
		t.Fatal("unmapped placeholder was sent")
	}
	if m.replies != 0 {
		t.Fatalf("reply sent despite unmapped placeholder: %+v", m)
	}
}

func TestControllerConsumesExactApprovalBeforeReply(t *testing.T) {
	m := &fakeMessenger{}
	approvals := &approvalStore{}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeApproval}, threadState{}), m, approvals)
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_approval"})
	decision := domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "approved draft",
	}
	first, err := controller.Handle(context.Background(), item, decision)
	if err != nil {
		t.Fatal(err)
	}
	if first.Action.Status != domain.ActionAwaitingApproval || approvals.requested != 1 || m.replies != 0 {
		t.Fatalf("first=%+v approvals=%+v messenger=%+v", first, approvals, m)
	}
	approvals.approved = true
	second, err := controller.Handle(context.Background(), item, decision)
	if err != nil {
		t.Fatal(err)
	}
	if second.Action.Status != domain.ActionCompleted || m.replies != 1 || approvals.completed != 1 {
		t.Fatalf("second=%+v approvals=%+v messenger=%+v", second, approvals, m)
	}
}

func TestControllerCompletesReadyApprovalInAutoMode(t *testing.T) {
	m := &fakeMessenger{}
	approvals := &approvalStore{approved: true}
	controller := NewController(policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, threadState{}), m, approvals)
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_ready"})
	decision := domain.Decision{
		Kind:        domain.DecisionReply,
		Confidence:  1,
		Risk:        domain.RiskLow,
		Reason:      "direct_mention",
		ReplyText:   "ready draft",
		OwnerAction: "确认示例状态变更通知契约。",
	}
	result, err := controller.Handle(context.Background(), item, decision)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || m.replies != 1 || approvals.completed != 1 {
		t.Fatalf("result=%+v approvals=%+v messenger=%+v", result, approvals, m)
	}
}

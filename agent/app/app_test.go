package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/policy"
	"github.com/liuchong/lark-agent/agent/poll"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/tools"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type fakeQueue struct {
	item       domain.WorkItem
	ok         bool
	done       bool
	retried    bool
	completed  domain.Decision
	approved   *domain.Decision
	goal       *domain.CodingGoal
	goalUsed   int
	leaseErr   error
	deferred   bool
	deferFor   time.Duration
	deadLetter bool
	deadReason string
}

func (f *fakeQueue) UpdateWorkItemSchedulingClaim(
	_ int64,
	_ string,
	kind domain.WorkKind,
	priority int,
	_ time.Duration,
) error {
	f.item.WorkKind = kind
	f.item.Priority = priority
	return nil
}

func (f *fakeQueue) RefreshLease(int64, string) error  { return nil }
func (f *fakeQueue) ValidateLease(int64, string) error { return f.leaseErr }

func (f *fakeQueue) SaveCodingGoalClaim(goal domain.CodingGoal, _ string) error {
	f.goal = &goal
	return nil
}
func (f *fakeQueue) CodingGoalBudget(int64) (int, int, error) {
	if f.goal == nil {
		return 0, 0, errors.New("goal not saved")
	}
	return f.goalUsed, f.goal.MaxInvestigationTurns, nil
}

func (f *fakeQueue) ReadyApprovedReply(int64) (domain.Decision, bool, error) {
	if f.approved == nil {
		return domain.Decision{}, false, nil
	}
	return *f.approved, true, nil
}

func (f *fakeQueue) ClaimNext(string) (domain.WorkItem, bool, error) {
	return f.item, f.ok, nil
}

func (f *fakeQueue) Complete(_ int64, decision domain.Decision) error {
	f.done = true
	f.completed = decision
	return nil
}

func (f *fakeQueue) MarkRetry(int64, string) error {
	f.retried = true
	return nil
}

func (f *fakeQueue) MarkDeadLetter(_ int64, reason string) error {
	f.deadLetter = true
	f.deadReason = reason
	return nil
}

func (f *fakeQueue) DeferWaitingUserClaim(_ int64, _ string, _ string, delay time.Duration) error {
	f.deferred = true
	f.deferFor = delay
	return nil
}

type fakeReplyResolver struct {
	resolution replymatch.Resolution
	err        error
	calls      int
}

type fakeControlHandler struct {
	called   bool
	decision domain.Decision
	err      error
}

func (h *fakeControlHandler) Handle(
	_ context.Context,
	_ domain.WorkItem,
	_ domain.OwnerControlCommand,
) (domain.Decision, error) {
	h.called = true
	return h.decision, h.err
}

func TestDaemonExecutesOwnerControlBeforeModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:     "om_owner_control",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "/help",
	})}
	handler := &fakeControlHandler{decision: domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 1,
		Risk:       domain.RiskLow,
		Reason:     "owner_control_help",
		ReplyText:  "命令帮助",
	}}
	decider := &fakeDecider{}
	replier := &fakeReplyHandler{status: domain.ActionCompleted}
	daemon := NewDaemon(
		q,
		router.New(router.Config{
			OwnerOpenID:      "ou_owner",
			AssistantOpenIDs: []string{"ou_bot"},
			Mode:             domain.ModeAuto,
		}),
		WithControlHandler(handler),
		WithDecider(decider),
		WithReplyHandler(replier),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || !handler.called || decider.called || !replier.called {
		t.Fatalf(
			"result=%+v handler=%+v decider=%+v replier=%+v",
			result, handler, decider, replier,
		)
	}
	if result.Decision.Relevance != domain.RelevanceOwnerRequest ||
		result.Decision.WorkKind != domain.WorkKindOwnerControl {
		t.Fatalf("route identity was not preserved: %+v", result.Decision)
	}
}

func (r *fakeReplyResolver) Resolve(context.Context, domain.WorkItem) (replymatch.Resolution, error) {
	r.calls++
	return r.resolution, r.err
}

func TestDaemonRunOnceRoutesItem(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_1",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}))
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || !q.done || result.Decision.Relevance != domain.RelevanceDirectMention {
		t.Fatalf("result=%+v queue=%+v", result, q)
	}
}

func TestDaemonSemanticOwnerAnswerSkipsMainReplyModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target",
		ChatID:    "oc_group",
		SenderID:  "ou_sender",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	decider := &fakeDecider{decision: domain.Decision{Kind: domain.DecisionReply}}
	resolver := &fakeReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID:        "om_target",
		Result:                 replymatch.ResultAnswered,
		MatchedOwnerMessageIDs: []string{"om_owner_answer"},
		Confidence:             0.98,
	}}
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(decider),
		WithDelegatedReplyResolver(resolver, 0.85, 30*time.Second),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || decider.called || !q.done ||
		result.Decision.Kind != domain.DecisionIgnore ||
		result.Decision.Reason != "owner_semantically_replied" {
		t.Fatalf("result=%+v queue=%+v resolver=%+v decider=%+v", result, q, resolver, decider)
	}
}

func TestDaemonNoReplyNeededSkipsMainReplyModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:  "om_private_answer",
		ChatID:     "oc_private",
		ChatType:   "p2p",
		SenderID:   "ou_sender",
		SenderType: "user",
		Content:    "有 UI 和客户端",
	})}
	decider := &fakeDecider{decision: domain.Decision{Kind: domain.DecisionReply}}
	resolver := &fakeReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_private_answer",
		Result:          replymatch.ResultNoReplyNeeded,
		Confidence:      0.98,
		Reason:          "the message answers an owner-led question and adds no request",
	}}
	daemon := NewDaemon(
		q,
		router.New(router.Config{
			OwnerOpenID:       "ou_owner",
			Mode:              domain.ModeAuto,
			PrivateReplyScope: domain.PrivateReplyScopeAll,
		}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(decider),
		WithDelegatedReplyResolver(resolver, 0.85, 30*time.Second),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || decider.called || !q.done ||
		result.Decision.Kind != domain.DecisionIgnore ||
		result.Decision.Reason != "delegated_reply_not_needed" {
		t.Fatalf("result=%+v queue=%+v resolver=%+v decider=%+v", result, q, resolver, decider)
	}
}

func TestDaemonAmbiguousOwnerReplyDefersWithoutMainModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target",
		ChatID:    "oc_group",
		SenderID:  "ou_sender",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	decider := &fakeDecider{decision: domain.Decision{Kind: domain.DecisionReply}}
	resolver := &fakeReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultAmbiguous,
		Confidence:      0.7,
		Reason:          "owner discussion is ambiguous",
	}}
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(decider),
		WithDelegatedReplyResolver(resolver, 0.85, 45*time.Second),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || resolver.calls != 1 || decider.called ||
		!q.deferred || q.deferFor != 45*time.Second || q.done {
		t.Fatalf("result=%+v queue=%+v resolver=%+v decider=%+v", result, q, resolver, decider)
	}
}

func TestDaemonWithdrawnDelegatedTargetCancelsWithoutMainModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target",
		ChatID:    "oc_group",
		SenderID:  "ou_sender",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	decider := &fakeDecider{decision: domain.Decision{Kind: domain.DecisionReply}}
	resolver := &fakeReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultWithdrawn,
		Confidence:      1,
		Reason:          "target message was withdrawn",
	}}
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(decider),
		WithDelegatedReplyResolver(resolver, 0.85, 30*time.Second),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decider.called || !q.done || result.Decision.Kind != domain.DecisionIgnore ||
		result.Decision.Reason != "message_withdrawn" {
		t.Fatalf("result=%+v queue=%+v decider=%+v", result, q, decider)
	}
}

func TestDaemonHonorsLongerSemanticRetryDeadline(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target",
		ChatID:    "oc_group",
		SenderID:  "ou_sender",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	resolver := &fakeReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultAmbiguous,
		Confidence:      0,
		Reason:          "target was edited inside the owner wait window",
		RetryAfter:      2 * time.Minute,
	}}
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithDelegatedReplyResolver(resolver, 0.85, 30*time.Second),
	)

	if _, err := daemon.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !q.deferred || q.deferFor != 2*time.Minute {
		t.Fatalf("queue=%+v", q)
	}
}

func TestInheritRouteFieldsPreservesDirectMentionRelevance(t *testing.T) {
	decision := inheritRouteFields(
		domain.Decision{Kind: domain.DecisionReply, Relevance: domain.RelevanceInferred},
		domain.Decision{Kind: domain.DecisionNotify, Relevance: domain.RelevanceDirectMention},
	)
	if decision.Relevance != domain.RelevanceDirectMention {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestInheritRouteFieldsPreservesOwnerRequestRelevance(t *testing.T) {
	decision := inheritRouteFields(
		domain.Decision{Kind: domain.DecisionReply, Relevance: domain.RelevanceInferred},
		domain.Decision{Kind: domain.DecisionNotify, Relevance: domain.RelevanceOwnerRequest},
	)
	if decision.Relevance != domain.RelevanceOwnerRequest {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestDaemonRejectsNotifyOnlyOwnerRequest(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_owner_bot",
		SenderID:  "ou_owner",
		Mentions:  []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
		Content:   "@Lark Agent 帮我查接口",
	})}
	decider := &fakeDecider{decision: domain.Decision{
		Kind:       domain.DecisionNotify,
		Relevance:  domain.RelevanceInferred,
		Confidence: 0.9,
		Risk:       domain.RiskLow,
		Reason:     "owner asked the assistant",
	}}
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", AssistantOpenIDs: []string{"ou_bot"}, Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(decider),
	)
	if _, err := daemon.RunOnce(context.Background()); err == nil {
		t.Fatal("accepted notify-only owner request")
	}
	if !q.retried || q.done {
		t.Fatalf("queue retried=%v done=%v completed=%+v", q.retried, q.done, q.completed)
	}
}

type fakeBuilder struct {
	called bool
	item   domain.WorkItem
}

func (b *fakeBuilder) Build(item domain.WorkItem) (agentcontext.Bundle, error) {
	b.called = true
	b.item = item
	return agentcontext.Bundle{
		Event: item.Event, WorkKind: item.WorkKind, Priority: item.Priority,
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
	}, nil
}

type fakeDecider struct {
	called   bool
	decision domain.Decision
	bundle   agentcontext.Bundle
	err      error
}

func (d *fakeDecider) Decide(_ context.Context, bundle agentcontext.Bundle) (domain.Decision, error) {
	d.called = true
	d.bundle = bundle
	return d.decision, d.err
}

func TestDaemonDeadLettersModelNonConvergenceWithoutRetry(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_non_convergent",
		Content:   "@Owner 看看删号报 10005",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	q.item.ID = 42
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{err: errs.NewInternalError(
			errs.SubtypeModelNonConvergence,
			"model did not submit a terminal decision after 3 attempts",
		)}),
	)
	_, err := daemon.RunOnce(context.Background())
	if err == nil {
		t.Fatal("non-convergent model run unexpectedly succeeded")
	}
	if !q.deadLetter || q.retried ||
		!strings.Contains(q.deadReason, "terminal decision after 3 attempts") {
		t.Fatalf("queue=%+v", q)
	}
}

type fakeReplyHandler struct {
	called           bool
	text             string
	status           domain.ActionStatus
	actionID         int64
	idempotency      string
	events           *[]string
	approvalExpected bool
}

func (h *fakeReplyHandler) Handle(_ context.Context, _ domain.WorkItem, decision domain.Decision) (reply.Result, error) {
	h.called = true
	h.text = decision.ReplyText
	if h.events != nil {
		*h.events = append(*h.events, "reply")
	}
	status := h.status
	if status == "" {
		status = domain.ActionCompleted
	}
	return reply.Result{Action: domain.Action{
		ID:          h.actionID,
		Status:      status,
		Idempotency: h.idempotency,
	}}, nil
}

func (h *fakeReplyHandler) RequiresApproval(domain.Decision) bool {
	return h.approvalExpected || h.status == domain.ActionAwaitingApproval
}

type fakeOwnerActivity struct {
	events *[]string
	begins int
	ends   int
}

func (f *fakeOwnerActivity) Begin(_ context.Context, _ domain.WorkItem) (string, error) {
	f.begins++
	if f.events != nil {
		*f.events = append(*f.events, "working_reaction_on")
	}
	return "reaction_1", nil
}

func (f *fakeOwnerActivity) End(_ context.Context, _ domain.WorkItem, _ string) error {
	f.ends++
	if f.events != nil {
		*f.events = append(*f.events, "working_reaction_off")
	}
	return nil
}

func TestOwnerRequestWorkingReactionWrapsFastPathReply(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_private", ChatID: "oc_private", ChatType: "p2p",
		ChatPartnerID: "ou_bot", SenderID: "ou_owner", Content: "几点了？",
	})}
	var events []string
	activity := &fakeOwnerActivity{events: &events}
	replier := &fakeReplyHandler{events: &events}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID: "ou_owner", AssistantOpenIDs: []string{"ou_bot"},
		Now: func() time.Time {
			return time.Date(2026, 7, 24, 8, 9, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}), WithReplyHandler(replier), WithOwnerActivityHandler(activity))
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || strings.Join(events, ",") != "working_reaction_on,reply,working_reaction_off" ||
		activity.begins != 1 || activity.ends != 1 {
		t.Fatalf("result=%+v events=%v activity=%+v", result, events, activity)
	}
}

func TestDelegatedOwnerMentionDoesNotShowAssistantWorkingReaction(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_mention", ChatID: "oc_group", ChatType: "group",
		SenderID: "ou_other", Content: "@Owner 看一下",
		Mentions: []domain.Mention{{OpenID: "ou_owner", Name: "Owner"}},
	})}
	activity := &fakeOwnerActivity{}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner"}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind: domain.DecisionReply, Confidence: 0.9, Risk: domain.RiskLow, ReplyText: "收到。",
		}}),
		WithReplyHandler(&fakeReplyHandler{}),
		WithOwnerActivityHandler(activity),
	)
	if _, err := daemon.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if activity.begins != 0 || activity.ends != 0 {
		t.Fatalf("activity=%+v", activity)
	}
}

func TestReplyDecisionWithoutHandlerRetriesInsteadOfCompleting(t *testing.T) {
	q := &fakeQueue{}
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_reply"})
	daemon := NewDaemon(q, router.New(router.Config{}))
	_, err := daemon.finishDecision(context.Background(), item, domain.Decision{
		Kind: domain.DecisionReply, Confidence: 1, Risk: domain.RiskLow, ReplyText: "hello",
	})
	if err == nil || !q.retried || q.done {
		t.Fatalf("err=%v queue=%+v", err, q)
	}
}

func TestDaemonPersistsAwaitingApprovalWhenReplyGateDoesNotSend(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_approval",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeApproval}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind:       domain.DecisionReply,
			Confidence: 0.99,
			Risk:       domain.RiskLow,
			ReplyText:  "draft",
		}}),
		WithReplyHandler(&fakeReplyHandler{status: domain.ActionAwaitingApproval}),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Kind != domain.DecisionRequestApproval || q.completed.Kind != domain.DecisionRequestApproval {
		t.Fatalf("result=%+v completed=%+v", result, q.completed)
	}
}

func TestDaemonPersistsExactDraftForRequestApproval(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_exact_approval",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	replier := &fakeReplyHandler{status: domain.ActionAwaitingApproval}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind:        domain.DecisionRequestApproval,
			Confidence:  0.99,
			Risk:        domain.RiskHigh,
			ReplyText:   "我可以在契约确认后接入",
			OwnerAction: "批准这条包含个人承诺的回复",
		}}),
		WithReplyHandler(replier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !replier.called || replier.text != "我可以在契约确认后接入" ||
		result.Decision.Kind != domain.DecisionRequestApproval ||
		q.completed.OwnerAction != "批准这条包含个人承诺的回复" {
		t.Fatalf("result=%+v replier=%+v completed=%+v", result, replier, q.completed)
	}
}

func TestDaemonResumesPersistedApprovedReplyWithoutModel(t *testing.T) {
	approved := domain.Decision{
		Kind:       domain.DecisionReply,
		Mode:       domain.ModeApproval,
		Confidence: 1,
		Risk:       domain.RiskLow,
		ReplyText:  "persisted exact draft",
	}
	q := &fakeQueue{
		ok:       true,
		item:     domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_resume"}),
		approved: &approved,
	}
	builder := &fakeBuilder{}
	decider := &fakeDecider{decision: domain.Decision{Kind: domain.DecisionReply, ReplyText: "different model draft"}}
	replier := &fakeReplyHandler{}
	daemon := NewDaemon(q, router.New(router.Config{Mode: domain.ModeApproval}),
		WithContextBuilder(builder),
		WithDecider(decider),
		WithReplyHandler(replier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if builder.called || decider.called {
		t.Fatalf("approved reply reran model: builder=%v decider=%v", builder.called, decider.called)
	}
	if replier.text != "persisted exact draft" || result.Decision.ReplyText != "persisted exact draft" {
		t.Fatalf("result=%+v replier=%+v", result, replier)
	}
}

func TestDaemonRunOnceUsesModelAndReplyHandler(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_1",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	builder := &fakeBuilder{}
	decider := &fakeDecider{decision: domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceDirectMention,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "我来跟进",
	}}
	replier := &fakeReplyHandler{}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(builder),
		WithDecider(decider),
		WithReplyHandler(replier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || !builder.called || !decider.called || !replier.called {
		t.Fatalf("result=%+v builder=%+v decider=%+v replier=%+v", result, builder, decider, replier)
	}
	if result.Decision.Kind != domain.DecisionReply || replier.text != "我来跟进" {
		t.Fatalf("result=%+v replier=%+v", result, replier)
	}
}

func TestDaemonRequestsApprovalWhenDirectMentionFallsBelowConfiguredConfidence(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_direct",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	builder := &fakeBuilder{}
	decider := &fakeDecider{decision: domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceInferred,
		Confidence: 0.72,
		Risk:       domain.RiskLow,
		ReplyText:  "收到，我先确认后同步。",
	}}
	messenger := &appMessenger{}
	replier := reply.NewController(
		policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto, ReplyConfidenceMin: 0.85}, appThreadState{}),
		messenger,
	)
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(builder),
		WithDecider(decider),
		WithReplyHandler(replier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.Decision.Kind != domain.DecisionRequestApproval || q.completed.Kind != domain.DecisionRequestApproval {
		t.Fatalf("result=%+v completed=%+v", result, q.completed)
	}
	if messenger.replies != 0 {
		t.Fatalf("messenger=%+v", messenger)
	}
}

type appMessenger struct {
	replies int
	text    string
}

type appThreadState struct{}

func (appThreadState) OwnerAlreadyReplied(context.Context, domain.WorkItem) (bool, error) {
	return false, nil
}

func (appThreadState) MessageWithdrawn(context.Context, domain.WorkItem) (bool, error) {
	return false, nil
}

func (m *appMessenger) ReplyAsUser(_ context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	m.replies++
	m.text = req.Text
	return tools.ReplyResult{MessageID: "om_reply", ChatID: "oc_1"}, nil
}

func (m *appMessenger) NotifyOwner(context.Context, tools.NotifyRequest) error {
	return nil
}

type fakePoller struct {
	called bool
	err    error
}

type fakeNotifier struct {
	called         bool
	approvalCalled bool
	decision       domain.Decision
	approvalAction domain.Action
	key            string
	events         *[]string
}

type genericOnlyNotifier struct {
	called bool
}

func (n *genericOnlyNotifier) HandleNotification(
	context.Context,
	domain.WorkItem,
	domain.Decision,
	string,
) error {
	n.called = true
	return nil
}

func (n *fakeNotifier) HandleNotification(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
	key string,
) error {
	n.called = true
	n.decision = decision
	n.key = key
	if n.events != nil {
		*n.events = append(*n.events, "notify")
	}
	return nil
}

func (n *fakeNotifier) HandleApprovalNotification(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
	action domain.Action,
) error {
	n.approvalCalled = true
	n.decision = decision
	n.approvalAction = action
	if n.events != nil {
		*n.events = append(*n.events, "approval_notify")
	}
	return nil
}

func (p *fakePoller) Poll(context.Context) (poll.Result, error) {
	p.called = true
	if p.err != nil {
		return poll.Result{}, p.err
	}
	return poll.Result{Inserted: 1}, nil
}

func TestDaemonPollOnceUsesPoller(t *testing.T) {
	poller := &fakePoller{}
	daemon := NewDaemon(&fakeQueue{}, router.New(router.Config{Mode: domain.ModeAuto}), WithPoller(poller))
	result, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !poller.called || result.Inserted != 1 {
		t.Fatalf("result=%+v poller=%+v", result, poller)
	}
}

func TestDaemonTickProcessesQueueAfterPollError(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_queued",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	poller := &fakePoller{err: errors.New("temporary lark search failure")}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}), WithPoller(poller))
	result, err := daemon.RunTick(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !poller.called || !q.done || !result.Run.Processed || result.PollError == "" {
		t.Fatalf("result=%+v poller=%+v queue=%+v", result, poller, q)
	}
}

func TestRouteWorkKindReachesBuilderAndModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_coding_kind", SenderID: "owner",
		Content:  "@Agent 请基于代码分析这个接口",
		Mentions: []domain.Mention{{OpenID: "assistant", Name: "Agent"}},
	})}
	builder := &fakeBuilder{}
	decider := &fakeDecider{decision: domain.Decision{
		Kind: domain.DecisionReply, Risk: domain.RiskLow, ReplyText: "evidence",
	}}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID: "owner", AssistantOpenIDs: []string{"assistant"}, AssistantNames: []string{"Agent"},
	}), WithContextBuilder(builder), WithDecider(decider), WithReplyHandler(&fakeReplyHandler{}))
	if _, err := daemon.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if builder.item.WorkKind != domain.WorkKindCodingQuestion ||
		decider.bundle.WorkKind != domain.WorkKindCodingQuestion {
		t.Fatalf("builder=%s model=%s", builder.item.WorkKind, decider.bundle.WorkKind)
	}
}

func TestCodingGoalRoutePersistsGoalBeforeModel(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_goal", SenderID: "owner",
		Content:  "@Agent 请后台处理这个代码问题，完成后通知我",
		Mentions: []domain.Mention{{OpenID: "assistant", Name: "Agent"}},
	})}
	q.item.ID = 1
	decider := &fakeDecider{decision: domain.Decision{
		Kind: domain.DecisionReply, Risk: domain.RiskLow, ReplyText: "done",
	}}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID: "owner", AssistantOpenIDs: []string{"assistant"}, AssistantNames: []string{"Agent"},
	}), WithContextBuilder(&fakeBuilder{}), WithDecider(decider),
		WithReplyHandler(&fakeReplyHandler{}), WithCodingGoalMaxTurns(25))
	if _, err := daemon.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if q.goal == nil || q.goal.Status != domain.CodingGoalActive || q.goal.MaxInvestigationTurns != 25 {
		t.Fatalf("goal=%+v", q.goal)
	}
}

func TestLostLeaseCancelsReplySideEffect(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_lost", SenderID: "owner", Content: "@Agent ping",
	})
	item.ID = 1
	item.LeaseBy = "worker:old-token"
	q := &fakeQueue{ok: true, item: item, leaseErr: errors.New("lease lost")}
	replier := &fakeReplyHandler{}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID: "owner", AssistantNames: []string{"Agent"},
	}), WithReplyHandler(replier))
	if _, err := daemon.RunOnce(context.Background()); err == nil {
		t.Fatal("lost lease was allowed to finish")
	}
	if replier.called {
		t.Fatal("lost lease executed reply side effect")
	}
}

func TestExhaustedCodingGoalRepliesAndTerminatesWithoutModel(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_goal_exhausted", SenderID: "owner",
		Content:  "@Agent 请后台处理代码问题，完成后通知我",
		Mentions: []domain.Mention{{OpenID: "assistant", Name: "Agent"}},
	})
	item.ID = 1
	q := &fakeQueue{ok: true, item: item, goalUsed: 25}
	decider := &fakeDecider{}
	replier := &fakeReplyHandler{}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID: "owner", AssistantOpenIDs: []string{"assistant"}, AssistantNames: []string{"Agent"},
	}), WithContextBuilder(&fakeBuilder{}), WithDecider(decider),
		WithReplyHandler(replier), WithCodingGoalMaxTurns(25))
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decider.called || !replier.called || result.Decision.Kind != domain.DecisionReply ||
		!strings.Contains(result.Decision.Reason, "coding_goal_turn_budget_exhausted") {
		t.Fatalf("decider=%v replier=%v result=%+v", decider.called, replier.called, result)
	}
}

func TestDaemonExecutesNotifyDecision(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_notify",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	notifier := &fakeNotifier{}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind:       domain.DecisionNotify,
			Confidence: 0.8,
			Risk:       domain.RiskLow,
			Reason:     "owner attention required",
		}}),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !notifier.called || result.Decision.Kind != domain.DecisionNotify || q.completed.Kind != domain.DecisionNotify {
		t.Fatalf("result=%+v notifier=%+v completed=%+v", result, notifier, q.completed)
	}
}

func TestDaemonNotifiesOwnerBeforeDelegatedReply(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_reply_then_notify",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	var events []string
	replier := &fakeReplyHandler{events: &events}
	notifier := &fakeNotifier{events: &events}
	decision := domain.Decision{
		Kind:        domain.DecisionReply,
		Confidence:  0.99,
		Risk:        domain.RiskLow,
		ReplyText:   "已收到，我会按确认后的契约接入",
		OwnerAction: "确认示例状态变更通知契约并同步 示例客户端回调",
		Reason:      "coordination handoff",
	}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: decision}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != "notify" || events[1] != "reply" {
		t.Fatalf("action order=%v", events)
	}
	if !result.Processed || notifier.decision.OwnerAction != decision.OwnerAction {
		t.Fatalf("result=%+v notifier=%+v", result, notifier)
	}
}

func TestDaemonNotifiesOwnerWithExactActionWhenDelegatedReplyAwaitsApproval(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_low_confidence",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	var events []string
	replier := &fakeReplyHandler{
		status:      domain.ActionAwaitingApproval,
		actionID:    355,
		idempotency: "approval:355",
		events:      &events,
	}
	notifier := &fakeNotifier{events: &events}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind:        domain.DecisionReply,
			Relevance:   domain.RelevanceDirectMention,
			Confidence:  0.72,
			Risk:        domain.RiskLow,
			ReplyText:   "我已核对上下文，但还不能确认具体组织。",
			OwnerAction: "确认具体 OpenAI 组织",
		}}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.Decision.Kind != domain.DecisionRequestApproval {
		t.Fatalf("result=%+v", result)
	}
	if !notifier.approvalCalled || notifier.approvalAction.ID != 355 {
		t.Fatalf("notifier=%+v", notifier)
	}
	if got, want := strings.Join(events, ","), "reply,approval_notify"; got != want {
		t.Fatalf("action order=%q want %q", got, want)
	}
}

func TestDaemonRejectsNonActionableApprovalNotifier(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_non_actionable_approval",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})}
	replier := &fakeReplyHandler{
		status:      domain.ActionAwaitingApproval,
		actionID:    357,
		idempotency: "approval:357",
	}
	notifier := &genericOnlyNotifier{}
	daemon := NewDaemon(q, router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind:       domain.DecisionReply,
			Relevance:  domain.RelevanceDirectMention,
			Confidence: 0.72,
			Risk:       domain.RiskLow,
			ReplyText:  "待审批的精确草稿",
		}}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	if _, err := daemon.RunOnce(context.Background()); err == nil {
		t.Fatal("non-actionable approval notifier was accepted")
	}
	if notifier.called {
		t.Fatal("approval silently degraded to a generic notification")
	}
}

func TestDaemonApprovedDelegatedReplyStillNotifiesBeforeSending(t *testing.T) {
	approved := domain.Decision{
		Kind:        domain.DecisionReply,
		Mode:        domain.ModeApproval,
		Relevance:   domain.RelevanceDirectMention,
		Confidence:  1,
		Risk:        domain.RiskLow,
		ReplyText:   "已批准的精确答复",
		OwnerAction: "核对后续处理",
	}
	q := &fakeQueue{
		ok:       true,
		item:     domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_approved"}),
		approved: &approved,
	}
	var events []string
	replier := &fakeReplyHandler{events: &events, approvalExpected: true}
	notifier := &fakeNotifier{events: &events}
	daemon := NewDaemon(
		q,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeApproval}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.Decision.Kind != domain.DecisionReply {
		t.Fatalf("result=%+v", result)
	}
	if got, want := strings.Join(events, ","), "notify,reply"; got != want {
		t.Fatalf("approved action order=%q want %q", got, want)
	}
}

func TestDaemonDoesNotSendPostReplyNoticeForOwnerRequest(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_owner_private", ChatID: "oc_private", ChatType: "p2p",
		SenderID: "ou_owner", ChatPartnerID: "ou_bot",
	})}
	replier := &fakeReplyHandler{}
	notifier := &fakeNotifier{}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID: "ou_owner", AssistantOpenIDs: []string{"ou_bot"}, Mode: domain.ModeAuto,
	}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
			Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "现在是 05:40。",
		}}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || !replier.called || notifier.called || q.completed.Kind != domain.DecisionReply {
		t.Fatalf("result=%+v replier=%+v notifier=%+v completed=%+v", result, replier, notifier, q.completed)
	}
}

func TestDaemonDoesNotSendPostReplyNoticeForAssistantRequest(t *testing.T) {
	q := &fakeQueue{ok: true, item: domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_group_assistant", ChatID: "oc_group", ChatType: "group",
		SenderID: "ou_owner",
		Mentions: []domain.Mention{{OpenID: "ou_bot", Name: "Assistant Bot"}},
	})}
	replier := &fakeReplyHandler{}
	notifier := &fakeNotifier{}
	daemon := NewDaemon(q, router.New(router.Config{
		OwnerOpenID:         "ou_owner",
		AssistantOpenIDs:    []string{"ou_bot"},
		AssistantReplyScope: domain.ReplyScopeAllGroups,
		Mode:                domain.ModeAuto,
	}),
		WithContextBuilder(&fakeBuilder{}),
		WithDecider(&fakeDecider{decision: domain.Decision{
			Kind: domain.DecisionReply, Relevance: domain.RelevanceAssistantRequest,
			Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "可以。",
		}}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || !replier.called || notifier.called || q.completed.Kind != domain.DecisionReply {
		t.Fatalf("result=%+v replier=%+v notifier=%+v completed=%+v", result, replier, notifier, q.completed)
	}
}

type fakePostReplyQueue struct {
	*fakeQueue
	pending       domain.Decision
	actionID      int64
	key           string
	found         bool
	completed     bool
	completedText string
}

func (q *fakePostReplyQueue) ReadyPostReplyNotification(int64) (int64, string, domain.Decision, bool, error) {
	return q.actionID, q.key, q.pending, q.found, nil
}

func (q *fakePostReplyQueue) BeginPostReplyNotification(
	context.Context,
	string,
	domain.Decision,
) (int64, string, bool, error) {
	return q.actionID, q.key, q.completed, nil
}

func (q *fakePostReplyQueue) CompletePostReplyNotification(_ context.Context, _ int64, errorText string) error {
	q.completedText = errorText
	q.found = false
	return nil
}

func TestDaemonCompletesWorkForCompletedPostReplyNotification(t *testing.T) {
	decision := domain.Decision{
		Kind:        domain.DecisionReply,
		ReplyText:   "persisted group reply",
		OwnerAction: "persisted owner follow-up",
	}
	q := &fakePostReplyQueue{
		fakeQueue: &fakeQueue{
			ok:   true,
			item: domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_notice_done"}),
		},
		pending:   decision,
		actionID:  92,
		key:       "owner-notification:done",
		found:     true,
		completed: true,
	}
	replier := &fakeReplyHandler{}
	notifier := &fakeNotifier{}
	daemon := NewDaemon(q, router.New(router.Config{Mode: domain.ModeAuto}),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if replier.called || notifier.called || !result.Processed || q.completed == false {
		t.Fatalf("result=%+v replier=%+v notifier=%+v queue=%+v", result, replier, notifier, q)
	}
}

func TestDaemonResumesPostReplyNotificationWithoutModelOrReply(t *testing.T) {
	decision := domain.Decision{
		Kind:        domain.DecisionReply,
		ReplyText:   "persisted group reply",
		OwnerAction: "persisted owner follow-up",
	}
	q := &fakePostReplyQueue{
		fakeQueue: &fakeQueue{
			ok:   true,
			item: domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_notice_resume"}),
		},
		pending:  decision,
		actionID: 91,
		key:      "owner-notification:key",
		found:    true,
	}
	builder := &fakeBuilder{}
	decider := &fakeDecider{}
	replier := &fakeReplyHandler{}
	notifier := &fakeNotifier{}
	daemon := NewDaemon(q, router.New(router.Config{Mode: domain.ModeAuto}),
		WithContextBuilder(builder),
		WithDecider(decider),
		WithReplyHandler(replier),
		WithNotificationHandler(notifier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if builder.called || decider.called || replier.called {
		t.Fatalf("resumed notification reran work: builder=%v decider=%v replier=%v", builder.called, decider.called, replier.called)
	}
	if !notifier.called || notifier.key != q.key || q.completedText != "" ||
		!result.Processed || result.Decision.OwnerAction != decision.OwnerAction {
		t.Fatalf("result=%+v notifier=%+v queue=%+v", result, notifier, q)
	}
}

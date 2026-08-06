package larkagent_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/app"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type convergenceContextBuilder struct{}

func (convergenceContextBuilder) Build(item domain.WorkItem) (agentcontext.Bundle, error) {
	return agentcontext.Bundle{
		Event:    item.Event,
		WorkKind: item.WorkKind,
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
	}, nil
}

type nonConvergentDecider struct{}

func (nonConvergentDecider) Decide(context.Context, agentcontext.Bundle) (domain.Decision, error) {
	return domain.Decision{}, errs.NewInternalError(
		errs.SubtypeModelNonConvergence,
		"model did not submit a terminal decision after 3 attempts",
	)
}

type terminalFailureCapture struct {
	calls int
	item  domain.WorkItem
	err   error
}

type alwaysAmbiguousResolver struct{}

type sequenceReplyResolver struct {
	resolutions []replymatch.Resolution
	calls       int
}

func (r *sequenceReplyResolver) Resolve(
	context.Context,
	domain.WorkItem,
) (replymatch.Resolution, error) {
	if r.calls >= len(r.resolutions) {
		return replymatch.Resolution{}, fmt.Errorf("unexpected resolver call %d", r.calls+1)
	}
	result := r.resolutions[r.calls]
	r.calls++
	return result, nil
}

type countingPartialDecider struct {
	calls int
}

type countingDecisionDecider struct {
	calls    int
	decision domain.Decision
}

func (d *countingDecisionDecider) Decide(
	context.Context,
	agentcontext.Bundle,
) (domain.Decision, error) {
	d.calls++
	return d.decision, nil
}

func (d *countingPartialDecider) Decide(
	context.Context,
	agentcontext.Bundle,
) (domain.Decision, error) {
	d.calls++
	return domain.Decision{
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceVerified,
		ReplyOutcome:   domain.ReplyOutcomePartial,
		ReplyText:      "已确认入口存在；生产配置仍未知。",
		Progress: domain.DecisionProgress{
			CompletedChecks: []string{"读取生产入口"},
			InitialFinding:  "入口存在",
			Unknowns:        []string{"生产配置"},
			NextStep:        "核对部署配置",
		},
	}, nil
}

type completedReplyHandler struct {
	calls int
}

func (h *completedReplyHandler) Handle(
	context.Context,
	domain.WorkItem,
	domain.Decision,
) (reply.Result, error) {
	h.calls++
	return reply.Result{Action: domain.Action{Status: domain.ActionCompleted}}, nil
}

type noOpInvestigationProgress struct{}

func (noOpInvestigationProgress) Begin(
	context.Context,
	domain.WorkItem,
	replymatch.Resolution,
) error {
	return nil
}
func (noOpInvestigationProgress) Finalizing(context.Context, domain.WorkItem) error {
	return nil
}
func (noOpInvestigationProgress) Complete(context.Context, domain.WorkItem) error {
	return nil
}
func (noOpInvestigationProgress) Block(context.Context, domain.WorkItem, error) error {
	return nil
}

func (alwaysAmbiguousResolver) Resolve(
	context.Context,
	domain.WorkItem,
) (replymatch.Resolution, error) {
	return replymatch.Resolution{
		Result:     replymatch.ResultAmbiguous,
		Confidence: 0,
		Reason:     "semantic context remained incomplete",
	}, nil
}

func (c *terminalFailureCapture) HandleTerminalFailure(
	_ context.Context,
	item domain.WorkItem,
	err error,
) error {
	c.calls++
	c.item = item
	c.err = err
	return nil
}

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

func TestModelNonConvergenceMovesLeasedWorkDirectlyToDeadLetter(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_integration_non_convergence",
		SenderID:  "ou_teammate",
		Content:   "@Owner 看看删号报 10005",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := &terminalFailureCapture{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(nonConvergentDecider{}),
		app.WithTerminalFailureHandler(terminal),
	)
	if _, err := daemon.RunOnce(context.Background()); err == nil {
		t.Fatal("non-convergent model run unexpectedly succeeded")
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusDeadLetter ||
		items[0].RetryCount != 1 {
		t.Fatalf("items=%+v", items)
	}
	if terminal.calls != 1 || terminal.item.ID != items[0].ID ||
		terminal.err == nil {
		t.Fatalf("terminal failure capture=%+v", terminal)
	}
}

func TestInferredGroupWorkCannotPromoteToSenderFacingReply(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_non_owner_without_owner_mention",
		ChatID:    "oc_tendo_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "等语音回复好了，Tendo 的任务应该就都有了",
		Mentions:  []domain.Mention{{OpenID: "ou_someone_else"}},
	}); err != nil {
		t.Fatal(err)
	}
	decider := &countingPartialDecider{}
	replier := &completedReplyHandler{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(decider),
		app.WithReplyHandler(replier),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.Decision.Kind != domain.DecisionRecord {
		t.Fatalf("result=%+v", result)
	}
	if decider.calls != 1 {
		t.Fatalf("model calls=%d want=1", decider.calls)
	}
	if replier.calls != 0 {
		t.Fatalf("sender-facing reply calls=%d want=0", replier.calls)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusCompleted {
		t.Fatalf("items=%+v", items)
	}
}

func TestInferredGroupWorkCannotCreateSenderFacingApproval(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_non_owner_approval_without_owner_mention",
		ChatID:    "oc_tendo_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "等语音回复好了，Tendo 的任务应该就都有了",
		Mentions:  []domain.Mention{{OpenID: "ou_someone_else"}},
	}); err != nil {
		t.Fatal(err)
	}
	decider := &countingDecisionDecider{decision: domain.Decision{
		Kind:         domain.DecisionRequestApproval,
		Risk:         domain.RiskHigh,
		ReplyOutcome: domain.ReplyOutcomeComplete,
		ReplyText:    "需要审批的群回复",
		OwnerAction:  "批准群回复",
	}}
	replier := &completedReplyHandler{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(decider),
		app.WithReplyHandler(replier),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed || result.Decision.Kind != domain.DecisionRecord ||
		decider.calls != 1 || replier.calls != 0 {
		t.Fatalf("result=%+v modelCalls=%d replyCalls=%d",
			result, decider.calls, replier.calls)
	}
	actions, err := store.ListActionAttempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Fatalf("sender-facing approval actions=%+v", actions)
	}
}

func TestAmbiguousDelegatedContextConvergesAtRetryCeiling(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	store.ConfigureOwnerReplyRecovery(1)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_ambiguous_ceiling",
		MessageID: "om_ambiguous_ceiling",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	terminal := &terminalFailureCapture{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithDelegatedReplyResolver(alwaysAmbiguousResolver{}, 0.85, time.Minute),
		app.WithTerminalFailureHandler(terminal),
	)

	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetWorkItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed ||
		current.Status != domain.StatusDeadLetter ||
		!current.NextAttemptAt.IsZero() {
		t.Fatalf("result=%+v current=%+v", result, current)
	}
	if terminal.calls != 1 || terminal.item.ID != item.ID || terminal.err == nil {
		t.Fatalf("terminal failure capture=%+v", terminal)
	}
	required, err := store.ListRequiredOwnerResolutionNotifications(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 ||
		required[0].ID == 0 ||
		required[0].WorkItemID != item.ID ||
		required[0].Reason != "owner_reply_ambiguous" {
		t.Fatalf("required owner resolutions=%+v", required)
	}
}

func TestProviderRetryCeilingDoesNotConsumeSemanticRetryBudget(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)

	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_independent_semantic_budget",
		MessageID: "om_independent_semantic_budget",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文确认",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	claimed, ok, err := store.ClaimNext("semantic-budget-worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v item=%+v err=%v", ok, claimed, err)
	}
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		"semantic context remains ambiguous",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetWorkItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusWaitingUser || current.RetryCount != 0 {
		t.Fatalf("current=%+v", current)
	}
}

func TestHeldReplyCandidateRechecksContextWithoutRerunningModel(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureOwnerReplyRecovery(3)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_held_candidate",
		MessageID: "om_held_candidate",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请核对生产入口和配置",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	resolver := &sequenceReplyResolver{resolutions: []replymatch.Resolution{
		{
			TargetMessageID:          item.Event.MessageID,
			Result:                   replymatch.ResultUnanswered,
			Confidence:               0.99,
			Reason:                   "unanswered",
			TaskSummary:              "核对生产入口和配置",
			TaskClass:                domain.TaskClassInvestigation,
			ClassificationConfidence: 0.99,
			RequiresProgress:         true,
		},
		{
			TargetMessageID: item.Event.MessageID,
			Result:          replymatch.ResultAmbiguous,
			Confidence:      0.6,
			Reason:          "owner discussion remains ambiguous",
		},
		{
			TargetMessageID: item.Event.MessageID,
			Result:          replymatch.ResultUnanswered,
			Confidence:      0.99,
			Reason:          "still unanswered",
		},
	}}
	decider := &countingPartialDecider{}
	replier := &completedReplyHandler{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(decider),
		app.WithReplyHandler(replier),
		app.WithDelegatedReplyResolver(resolver, 0.85, time.Nanosecond),
		app.WithInvestigationProgressHandler(noOpInvestigationProgress{}),
	)

	first, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Processed || decider.calls != 1 || replier.calls != 0 {
		t.Fatalf("first=%+v modelCalls=%d replyCalls=%d", first, decider.calls, replier.calls)
	}
	candidate, found, err := store.ReadyWorkReplyCandidate(item.ID)
	if err != nil || !found || candidate.Status != domain.ReplyCandidateHeld {
		t.Fatalf("candidate=%+v found=%v err=%v", candidate, found, err)
	}
	time.Sleep(time.Millisecond)
	second, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Processed || decider.calls != 1 || replier.calls != 1 ||
		second.Decision.ReplyText != candidate.Decision.ReplyText {
		t.Fatalf("second=%+v modelCalls=%d replyCalls=%d", second, decider.calls, replier.calls)
	}
	if _, found, err := store.ReadyWorkReplyCandidate(item.ID); err != nil || found {
		t.Fatalf("consumed candidate found=%v err=%v", found, err)
	}
}

func TestRestartDoesNotResumeDelegatedContextOrSendOldCandidate(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_cross_session_candidate",
		MessageID: "om_cross_session_candidate",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 这个问题修复后改下状态哈",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: first.CurrentSession().StartedAt.Add(time.Second),
	}
	if _, err := first.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := first.ClaimNext("old-worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v item=%+v err=%v", ok, claimed, err)
	}
	_, created, err := first.BeginDelegatedInvestigation(domain.DelegatedInvestigation{
		WorkItemID:    claimed.ID,
		TaskSummary:   "检查不相关的示例能力接口",
		TaskClass:     domain.TaskClassCoding,
		ContextCutoff: event.CreatedAt.Add(time.Minute),
		ContextDigest: "sha256:old-wrong-context",
		ContextMessages: []domain.NormalizedEvent{{
			MessageID: "om_unrelated",
			Content:   "示例能力接口",
		}},
	})
	if err != nil || !created {
		t.Fatalf("begin created=%v err=%v", created, err)
	}
	if err := first.SaveWorkReplyCandidate(claimed.ID, claimed.LeaseBy, domain.Decision{
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceVerified,
		ReplyOutcome:   domain.ReplyOutcomeComplete,
		ReplyText:      "示例能力相关客户端接口已暴露。",
	}); err != nil {
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
	if _, err := current.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := current.ConvergeInterruptedWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Resumed != 0 || report.WaitingOwner != 1 {
		t.Fatalf("cross-session delegated work was automatically resumed: %+v", report)
	}
	interrupted, err := current.GetWorkItem(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Status != domain.StatusInterrupted {
		t.Fatalf("status=%s want=%s", interrupted.Status, domain.StatusInterrupted)
	}
	candidate, found, err := current.ReadyWorkReplyCandidate(claimed.ID)
	if err != nil || !found || candidate.Status != domain.ReplyCandidateHeld {
		t.Fatalf("candidate=%+v found=%v err=%v", candidate, found, err)
	}
	replier := &completedReplyHandler{}
	daemon := app.NewDaemon(
		current,
		router.New(router.Config{OwnerOpenID: "ou_owner", Mode: domain.ModeAuto}),
		app.WithContextBuilder(convergenceContextBuilder{}),
		app.WithDecider(&countingPartialDecider{}),
		app.WithReplyHandler(replier),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed || replier.calls != 0 {
		t.Fatalf("result=%+v replyCalls=%d", result, replier.calls)
	}

	resumedInspection, err := current.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID: claimed.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumedInspection.InvestigationHistory) != 1 ||
		resumedInspection.InvestigationHistory[0].TaskSummary != "检查不相关的示例能力接口" {
		t.Fatalf("investigation history=%+v", resumedInspection.InvestigationHistory)
	}
	resumed, ok, err := current.ClaimNext("new-worker")
	if err != nil || !ok {
		t.Fatalf("claim resumed ok=%v item=%+v err=%v", ok, resumed, err)
	}
	if resumed.InvestigationActive || resumed.TaskSummary != "" ||
		resumed.ContextDigest != "" || len(resumed.ResolvedContext) != 0 {
		t.Fatalf("old investigation context was hydrated: %+v", resumed)
	}
	if _, created, err := current.BeginDelegatedInvestigation(domain.DelegatedInvestigation{
		WorkItemID:    resumed.ID,
		TaskSummary:   "查找示例列表刷新问题并更新 Bug 状态",
		TaskClass:     domain.TaskClassCoding,
		ContextCutoff: event.CreatedAt.Add(2 * time.Minute),
		ContextDigest: "sha256:new-current-context",
	}); err != nil || !created {
		t.Fatalf("new investigation created=%v err=%v", created, err)
	}
}

func TestCancelInterruptedDelegatedWorkClosesCandidateAndInvestigation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := domain.NormalizedEvent{
		MessageID: "om_cancel_contextual_work",
		Content:   "@测试负责人 请调查后回复",
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("cancel-context-worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v item=%+v err=%v", ok, item, err)
	}
	if _, created, err := store.BeginDelegatedInvestigation(domain.DelegatedInvestigation{
		WorkItemID:    item.ID,
		TaskSummary:   "调查待取消问题",
		TaskClass:     domain.TaskClassInvestigation,
		ContextCutoff: event.CreatedAt.Add(time.Minute),
		ContextDigest: "sha256:cancel-context",
	}); err != nil || !created {
		t.Fatalf("begin created=%v err=%v", created, err)
	}
	if err := store.SaveWorkReplyCandidate(item.ID, item.LeaseBy, domain.Decision{
		Kind:      domain.DecisionReply,
		ReplyText: "不应发送的旧草稿",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferWaitingUserClaim(
		item.ID,
		item.LeaseBy,
		"awaiting context",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	result, err := store.CancelWork(context.Background(), domain.CancelWorkRequest{
		WorkItemIDs: []int64{item.ID},
		Reason:      "wrong contextual investigation",
	})
	if err != nil || result.Changed != 1 {
		t.Fatalf("cancel result=%+v err=%v", result, err)
	}
	if _, found, err := store.ReadyWorkReplyCandidate(item.ID); err != nil || found {
		t.Fatalf("candidate found=%v err=%v", found, err)
	}
	investigation, found, err := store.GetDelegatedInvestigation(item.ID)
	if err != nil || !found || investigation.Status != domain.InvestigationBlocked {
		t.Fatalf("investigation=%+v found=%v err=%v", investigation, found, err)
	}
}

func TestApprovedCrossSessionReplyArchivesContextAndCancelsCandidate(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{
		MessageID: "om_approved_cross_session",
		Content:   "@测试负责人 请确认后回复",
		CreatedAt: first.CurrentSession().StartedAt.Add(time.Second),
	}
	if _, err := first.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := first.ClaimNext("approved-context-worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v item=%+v err=%v", ok, item, err)
	}
	if _, created, err := first.BeginDelegatedInvestigation(domain.DelegatedInvestigation{
		WorkItemID:    item.ID,
		TaskSummary:   "上一会话的调查主题",
		TaskClass:     domain.TaskClassInvestigation,
		ContextCutoff: event.CreatedAt.Add(time.Minute),
		ContextDigest: "sha256:approved-old-context",
	}); err != nil || !created {
		t.Fatalf("begin created=%v err=%v", created, err)
	}
	decision := domain.Decision{
		Kind:      domain.DecisionReply,
		Relevance: domain.RelevanceDirectMention,
		ReplyText: "Owner 已明确批准的准确草稿",
	}
	if err := first.SaveWorkReplyCandidate(item.ID, item.LeaseBy, decision); err != nil {
		t.Fatal(err)
	}
	actionID, err := first.RequestReplyApproval(
		context.Background(),
		item.DedupKey,
		decision.ReplyText,
		"requires exact approval",
		"",
		decision.Relevance,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.DecideAction(actionID, true); err != nil {
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
	if _, err := current.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := current.ConvergeInterruptedWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Resumed != 1 || report.WaitingOwner != 0 {
		t.Fatalf("report=%+v", report)
	}
	if _, found, err := current.ReadyWorkReplyCandidate(item.ID); err != nil || found {
		t.Fatalf("candidate found=%v err=%v", found, err)
	}
	if _, found, err := current.GetDelegatedInvestigation(item.ID); err != nil || found {
		t.Fatalf("active investigation found=%v err=%v", found, err)
	}
	history, err := current.ListDelegatedInvestigationHistory(context.Background(), item.ID)
	if err != nil || len(history) != 1 ||
		history[0].TaskSummary != "上一会话的调查主题" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	claimed, ok, err := current.ClaimNext("approved-new-worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v item=%+v err=%v", ok, claimed, err)
	}
	if claimed.InvestigationActive || claimed.TaskSummary != "" {
		t.Fatalf("approved work hydrated old context: %+v", claimed)
	}
	approved, found, err := current.ReadyApprovedReply(item.ID)
	if err != nil || !found || approved.ReplyText != decision.ReplyText {
		t.Fatalf("approved=%+v found=%v err=%v", approved, found, err)
	}
}

func TestTerminalOwnerResolutionGenerationsSurviveResumeAndRepeatFailure(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	store.ConfigureOwnerReplyRecovery(1)
	item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_terminal_generations",
		MessageID: "om_terminal_generations",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	terminalize := func(worker, reason string) {
		t.Helper()
		claimed, ok, err := store.ClaimNext(worker)
		if err != nil || !ok || claimed.ID != item.ID {
			t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
		}
		if err := store.DeferWaitingUserClaim(
			claimed.ID,
			claimed.LeaseBy,
			reason,
			time.Minute,
		); err != nil {
			t.Fatal(err)
		}
	}

	terminalize("first-generation", "first terminal generation")
	first, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID == 0 {
		t.Fatalf("first=%+v", first)
	}
	firstActionID, firstKey, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		first[0].ID,
		item.ID,
		"first terminal summary",
	)
	if err != nil || !send {
		t.Fatalf("first action=%d key=%q send=%v err=%v", firstActionID, firstKey, send, err)
	}
	if len(firstKey) > 50 {
		t.Fatalf("first public key is too long: %d %q", len(firstKey), firstKey)
	}
	if err := store.CompleteOwnerResolutionNotification(context.Background(), firstActionID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    item.ID,
		ForceTerminal: true,
	}); err != nil {
		t.Fatal(err)
	}

	terminalize("second-generation", "second terminal generation")
	if err := store.ConvergeOwnerResolutionRequirements(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID == first[0].ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	secondActionID, secondKey, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		second[0].ID,
		item.ID,
		"second terminal summary",
	)
	if err != nil || !send {
		t.Fatalf("second action=%d key=%q send=%v err=%v", secondActionID, secondKey, send, err)
	}
	if len(secondKey) > 50 {
		t.Fatalf("second public key is too long: %d %q", len(secondKey), secondKey)
	}
	if secondActionID == firstActionID || secondKey == firstKey {
		t.Fatalf("reused generation identity: first=(%d,%q) second=(%d,%q)",
			firstActionID, firstKey, secondActionID, secondKey)
	}
}

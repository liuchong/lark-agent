package cmd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/app"
	"github.com/liuchong/lark-agent/agent/config"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/agent/memory"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/storage"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

func TestRootDoesNotExposeCopiedInternalEventBus(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), &bytes.Buffer{})
	if command, _, err := root.Find([]string{"event", "_bus"}); err == nil &&
		command != nil && command.Name() == "_bus" {
		t.Fatal("standalone agent must not copy the official CLI internal event bus")
	}
}

func TestRuntimePolicySnapshotUsesValidatedActiveConfiguration(t *testing.T) {
	cfg := config.Default()
	cfg.Policy.Mode = domain.ModeAuto
	cfg.Assistant.ReplyScope = domain.ReplyScopeAllGroups
	cfg.Policy.ReplyScope = domain.ReplyScopeAllGroups
	cfg.Policy.PrivateReplyScope = domain.PrivateReplyScopeAll
	cfg.Policy.OwnerWait = 3 * time.Minute
	cfg.Policy.OwnerReplyConfidenceMin = 0.85
	cfg.Policy.OwnerReplyRetry = 5 * time.Minute
	cfg.Policy.ReplyConfidenceMin = 0.70
	cfg.Policy.InvestigationProgress = "enabled"

	got := runtimePolicySnapshot(cfg)
	if got.Mode != domain.ModeAuto ||
		got.AssistantReplyScope != domain.ReplyScopeAllGroups ||
		got.DelegatedReplyScope != domain.ReplyScopeAllGroups ||
		got.PrivateReplyScope != domain.PrivateReplyScopeAll ||
		got.OwnerWait != (3*time.Minute).String() ||
		got.OwnerReplyConfidenceMin != 0.85 ||
		got.OwnerReplyRetry != (5*time.Minute).String() ||
		got.ReplyConfidenceMin != 0.70 ||
		got.InvestigationProgress != "enabled" ||
		!got.Authoritative ||
		!got.MustNotInferFromRules {
		t.Fatalf("runtime policy snapshot=%+v", got)
	}
}

type fakeSemanticContextReader struct {
	result serviceim.SemanticReplyContext
	err    error
}

type runtimePolicyCaptureDecider struct {
	bundle agentcontext.Bundle
	prompt string
}

func (d *runtimePolicyCaptureDecider) Decide(
	_ context.Context,
	bundle agentcontext.Bundle,
) (domain.Decision, error) {
	d.bundle = bundle
	d.prompt = agentcontext.AgentUserPrompt(bundle)
	return domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceOwnerRequest,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "当前低风险代回复直接发送阈值是 0.70。",
	}, nil
}

type completedRuntimePolicyReplyHandler struct{}

func (completedRuntimePolicyReplyHandler) Handle(
	context.Context,
	domain.WorkItem,
	domain.Decision,
) (reply.Result, error) {
	return reply.Result{Action: domain.Action{Status: domain.ActionCompleted}}, nil
}

type notCommandSemanticResolver struct{}

func (notCommandSemanticResolver) Resolve(
	context.Context,
	domain.WorkItem,
	agentcontext.Bundle,
) (domain.SemanticControlResolution, error) {
	return domain.SemanticControlResolution{Kind: domain.SemanticControlNotCommand}, nil
}

func (r fakeSemanticContextReader) GetSemanticReplyContext(
	context.Context,
	serviceim.SemanticReplyContextRequest,
) (serviceim.SemanticReplyContext, error) {
	return r.result, r.err
}

type capturingSemanticContextReader struct {
	request serviceim.SemanticReplyContextRequest
	result  serviceim.SemanticReplyContext
}

func (r *capturingSemanticContextReader) GetSemanticReplyContext(
	_ context.Context,
	request serviceim.SemanticReplyContextRequest,
) (serviceim.SemanticReplyContext, error) {
	r.request = request
	return r.result, nil
}

type fakeSemanticReplyStore struct {
	pending  []domain.WorkItem
	recorded []replymatch.Resolution
}

func (s *fakeSemanticReplyStore) ListPendingDelegatedWork(string) ([]domain.WorkItem, error) {
	return append([]domain.WorkItem(nil), s.pending...), nil
}

func (s *fakeSemanticReplyStore) RecordOwnerReplyResolution(
	_ int64,
	resolution replymatch.Resolution,
) error {
	s.recorded = append(s.recorded, resolution)
	return nil
}

type fakeSemanticMatcher struct {
	request replymatch.Request
	result  replymatch.Resolution
	calls   int
}

type fakeFinalReplyResolver struct {
	resolution replymatch.Resolution
	err        error
	calls      int
}

func (r *fakeFinalReplyResolver) Resolve(
	context.Context,
	domain.WorkItem,
) (replymatch.Resolution, error) {
	r.calls++
	return r.resolution, r.err
}

func TestLiveThreadStateUsesOneFinalSemanticReadForWithdrawnAndAnsweredChecks(t *testing.T) {
	resolver := &fakeFinalReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultAnswered,
		Confidence:      0.97,
		Reason:          "owner answered after the draft",
	}}
	state := &liveThreadState{
		resolver:      resolver,
		confidenceMin: 0.85,
		resolutions:   make(map[string]replymatch.Resolution),
	}
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_target"})
	item.ID = 11
	item.WorkKind = domain.WorkKindDirectMention

	withdrawn, err := state.MessageWithdrawn(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	answered, err := state.OwnerAlreadyReplied(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn || !answered || resolver.calls != 1 {
		t.Fatalf(
			"withdrawn=%v answered=%v resolver_calls=%d",
			withdrawn,
			answered,
			resolver.calls,
		)
	}
}

func TestLiveThreadStateTreatsNoReplyNeededAsFinalSuppression(t *testing.T) {
	resolver := &fakeFinalReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultNoReplyNeeded,
		Confidence:      0.98,
		Reason:          "the private message does not require a response",
	}}
	state := &liveThreadState{
		resolver:      resolver,
		confidenceMin: 0.85,
		resolutions:   make(map[string]replymatch.Resolution),
	}
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_target"})
	item.ID = 12
	item.WorkKind = domain.WorkKindDirectMention

	suppressed, err := state.OwnerAlreadyReplied(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if !suppressed || resolver.calls != 1 {
		t.Fatalf("suppressed=%v resolver_calls=%d", suppressed, resolver.calls)
	}
}

func TestLiveThreadStateBlocksFinalSendWhenSemanticStateIsAmbiguous(t *testing.T) {
	resolver := &fakeFinalReplyResolver{resolution: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultAmbiguous,
		Confidence:      0.4,
		Reason:          "late discussion is ambiguous",
	}}
	state := &liveThreadState{
		resolver:      resolver,
		confidenceMin: 0.85,
		resolutions:   make(map[string]replymatch.Resolution),
	}
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_target"})
	item.WorkKind = domain.WorkKindDirectMention
	if _, err := state.MessageWithdrawn(context.Background(), item); err == nil {
		t.Fatal("ambiguous final state allowed send")
	}
}

func (m *fakeSemanticMatcher) Resolve(
	_ context.Context,
	request replymatch.Request,
) (replymatch.Resolution, error) {
	m.calls++
	m.request = request
	return m.result, nil
}

func TestLiveDelegatedReplyResolverUsesLatestLarkContextAndAllPendingTargets(t *testing.T) {
	base := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	target := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target", ChatID: "oc_group", Content: "旧内容", CreatedAt: base,
	})
	target.ID = 7
	other := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_other", ChatID: "oc_group", Content: "另一个问题",
		CreatedAt: base.Add(time.Second),
	})
	store := &fakeSemanticReplyStore{pending: []domain.WorkItem{target, other}}
	matcher := &fakeSemanticMatcher{result: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultUnanswered,
		Confidence:      0.96,
		Reason:          "owner discussion did not answer the exact target",
	}}
	cutoff := base.Add(3 * time.Minute)
	resolver := liveDelegatedReplyResolver{
		contexts: fakeSemanticContextReader{result: serviceim.SemanticReplyContext{
			Messages: []serviceim.Message{
				{
					MessageID: "om_target", ChatID: "oc_group", Content: "编辑后的内容",
					CreateTime: base.Format(time.RFC3339),
				},
				{
					MessageID: "om_owner", ChatID: "oc_group", SenderOpenID: "ou_owner",
					Content: "我在确认另一个事项", CreateTime: base.Add(time.Minute).Format(time.RFC3339),
				},
			},
			ContextCutoff: cutoff,
		}},
		store:   store,
		matcher: matcher,
	}

	result, err := resolver.Resolve(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Result != replymatch.ResultUnanswered || matcher.calls != 1 ||
		len(matcher.request.Pending) != 2 ||
		matcher.request.Target.Event.Content != "编辑后的内容" ||
		len(store.recorded) != 1 ||
		!store.recorded[0].ContextCutoff.Equal(cutoff) {
		t.Fatalf(
			"result=%+v matcher=%+v request=%+v recorded=%+v",
			result,
			matcher,
			matcher.request,
			store.recorded,
		)
	}
}

func TestLiveDelegatedReplyResolverIncludesPreTargetConversationDirection(t *testing.T) {
	base := time.Date(2026, 7, 29, 4, 23, 15, 0, time.UTC)
	target := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target", ChatID: "oc_private", ChatType: "p2p",
		SenderID: "ou_teammate", Content: "有 UI 和客户端", CreatedAt: base,
	})
	target.ID = 8
	contexts := &capturingSemanticContextReader{
		result: serviceim.SemanticReplyContext{
			Messages: []serviceim.Message{{
				MessageID: "om_target", ChatID: "oc_private", ChatType: "p2p",
				SenderOpenID: "ou_teammate", Content: "有 UI 和客户端",
				CreateTime: base.Format(time.RFC3339),
			}},
			ContextCutoff: base.Add(3 * time.Minute),
		},
	}
	store := &fakeSemanticReplyStore{pending: []domain.WorkItem{target}}
	matcher := &fakeSemanticMatcher{result: replymatch.Resolution{
		TargetMessageID: "om_target",
		Result:          replymatch.ResultUnanswered,
		Confidence:      0.96,
		Reason:          "fixture",
	}}
	resolver := liveDelegatedReplyResolver{
		contexts:  contexts,
		store:     store,
		matcher:   matcher,
		ownerWait: 3 * time.Minute,
	}

	if _, err := resolver.Resolve(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	wantSince := base.Add(-3 * time.Minute)
	if !contexts.request.Since.Equal(wantSince) {
		t.Fatalf("since=%s want=%s", contexts.request.Since, wantSince)
	}
}

func TestLiveDelegatedReplyResolverCancelsWithdrawnTargetWithoutModel(t *testing.T) {
	target := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_missing", ChatID: "oc_group",
	})
	target.ID = 8
	store := &fakeSemanticReplyStore{}
	matcher := &fakeSemanticMatcher{}
	resolver := liveDelegatedReplyResolver{
		contexts: fakeSemanticContextReader{result: serviceim.SemanticReplyContext{
			Withdrawn:     true,
			ContextCutoff: time.Now().UTC(),
		}},
		store:   store,
		matcher: matcher,
	}

	result, err := resolver.Resolve(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Result != replymatch.ResultWithdrawn || matcher.calls != 0 ||
		len(store.recorded) != 1 {
		t.Fatalf("result=%+v matcher_calls=%d recorded=%+v", result, matcher.calls, store.recorded)
	}
}

func TestLiveDelegatedReplyResolverWaitsThreeMinutesAfterTargetEdit(t *testing.T) {
	base := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	target := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_edited", ChatID: "oc_group", Content: "旧问题", CreatedAt: base,
	})
	target.ID = 9
	store := &fakeSemanticReplyStore{pending: []domain.WorkItem{target}}
	matcher := &fakeSemanticMatcher{}
	editedAt := base.Add(2 * time.Minute)
	cutoff := editedAt.Add(30 * time.Second)
	resolver := liveDelegatedReplyResolver{
		contexts: fakeSemanticContextReader{result: serviceim.SemanticReplyContext{
			Messages: []serviceim.Message{{
				MessageID: "om_edited", ChatID: "oc_group", Content: "编辑后的问题",
				CreateTime: base.Format(time.RFC3339),
				UpdateTime: editedAt.Format(time.RFC3339),
			}},
			ContextCutoff: cutoff,
		}},
		store:     store,
		matcher:   matcher,
		ownerWait: 3 * time.Minute,
	}

	result, err := resolver.Resolve(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Result != replymatch.ResultAmbiguous ||
		result.RetryAfter != 150*time.Second ||
		matcher.calls != 0 ||
		len(store.recorded) != 1 {
		t.Fatalf(
			"result=%+v matcher_calls=%d recorded=%+v",
			result,
			matcher.calls,
			store.recorded,
		)
	}
}

func TestAuthStatusReportsMissingUserTokenSeparately(t *testing.T) {
	t.Setenv("LARK_AGENT_APP_SECRET", "super-secret-value")
	cfg := config.Default()
	cfg.Lark.AppID = "cli_a"
	cfg.Lark.KeychainService = "lark-agent-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Assistant.OpenIDs = []string{"ou_bot"}
	cfg.Workspace.Root = t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Execute(strings.NewReader(""), &out, &errOut, []string{
		"--config", configPath,
		"auth", "status",
	}); code != 0 {
		t.Fatalf("status code=%d stderr=%s", code, errOut.String())
	}
	output := out.String()
	for _, want := range []string{
		`"app_secret":true`,
		`"user_token":false`,
		`"configured":true`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("auth status missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "super-secret-value") {
		t.Fatalf("auth status leaked secret: %s", output)
	}
}

func TestMergeAuthLoginInputAllowsAddingUserTokenToExistingAppSecret(t *testing.T) {
	merged, err := mergeAuthLoginInput(serviceim.Credentials{
		AppSecret: "existing-app-secret",
	}, authLoginInput{
		UserAccessToken: " user-token ",
		RefreshToken:    " refresh-token ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if merged.AppSecret != "existing-app-secret" ||
		merged.UserAccessToken != "user-token" ||
		merged.RefreshToken != "refresh-token" {
		t.Fatalf("merged=%+v", merged)
	}
}

func TestMergeAuthLoginInputStillRequiresAppSecretWithoutExistingSecret(t *testing.T) {
	_, err := mergeAuthLoginInput(serviceim.Credentials{}, authLoginInput{
		UserAccessToken: "user-token",
	})
	if err == nil || !strings.Contains(err.Error(), "app_secret is required") {
		t.Fatalf("err=%v", err)
	}
}

func TestApprovalCommandsListAndApproveExactAction(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_cmd_approval", Content: "format"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	actionID, err := store.RequestShellApproval(context.Background(), domain.DedupKey(event), "gofmt -w .", ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Execute(strings.NewReader(""), &out, &errOut, []string{"--state", statePath, "approval", "list"}); code != 0 {
		t.Fatalf("list code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"status":"awaiting_approval"`) {
		t.Fatalf("list output=%s", out.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Execute(strings.NewReader(""), &out, &errOut, []string{
		"--state", statePath, "approval", "approve", strconv.FormatInt(actionID, 10),
	}); code != 0 {
		t.Fatalf("approve code=%d stderr=%s", code, errOut.String())
	}
	store, err = storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	action, err := store.GetActionAttempt(actionID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
}

func TestMemoryCommandsPersistAddListFeedbackAndConfirmedDelete(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")

	run := func(args ...string) (string, string, int) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := Execute(
			strings.NewReader(""),
			&out,
			&errOut,
			append([]string{"--state", statePath, "memory"}, args...),
		)
		return out.String(), errOut.String(), code
	}

	addOut, addErr, code := run("add", string(memory.KindPreference), "优先使用中文回复")
	if code != 0 {
		t.Fatalf("add code=%d stderr=%s", code, addErr)
	}
	if !strings.Contains(addOut, `"status":"confirmed"`) {
		t.Fatalf("add output=%s", addOut)
	}

	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.ListMemories(context.Background(), "global", false, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%+v", records)
	}
	memoryID := records[0].ID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	listOut, listErr, code := run("list")
	if code != 0 {
		t.Fatalf("list code=%d stderr=%s", code, listErr)
	}
	if !strings.Contains(listOut, memoryID) || !strings.Contains(listOut, "优先使用中文回复") {
		t.Fatalf("list output=%s", listOut)
	}

	feedbackOut, feedbackErr, code := run("feedback", memoryID, string(memory.FeedbackHelpful), "回复语言正确")
	if code != 0 {
		t.Fatalf("feedback code=%d stderr=%s", code, feedbackErr)
	}
	if !strings.Contains(feedbackOut, `"verdict":"helpful"`) {
		t.Fatalf("feedback output=%s", feedbackOut)
	}

	_, deleteErr, code := run("delete", memoryID)
	if code == 0 || !strings.Contains(deleteErr, "--confirm") {
		t.Fatalf("delete without confirm code=%d stderr=%s", code, deleteErr)
	}

	deleteOut, deleteErr, code := run("delete", memoryID, "--confirm")
	if code != 0 {
		t.Fatalf("delete code=%d stderr=%s", code, deleteErr)
	}
	if !strings.Contains(deleteOut, `"deleted":true`) {
		t.Fatalf("delete output=%s", deleteOut)
	}

	listOut, listErr, code = run("list")
	if code != 0 {
		t.Fatalf("list after delete code=%d stderr=%s", code, listErr)
	}
	if strings.Contains(listOut, memoryID) {
		t.Fatalf("deleted memory remains visible: %s", listOut)
	}
}

type configuredPollerIM struct {
	message serviceim.Message
}

func (f configuredPollerIM) SearchChats(context.Context, serviceim.SearchChatsRequest) (serviceim.SearchChatsResult, error) {
	return serviceim.SearchChatsResult{Items: []serviceim.Chat{{
		ChatID: f.message.ChatID, Name: "Test Group", ChatMode: "group",
	}}}, nil
}

func (f configuredPollerIM) SearchMessages(_ context.Context, req serviceim.SearchMessagesRequest) (serviceim.SearchMessagesResult, error) {
	if req.IncludeAtMe {
		return serviceim.SearchMessagesResult{}, nil
	}
	return serviceim.SearchMessagesResult{Items: []serviceim.Message{f.message}}, nil
}

type chatSearchCaller struct {
	requests  []serviceim.APIRequest
	responses map[string]map[string]any
}

func (f *chatSearchCaller) CallAPI(_ context.Context, req serviceim.APIRequest) (any, error) {
	f.requests = append(f.requests, req)
	pageToken, _ := req.Params["page_token"].(string)
	data := f.responses[pageToken]
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{
		"data": data,
	}, nil
}

type failingMessageContextCaller struct{}

func (failingMessageContextCaller) CallAPI(context.Context, serviceim.APIRequest) (any, error) {
	return nil, errors.New("Authentication token expired. Please request a new one.")
}

type delegatedContextCaller struct {
	base time.Time
}

func (c delegatedContextCaller) CallAPI(
	_ context.Context,
	req serviceim.APIRequest,
) (any, error) {
	switch req.Path {
	case "/open-apis/im/v1/messages/mget":
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id": "om_target", "chat_id": "oc_group",
				"content":     `{"text":"编辑后的提问"}`,
				"create_time": c.base.Format(time.RFC3339),
			},
		}}}, nil
	case "/open-apis/im/v1/messages":
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id": "om_owner_context", "chat_id": "oc_group",
				"sender":      map[string]any{"id": map[string]any{"open_id": "ou_owner"}},
				"content":     `{"text":"这条讨论没有回答具体问题"}`,
				"create_time": c.base.Add(time.Minute).Format(time.RFC3339),
			},
			map[string]any{
				"message_id": "om_target", "chat_id": "oc_group",
				"content":     `{"text":"编辑后的提问"}`,
				"create_time": c.base.Format(time.RFC3339),
			},
		}}}, nil
	default:
		return nil, errors.New("unexpected Lark API path: " + req.Path)
	}
}

func TestConversationBuilderIncludesPostTargetDiscussionForDelegatedReply(t *testing.T) {
	base := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	builder := &conversationBuilder{
		svc:                serviceim.NewService(delegatedContextCaller{base: base}, "ou_owner"),
		includeLarkContext: true,
		base:               agentcontext.Builder{},
	}
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_target",
		ChatID:    "oc_group",
		Content:   "旧提问",
		CreatedAt: base,
	})
	item.WorkKind = domain.WorkKindDirectMention

	bundle, err := builder.Build(item)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Event.Content != "编辑后的提问" ||
		len(bundle.Conversation) != 2 ||
		bundle.Conversation[1].MessageID != "om_owner_context" ||
		bundle.ContextSelection.Reason != "delegated_post_target_window" {
		t.Fatalf("bundle=%+v", bundle)
	}
}

func TestConversationBuilderContinuesWhenLarkHistoryIsUnavailable(t *testing.T) {
	builder := &conversationBuilder{
		svc:                serviceim.NewService(failingMessageContextCaller{}, "ou_owner"),
		includeLarkContext: true,
		base:               agentcontext.Builder{},
	}
	bundle, err := builder.Build(domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_context_unavailable",
		ChatID:    "oc_private",
		Content:   "请继续处理这个问题",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.ContextSelection.Incomplete ||
		bundle.ContextSelection.AnchorMessageID != "om_context_unavailable" ||
		bundle.ContextSelection.Reason != "lark_context_unavailable" ||
		len(bundle.Conversation) != 0 {
		t.Fatalf("bundle=%+v", bundle)
	}
}

func TestConversationBuilderResolvesTrustedGitHubReferenceFromSharedContext(t *testing.T) {
	signingKey := "synthetic-signing-key"
	ref := domain.GitHubReference{
		SchemaVersion:      1,
		Repository:         "example/widgets",
		Kind:               domain.GitHubReferenceWorkflowRun,
		WorkflowRunID:      981,
		WorkflowRunAttempt: 2,
	}
	marker, err := internalgithub.EncodeReferenceMarker(ref, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	target := domain.NormalizedEvent{
		MessageID:        "om_question",
		ChatID:           "oc_synthetic",
		ReplyToMessageID: "om_notification",
		Content:          "请检查这次工作流失败",
	}
	item := domain.NewWorkItem(target)
	item.ResolvedContext = []domain.NormalizedEvent{
		{
			MessageID:  "om_notification",
			ChatID:     "oc_synthetic",
			SenderID:   "cli_current",
			SenderType: "app",
			Content:    marker,
		},
		target,
	}
	builder := &conversationBuilder{
		githubEnabled:       true,
		currentAppID:        "cli_current",
		allowedRepositories: []string{"example/widgets"},
		referenceSigningKey: signingKey,
		base:                agentcontext.Builder{},
	}

	bundle, err := builder.Build(item)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.GitHubReference == nil || *bundle.GitHubReference != ref {
		t.Fatalf("github reference=%+v", bundle.GitHubReference)
	}
}

func TestConfiguredLivePollerPersistsRouterPriority(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	start := time.Now().UTC().Add(-time.Minute)
	if err := store.SetPollCursor("messages:all", start); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Workspace.Root = t.TempDir()
	cfg.Owner.OpenID = "owner"
	cfg.Owner.Name = "Owner"
	cfg.Assistant.OpenIDs = []string{"assistant"}
	cfg.Assistant.Names = []string{"Agent"}
	r := newAgentRouter(cfg, store)
	poller := newConfiguredLivePoller(configuredPollerIM{message: serviceim.Message{
		MessageID: "om_wired_fast", ChatID: "oc_lobster", ChatType: "group",
		SenderOpenID: "owner", Content: "@_user_1 ping",
		Mentions:   []domain.Mention{{OpenID: "assistant", Name: "Agent"}},
		CreateTime: time.Now().UTC().Format(time.RFC3339),
	}}, store, r, cfg, "Test Group", nil, true)
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 ||
		items[0].WorkKind != domain.WorkKindFastPath ||
		items[0].Priority != domain.PriorityFastPath {
		t.Fatalf("items=%+v", items)
	}
}

func TestLiveOptionsWithoutUserTokenDoNotExposeUserContextTools(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("LARK_AGENT_APP_SECRET", "secret")
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	cfg := config.Default()
	cfg.Workspace.Root = t.TempDir()
	cfg.Lark.AppID = "cli_a"
	cfg.Lark.KeychainService = "lark-agent-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Assistant.OpenIDs = []string{"ou_bot"}
	cfg.Assistant.Names = []string{"Assistant Bot"}
	options, realtimeRunner, _, info, err := buildLiveOptions(
		context.Background(),
		cfg,
		store,
		newAgentRouter(cfg, store),
		true,
		false,
		"Example Group",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if realtimeRunner == nil {
		t.Fatal("realtime owner-request source should still be configured")
	}
	if info["realtime_owner_requests"] != true || info["realtime_requests"] != true {
		t.Fatalf("realtime compatibility fields=%+v", info)
	}
	if info["user_polling"] != false || info["user_context"] != false || info["user_token"] != "missing" {
		t.Fatalf("info=%+v", info)
	}
	if info["agent_tools"] != 13 {
		t.Fatalf("agent tool count=%v; user-token-only context tools leaked", info["agent_tools"])
	}
	if len(options) == 0 {
		t.Fatal("expected daemon options")
	}
}

func TestLiveOptionsCarryRuntimePolicyThroughCompactedModelPrompt(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("LARK_AGENT_APP_SECRET", "secret")
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck

	cfg := config.Default()
	cfg.Workspace.Root = t.TempDir()
	cfg.Lark.AppID = "cli_a"
	cfg.Lark.KeychainService = "lark-agent-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Assistant.OpenIDs = []string{"ou_bot"}
	cfg.Assistant.Names = []string{"Assistant Bot"}
	cfg.Policy.OwnerWait = 3 * time.Minute
	cfg.Policy.OwnerReplyConfidenceMin = 0.85
	cfg.Policy.OwnerReplyRetry = 5 * time.Minute
	cfg.Policy.ReplyConfidenceMin = 0.70

	agentRouter := newAgentRouter(cfg, store)
	options, _, _, _, err := buildLiveOptions(
		context.Background(),
		cfg,
		store,
		agentRouter,
		true,
		false,
		"Example Group",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	capture := &runtimePolicyCaptureDecider{}
	options = append(options,
		app.WithSemanticControlResolver(notCommandSemanticResolver{}),
		app.WithDecider(capture),
		app.WithReplyHandler(completedRuntimePolicyReplyHandler{}),
	)

	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:     "om_live_policy",
		ChatID:        "oc_assistant_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "再确认一下：0.85 和 0.70 分别管什么？",
	})
	item.WorkKind = domain.WorkKindSimpleQuestion
	for i := 0; i < 40; i++ {
		item.ResolvedContext = append(item.ResolvedContext, domain.NormalizedEvent{
			MessageID: "om_context_" + strconv.Itoa(i),
			ChatID:    item.Event.ChatID,
			SenderID:  "ou_owner",
			Content:   strings.Repeat("历史上下文", 1200),
		})
	}
	item.ResolvedContext = append(item.ResolvedContext, item.Event)
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	if _, err := app.NewDaemon(store, agentRouter, options...).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if capture.bundle.RuntimePolicy != runtimePolicySnapshot(cfg) {
		t.Fatalf("runtime policy was lost before the decider: %+v", capture.bundle.RuntimePolicy)
	}
	for _, want := range []string{
		`"runtime_policy":{"authoritative":true`,
		`"owner_wait":"3m0s"`,
		`"owner_reply_confidence_min":0.85`,
		`"owner_reply_retry":"5m0s"`,
		`"reply_confidence_min":0.7`,
		`"must_not_infer_from_workspace_rules":true`,
	} {
		if !strings.Contains(capture.prompt, want) {
			t.Fatalf("compacted production prompt missing %q:\n%s", want, capture.prompt)
		}
	}
	if len(capture.prompt) > 49*1024 {
		t.Fatalf("production prompt was not compacted: bytes=%d", len(capture.prompt))
	}
}

func TestConfiguredGroupsReplyScopesRequireChatQuery(t *testing.T) {
	if err := validateLiveReplyScopes(domain.ReplyScopeAllGroups, domain.ReplyScopeConfiguredGroups, ""); err == nil ||
		!strings.Contains(err.Error(), "policy.reply_scope") ||
		!strings.Contains(err.Error(), "--chat-query") {
		t.Fatalf("missing delegated configured-groups validation: %v", err)
	}
	if err := validateLiveReplyScopes(domain.ReplyScopeConfiguredGroups, domain.ReplyScopeAllGroups, ""); err == nil ||
		!strings.Contains(err.Error(), "assistant.reply_scope") ||
		!strings.Contains(err.Error(), "--chat-query") {
		t.Fatalf("missing assistant configured-groups validation: %v", err)
	}
	if err := validateLiveReplyScopes(
		domain.ReplyScopeConfiguredGroups,
		domain.ReplyScopeConfiguredGroups,
		"Acceptance Group",
	); err != nil {
		t.Fatalf("configured groups with query rejected: %v", err)
	}
	if err := validateLiveReplyScopes(
		domain.ReplyScopeAllGroups,
		domain.ReplyScopeAllGroups,
		"",
	); err != nil {
		t.Fatalf("all groups without query rejected: %v", err)
	}
}

func TestConfiguredAssistantChatsResolveEveryPageWithBotIdentity(t *testing.T) {
	caller := &chatSearchCaller{responses: map[string]map[string]any{
		"": {
			"items": []any{map[string]any{"meta_data": map[string]any{
				"chat_id": "oc_acceptance",
				"name":    "Acceptance Group",
			}}},
			"has_more":   true,
			"page_token": "next",
		},
		"next": {
			"items": []any{map[string]any{"meta_data": map[string]any{
				"chat_id": "oc_second",
				"name":    "Second Configured Group",
			}}},
		},
	}}
	chatIDs, err := discoverConfiguredAssistantChats(
		context.Background(),
		serviceim.NewService(caller, "ou_owner"),
		"Acceptance Group",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(chatIDs) != 2 || chatIDs[0] != "oc_acceptance" || chatIDs[1] != "oc_second" {
		t.Fatalf("chat IDs=%v", chatIDs)
	}
	if len(caller.requests) != 2 ||
		caller.requests[0].As != serviceim.IdentityBot ||
		caller.requests[1].As != serviceim.IdentityBot ||
		caller.requests[1].Params["page_token"] != "next" {
		t.Fatalf("search requests=%+v", caller.requests)
	}
}

func TestConfiguredAssistantChatsFailWhenBotSeesNoMatch(t *testing.T) {
	caller := &chatSearchCaller{responses: map[string]map[string]any{}}
	_, err := discoverConfiguredAssistantChats(
		context.Background(),
		serviceim.NewService(caller, "ou_owner"),
		"missing",
	)
	if err == nil ||
		!strings.Contains(err.Error(), "assistant.reply_scope") ||
		!strings.Contains(err.Error(), "no bot-visible group") {
		t.Fatalf("error=%v", err)
	}
}

func TestAgentConfigFingerprintIncludesDecisionToolContract(t *testing.T) {
	cfg := config.Default()
	contract, err := currentAgentOperatingContract()
	if err != nil {
		t.Fatal(err)
	}
	changed := contract
	changed.SubmitDecisionDescription += " changed"

	originalFingerprint := agentConfigFingerprintForContract(cfg, contract)
	changedFingerprint := agentConfigFingerprintForContract(cfg, changed)
	if originalFingerprint == changedFingerprint {
		t.Fatalf("decision tool contract did not change fingerprint: %s", originalFingerprint)
	}
	if contract.SubmitDecisionName != "submit_decision" ||
		!strings.Contains(contract.SubmitDecisionDescription, "structured decision") ||
		!strings.Contains(contract.SubmitDecisionSchema, `"decision"`) ||
		!strings.Contains(contract.SubmitDecisionSchema, `"evidence_status"`) ||
		!strings.Contains(contract.SubmitDecisionSchema, "canonical evidence-limited response") {
		t.Fatalf("incomplete current operating contract: %+v", contract)
	}
}

func TestQueueSummaryCommandReportsFastPathLane(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om_fast", Content: "几点了"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkItemScheduling(items[0].ID, domain.WorkKindFastPath, domain.PriorityFastPath, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Execute(strings.NewReader(""), &out, &errOut, []string{"--state", statePath, "queue", "summary"}); code != 0 {
		t.Fatalf("summary code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"lane_counts"`) || !strings.Contains(out.String(), `"fast_path":1`) {
		t.Fatalf("summary output=%s", out.String())
	}
}

func TestQueueCancelRequiresReasonAndCancelsInterruptedExceptKept(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"om_cmd_cancel", "om_cmd_keep"} {
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
	var cancelID, keepID int64
	for _, item := range items {
		switch item.Event.MessageID {
		case "om_cmd_cancel":
			cancelID = item.ID
		case "om_cmd_keep":
			keepID = item.ID
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })

	var out, errOut bytes.Buffer
	code := Execute(strings.NewReader(""), &out, &errOut, []string{
		"--state", statePath, "queue", "cancel", "--all-interrupted",
	})
	if code == 0 || !strings.Contains(errOut.String(), "--reason") {
		t.Fatalf("missing reason code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = Execute(strings.NewReader(""), &out, &errOut, []string{
		"--state", statePath, "queue", "cancel", "--all-interrupted",
		"--keep-work-id", strconv.FormatInt(keepID, 10),
		"--reason", "audited stale command fixture",
	})
	if code != 0 {
		t.Fatalf("cancel code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), `"changed":1`) ||
		!strings.Contains(out.String(), strconv.FormatInt(cancelID, 10)) {
		t.Fatalf("cancel output=%s", out.String())
	}
	items, err = current.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		switch item.ID {
		case cancelID:
			if item.Status != domain.StatusCancelled {
				t.Fatalf("cancelled item=%+v", item)
			}
		case keepID:
			if item.Status != domain.StatusInterrupted {
				t.Fatalf("kept item=%+v", item)
			}
		}
	}
}

func TestQueueExposesExplicitInspectAndResumeCommands(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), &bytes.Buffer{})
	for _, path := range [][]string{
		{"queue", "inspect"},
		{"queue", "resume"},
		{"queue", "backfill"},
		{"queue", "cancel"},
		{"queue", "tasks"},
		{"queue", "acknowledge"},
		{"queue", "reconcile"},
	} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if command == nil || command.Name() != path[len(path)-1] {
			t.Fatalf("command %v is not registered", path)
		}
	}
}

func TestQueueTasksAndAcknowledgeUseOwnerControlState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_cli_owner_control",
		Content:   "需要人工收口的历史任务",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := first.ListWorkItems()
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	workItemID := items[0].ID
	if claimed, ok, claimErr := first.ClaimNext("owner-control-cli-test"); claimErr != nil {
		t.Fatal(claimErr)
	} else if !ok || claimed.ID != workItemID {
		t.Fatalf("claimed=%+v ok=%v", claimed, ok)
	}
	if err := first.MarkDeadLetter(workItemID, "fixture requires owner acknowledgement"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := Execute(strings.NewReader(""), &out, &errOut, []string{
		"--state", statePath, "queue", "tasks", "--view", "action",
	})
	if code != 0 {
		t.Fatalf("tasks code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), `"total":1`) ||
		!strings.Contains(out.String(), `"message_id":"om_cli_owner_control"`) {
		t.Fatalf("tasks output=%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = Execute(strings.NewReader(""), &out, &errOut, []string{
		"--state", statePath, "queue", "acknowledge",
		"--work-id", strconv.FormatInt(workItemID, 10),
		"--reason", "已人工核对",
	})
	if code != 0 {
		t.Fatalf("acknowledge code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), `"name":"task_acknowledge"`) ||
		!strings.Contains(out.String(), `"work_item_id":`+strconv.FormatInt(workItemID, 10)) {
		t.Fatalf("acknowledge output=%s", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = Execute(strings.NewReader(""), &out, &errOut, []string{
		"--state", statePath, "queue", "tasks", "--view", "action",
	})
	if code != 0 {
		t.Fatalf("tasks after acknowledge code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"total":0`) {
		t.Fatalf("tasks after acknowledge output=%s", out.String())
	}
}

func TestOwnerNotificationTextUsesConcretePreReplyAction(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_coordination"})
	text := ownerNotificationText(item, domain.Decision{
		Kind:        domain.DecisionReply,
		Reason:      "direct_mention",
		ReplyText:   "我已完成接口契约核对，当前还需要示例状态变更。",
		OwnerAction: "确认示例状态变更通知契约并同步 示例客户端回调",
	}, "测试负责人", "zh-CN")
	for _, want := range []string{
		"已收到消息 om_coordination 的代回复请求",
		"我已完成接口契约核对",
		"即将自动发送",
		"发送后仍需你处理",
		"确认示例状态变更通知契约并同步 示例客户端回调",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notification missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "direct_mention") {
		t.Fatalf("notification leaked internal reason: %s", text)
	}
	if strings.Contains(text, "请查看智能助手的回复，并确认") {
		t.Fatalf("automatic pre-reply notification asked for unnecessary confirmation: %s", text)
	}
}

func TestOwnerNotificationTextSaysAutomaticReplyNeedsNoOwnerAction(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_auto_reply"})
	text := ownerNotificationText(item, domain.Decision{
		Kind:      domain.DecisionReply,
		ReplyText: "已完成只读核对，当前配置已经生效。",
	}, "测试负责人", "zh-CN")
	for _, want := range []string{"测试负责人", "即将自动发送", "当前无需你操作"} {
		if !strings.Contains(text, want) {
			t.Fatalf("notification missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"正在准备", "请查看", "确认是否"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("automatic notification contains %q: %s", forbidden, text)
		}
	}
}

func TestApprovalNotificationTextIncludesExactPrivateCommands(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_approval"})
	text := approvalNotificationText(item, domain.Decision{
		Kind:        domain.DecisionRequestApproval,
		Relevance:   domain.RelevanceDirectMention,
		ReplyText:   "我已核对上下文，但还不能确认具体组织。",
		OwnerAction: "确认具体 OpenAI 组织",
	}, domain.Action{ID: 355, Status: domain.ActionAwaitingApproval}, "测试负责人", "zh-CN")
	for _, want := range []string{
		"审批 #355",
		"尚未发送",
		"我已核对上下文",
		"确认具体 OpenAI 组织",
		"/approval approve 355 confirm",
		"/approval reject 355 <原因>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("approval notification missing %q: %s", want, text)
		}
	}
}

func TestApprovalNotificationTextPreservesAssistantRequestIdentity(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_assistant_approval"})
	text := approvalNotificationText(item, domain.Decision{
		Kind:        domain.DecisionRequestApproval,
		Relevance:   domain.RelevanceAssistantRequest,
		ReplyText:   "这是等待确认的助手答复。",
		OwnerAction: "确认是否发送",
	}, domain.Action{ID: 356, Status: domain.ActionAwaitingApproval}, "测试负责人", "zh-CN")
	for _, want := range []string{"助手答复草稿", "审批 #356", "尚未发送"} {
		if !strings.Contains(text, want) {
			t.Fatalf("assistant approval notification missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "代回复草稿") {
		t.Fatalf("assistant approval was mislabeled as delegated: %s", text)
	}
}

func TestOwnerNotificationTextDoesNotPasteEnglishModelReason(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_missing_context"})
	text := ownerNotificationText(item, domain.Decision{
		Kind:   domain.DecisionNotify,
		Reason: "This is a direct mention but the referenced context is not readable, so no evidence-backed response is possible.",
	}, "测试负责人", "zh-CN")
	for _, want := range []string{"测试负责人", "om_missing_context", "上下文"} {
		if !strings.Contains(text, want) {
			t.Fatalf("notification missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "This is a direct mention") {
		t.Fatalf("notification leaked English model reason: %s", text)
	}
}

type capturingOwnerMessenger struct {
	notification string
	key          string
	count        int
	replyCount   int
	fail         error
}

func (m *capturingOwnerMessenger) ReplyAsUser(
	context.Context,
	agenttools.ReplyRequest,
) (agenttools.ReplyResult, error) {
	m.replyCount++
	return agenttools.ReplyResult{}, nil
}

func (m *capturingOwnerMessenger) NotifyOwner(
	_ context.Context,
	request agenttools.NotifyRequest,
) error {
	m.notification = request.Text
	m.key = request.IdempotencyKey
	m.count++
	if m.fail != nil {
		err := m.fail
		m.fail = nil
		return err
	}
	return nil
}

func TestTerminalFailureRequirementSendsPrivateBotCommandsOnce(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_terminal_private_commands",
		MessageID: "om_terminal_private_commands",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	item.Status = domain.StatusWaitingUser
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	if _, err := store.RecordWorkIntake(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("terminal-command-test")
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		"same-chat context is incomplete",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}

	messenger := &capturingOwnerMessenger{}
	owner := config.OwnerConfig{
		Name:              "测试负责人",
		PreferredLanguage: agentlocale.LanguageChinese,
		FallbackLanguage:  agentlocale.LanguageChinese,
	}
	if err := flushOwnerResolutionNotifications(
		context.Background(),
		store,
		messenger,
		owner,
		&bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/task " + strconv.FormatInt(claimed.ID, 10),
		"/task resume " + strconv.FormatInt(claimed.ID, 10) + " confirm",
	} {
		if !strings.Contains(messenger.notification, want) {
			t.Fatalf("terminal notification missing %q: %s", want, messenger.notification)
		}
	}
	if strings.Contains(messenger.notification, "queue inspect") ||
		strings.Contains(messenger.notification, "queue resume") {
		t.Fatalf("terminal notification requires local CLI: %s", messenger.notification)
	}
	if messenger.count != 1 {
		t.Fatalf("notification count=%d", messenger.count)
	}
	if messenger.replyCount != 0 {
		t.Fatalf("terminal handling sent %d source-chat replies", messenger.replyCount)
	}
	if err := flushOwnerResolutionNotifications(
		context.Background(),
		store,
		messenger,
		owner,
		&bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	if messenger.count != 1 {
		t.Fatalf("duplicate terminal notification count=%d", messenger.count)
	}
}

func TestTerminalFailureRequirementDoesNotSendAfterExplicitResume(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_resumed_before_terminal_notice",
		MessageID: "om_resumed_before_terminal_notice",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	item.Status = domain.StatusWaitingUser
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	if _, err := store.RecordWorkIntake(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("resume-before-terminal-notice")
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		"same-chat context is incomplete",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    claimed.ID,
		ForceTerminal: true,
	}); err != nil {
		t.Fatal(err)
	}

	messenger := &capturingOwnerMessenger{}
	if err := flushOwnerResolutionNotifications(
		context.Background(),
		store,
		messenger,
		config.OwnerConfig{
			Name:              "测试负责人",
			PreferredLanguage: agentlocale.LanguageChinese,
			FallbackLanguage:  agentlocale.LanguageChinese,
		},
		&bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	if messenger.count != 0 || messenger.replyCount != 0 {
		t.Fatalf("stale notification was sent: %+v", messenger)
	}
	handler := liveTerminalFailureHandler{
		store:     store,
		messenger: messenger,
		owner: config.OwnerConfig{
			Name:              "测试负责人",
			PreferredLanguage: agentlocale.LanguageChinese,
			FallbackLanguage:  agentlocale.LanguageChinese,
		},
	}
	if err := handler.HandleTerminalFailure(
		context.Background(),
		claimed,
		errors.New("same-chat context is incomplete"),
	); err != nil {
		t.Fatal(err)
	}
	if messenger.count != 0 || messenger.replyCount != 0 {
		t.Fatalf("stale immediate notification was sent: %+v", messenger)
	}
}

func TestTerminalFailureRequirementRetriesExactFailedSend(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)
	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_retry_terminal_notice",
		MessageID: "om_retry_terminal_notice",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	item.Status = domain.StatusWaitingUser
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	if _, err := store.RecordWorkIntake(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("retry-terminal-notice")
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		"same-chat context is incomplete",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}

	messenger := &capturingOwnerMessenger{fail: errors.New("temporary Lark failure")}
	owner := config.OwnerConfig{
		Name:              "测试负责人",
		PreferredLanguage: agentlocale.LanguageChinese,
		FallbackLanguage:  agentlocale.LanguageChinese,
	}
	errOut := &bytes.Buffer{}
	if err := flushOwnerResolutionNotifications(
		context.Background(),
		store,
		messenger,
		owner,
		errOut,
	); err != nil {
		t.Fatal(err)
	}
	if messenger.count != 1 || !strings.Contains(errOut.String(), "temporary Lark failure") {
		t.Fatalf("first send messenger=%+v stderr=%s", messenger, errOut.String())
	}
	firstKey := messenger.key
	if firstKey == "" || len(firstKey) > 50 {
		t.Fatalf("first key=%q", firstKey)
	}

	if err := flushOwnerResolutionNotifications(
		context.Background(),
		store,
		messenger,
		owner,
		&bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	if messenger.count != 2 || messenger.key != firstKey {
		t.Fatalf("retry messenger=%+v firstKey=%q", messenger, firstKey)
	}
	if err := flushOwnerResolutionNotifications(
		context.Background(),
		store,
		messenger,
		owner,
		&bytes.Buffer{},
	); err != nil {
		t.Fatal(err)
	}
	if messenger.count != 2 {
		t.Fatalf("completed notice was sent again: %+v", messenger)
	}
}

func TestLiveOwnerNotifierSendsIdempotentExactApprovalNotice(t *testing.T) {
	messenger := &capturingOwnerMessenger{}
	notifier := liveOwnerNotifier{
		messenger: messenger,
		ownerName: "测试负责人",
		preferred: agentlocale.LanguageChinese,
		fallback:  agentlocale.LanguageChinese,
	}
	err := notifier.HandleApprovalNotification(
		context.Background(),
		domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_approval"}),
		domain.Decision{
			Kind:        domain.DecisionRequestApproval,
			ReplyText:   "我已核对上下文，但还不能确认具体组织。",
			OwnerAction: "确认具体 OpenAI 组织",
		},
		domain.Action{ID: 355, Status: domain.ActionAwaitingApproval},
	)
	if err != nil {
		t.Fatal(err)
	}
	if messenger.key != "owner-approval-notice:355" {
		t.Fatalf("idempotency key=%q", messenger.key)
	}
	for _, want := range []string{
		"审批 #355",
		"尚未发送",
		"/approval approve 355 confirm",
		"/approval reject 355 <原因>",
	} {
		if !strings.Contains(messenger.notification, want) {
			t.Fatalf("notification missing %q: %s", want, messenger.notification)
		}
	}
}

func TestAutoOwnerNoticeUsesResolvedDecisionLanguage(t *testing.T) {
	messenger := &capturingOwnerMessenger{}
	notifier := liveOwnerNotifier{
		messenger: messenger,
		ownerName: "Liu Chong",
		preferred: agentlocale.LanguageAuto,
		fallback:  agentlocale.LanguageChinese,
	}
	err := notifier.HandleNotification(
		context.Background(),
		domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_english",
			Content:   "Please verify the API contract.",
		}),
		domain.Decision{
			Kind:        domain.DecisionReply,
			Language:    string(agentlocale.LanguageEnglish),
			ReplyText:   "I verified the public API contract and found one unresolved permission.",
			OwnerAction: "Confirm whether that permission should be enabled.",
		},
		"owner-notice-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Liu Chong", "send this reply automatically", "I verified"} {
		if !strings.Contains(messenger.notification, want) {
			t.Fatalf("notification missing %q: %s", want, messenger.notification)
		}
	}
	if strings.Contains(messenger.notification, "智能助手") {
		t.Fatalf("notification mixed languages: %s", messenger.notification)
	}
}

func TestRecoveryNoticeUsesMessageResolvedLanguage(t *testing.T) {
	text := recoveryConvergenceText(
		storage.RecoveryConvergenceNotice{
			WorkItemID: 42,
			MessageID:  "om_english_recovery",
			Kind:       "uncertain_external_action",
		},
		config.OwnerConfig{Name: "Liu Chong"},
		agentlocale.LanguageEnglish,
	)
	for _, want := range []string{"Liu Chong", "was terminalized", "was not replayed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("recovery notice missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "已收口") {
		t.Fatalf("recovery notice mixed languages: %s", text)
	}
}

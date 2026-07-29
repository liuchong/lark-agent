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

	"github.com/liuchong/lark-agent/agent/config"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/storage"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

func TestRootDoesNotExposeCopiedInternalEventBus(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), &bytes.Buffer{})
	if command, _, err := root.Find([]string{"event", "_bus"}); err == nil &&
		command != nil && command.Name() == "_bus" {
		t.Fatal("standalone agent must not copy the official CLI internal event bus")
	}
}

type fakeSemanticContextReader struct {
	result serviceim.SemanticReplyContext
	err    error
}

func (r fakeSemanticContextReader) GetSemanticReplyContext(
	context.Context,
	serviceim.SemanticReplyContextRequest,
) (serviceim.SemanticReplyContext, error) {
	return r.result, r.err
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
	if info["agent_tools"] != 12 {
		t.Fatalf("agent tool count=%v; user-token-only context tools leaked", info["agent_tools"])
	}
	if len(options) == 0 {
		t.Fatal("expected daemon options")
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

func TestQueueExposesExplicitInspectAndResumeCommands(t *testing.T) {
	root := NewRootCommand(strings.NewReader(""), &bytes.Buffer{})
	for _, path := range [][]string{{"queue", "inspect"}, {"queue", "resume"}, {"queue", "backfill"}} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if command == nil || command.Name() != path[len(path)-1] {
			t.Fatalf("command %v is not registered", path)
		}
	}
}

func TestOwnerNotificationTextUsesConcretePostReplyAction(t *testing.T) {
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_coordination"})
	text := ownerNotificationText(item, domain.Decision{
		Kind:        domain.DecisionReply,
		Reason:      "direct_mention",
		OwnerAction: "确认示例状态变更通知契约并同步 示例客户端回调",
	})
	for _, want := range []string{
		"已回复原消息 om_coordination",
		"仍需你处理",
		"确认示例状态变更通知契约并同步 示例客户端回调",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notification missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "direct_mention") {
		t.Fatalf("notification leaked internal reason: %s", text)
	}
}

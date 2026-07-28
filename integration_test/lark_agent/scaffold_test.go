package larkagent_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/app"
	"github.com/liuchong/lark-agent/agent/config"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/feedback"
	"github.com/liuchong/lark-agent/agent/ingest"
	"github.com/liuchong/lark-agent/agent/policy"
	"github.com/liuchong/lark-agent/agent/poll"
	"github.com/liuchong/lark-agent/agent/realtime"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/router"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	"github.com/liuchong/lark-agent/agent/storage"
	"github.com/liuchong/lark-agent/agent/tools"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

type configuredAssistantPollIM struct {
	now time.Time
}

func (f configuredAssistantPollIM) SearchChats(
	context.Context,
	serviceim.SearchChatsRequest,
) (serviceim.SearchChatsResult, error) {
	return serviceim.SearchChatsResult{Items: []serviceim.Chat{{
		ChatID: "oc_user_scope", Name: "Configured Group", ChatMode: "group",
	}}}, nil
}

func (f configuredAssistantPollIM) SearchMessages(
	_ context.Context,
	req serviceim.SearchMessagesRequest,
) (serviceim.SearchMessagesResult, error) {
	if req.IncludeAtMe {
		return serviceim.SearchMessagesResult{}, nil
	}
	return serviceim.SearchMessagesResult{Items: []serviceim.Message{
		{
			MessageID: "om_bot_scope", ChatID: "oc_bot_scope", ChatType: "group",
			SenderOpenID: "ou_owner", SenderType: "user", Content: "@_user_1 在吗",
			Mentions:   []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
			CreateTime: f.now.Format(time.RFC3339),
		},
		{
			MessageID: "om_user_scope", ChatID: "oc_user_scope", ChatType: "group",
			SenderOpenID: "ou_owner", SenderType: "user", Content: "@_user_1 在吗",
			Mentions:   []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
			CreateTime: f.now.Format(time.RFC3339),
		},
		{
			MessageID: "om_non_owner_bot", ChatID: "oc_bot_scope", ChatType: "group",
			SenderOpenID: "ou_other", SenderType: "user", Content: "@_user_1 在吗",
			Mentions:   []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
			CreateTime: f.now.Format(time.RFC3339),
		},
	}}, nil
}

func buildAgentBinary(t *testing.T) string {
	t.Helper()
	repo := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "lark-agent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/lark-agent")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/lark-agent failed: %v\n%s", err, out)
	}
	return bin
}

func TestPolledAssistantScopeUsesBotResolvedGroupsEndToEnd(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	start := now.Add(-30 * time.Second)
	if err := store.SetPollCursor("messages:all", start); err != nil {
		t.Fatal(err)
	}
	r := router.New(router.Config{
		OwnerOpenID:         "ou_owner",
		AssistantOpenIDs:    []string{"ou_bot"},
		AssistantNames:      []string{"Lark Agent"},
		AssistantReplyScope: domain.ReplyScopeConfiguredGroups,
		Mode:                domain.ModeAuto,
	})
	poller := poll.New(configuredAssistantPollIM{now: now}, store, poll.Config{
		OwnerOpenID:                "ou_owner",
		ChatQuery:                  "Configured Group",
		AssistantNames:             []string{"Lark Agent"},
		ConfiguredAssistantChatIDs: []string{"oc_bot_scope"},
		Now:                        func() time.Time { return now },
		Classify:                   r.Route,
	})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	byMessageID := map[string]domain.WorkItem{}
	for _, item := range items {
		byMessageID[item.Event.MessageID] = item
	}
	if _, exists := byMessageID["om_non_owner_bot"]; exists {
		t.Fatalf("non-owner assistant mention was queued: %+v", byMessageID["om_non_owner_bot"])
	}
	botScoped := byMessageID["om_bot_scope"]
	if !botScoped.Event.InAssistantScope || botScoped.Event.InTestScope {
		t.Fatalf("bot-scoped item=%+v", botScoped)
	}
	accepted, err := r.Route(context.Background(), botScoped)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Relevance != domain.RelevanceAssistantRequest {
		t.Fatalf("accepted decision=%+v", accepted)
	}
	gate := policy.NewReplyGate(policy.Config{
		Mode:                domain.ModeAuto,
		OwnerOpenID:         "ou_owner",
		AssistantReplyScope: domain.ReplyScopeConfiguredGroups,
	}, privateReplyThreadState{})
	action, err := gate.Prepare(context.Background(), botScoped, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceAssistantRequest,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "在的。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("bot-scoped action=%+v", action)
	}
	userScoped := byMessageID["om_user_scope"]
	if userScoped.Event.InAssistantScope || !userScoped.Event.InTestScope {
		t.Fatalf("user-scoped item=%+v", userScoped)
	}
	rejected, err := r.Route(context.Background(), userScoped)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Kind != domain.DecisionIgnore || rejected.Reason != "outside_assistant_reply_scope" {
		t.Fatalf("rejected decision=%+v", rejected)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestPrivateAssistantPartnerIdentityRoutesOwnerFastPath(t *testing.T) {
	agentRouter := router.New(router.Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		Now: func() time.Time {
			return time.Date(2026, 7, 24, 5, 40, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	})
	decision, err := agentRouter.Route(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:     "om_private",
		ChatID:        "oc_private",
		ChatName:      "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "几点了？",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply ||
		decision.WorkKind != domain.WorkKindFastPath ||
		decision.Relevance != domain.RelevanceOwnerRequest {
		t.Fatalf("decision=%+v", decision)
	}
}

type realtimeFixtureConsumer struct {
	payload string
}

func (f realtimeFixtureConsumer) Consume(_ context.Context, out io.Writer) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(f.payload)); err != nil {
		return err
	}
	_, err := io.WriteString(out, compact.String()+"\n")
	return err
}

type contextFixtureCaller struct{}

func (contextFixtureCaller) CallAPI(
	_ context.Context,
	req serviceim.APIRequest,
) (interface{}, error) {
	if req.Path != "/open-apis/im/v1/messages" ||
		req.Params["container_id_type"] != "thread" ||
		req.Params["container_id"] != "omt_backend" {
		return nil, fmt.Errorf("unexpected context request: %s %+v", req.Path, req.Params)
	}
	return map[string]any{"data": map[string]any{"items": []any{
		map[string]any{
			"message_id": "om_root", "chat_id": "oc_lobster", "thread_id": "omt_backend",
			"content": `{"text":"POST /api/sample/items"}`, "create_time": "1784853997000",
		},
		map[string]any{
			"message_id": "om_parent", "chat_id": "oc_lobster", "thread_id": "omt_backend",
			"parent_id": "om_root", "content": `{"text":"每次都会访问 SampleDB"}`, "create_time": "1784853998000",
		},
		map[string]any{
			"message_id": "om_target", "chat_id": "oc_lobster", "thread_id": "omt_backend",
			"parent_id": "om_parent", "content": `{"text":"为什么？"}`, "create_time": "1784853999000",
		},
		map[string]any{
			"message_id": "om_future", "chat_id": "oc_lobster", "thread_id": "omt_backend",
			"content": `{"text":"之后才出现"}`, "create_time": "1784854000000",
		},
		map[string]any{
			"message_id": "om_other", "chat_id": "oc_other", "thread_id": "omt_backend",
			"content": `{"text":"另一个群的内容"}`, "create_time": "1784853998500",
		},
	}}}, nil
}

func TestRealtimeQuotedThreadContextPersistsAndStaysInSameChat(t *testing.T) {
	event, err := ingest.NormalizeRealtime([]byte(`{
		"type":"im.message.receive_v1",
		"event_id":"evt_context",
		"message_id":"om_target",
		"create_time":"1784853999000",
		"chat_id":"oc_lobster",
		"chat_type":"group",
		"root_id":"om_root",
		"reply_to":"om_parent",
		"thread_id":"omt_backend",
		"sender_id":"ou_owner",
		"sender_type":"user",
		"content":"为什么？"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	persisted := items[0]
	if persisted.Event.RootMessageID != "om_root" ||
		persisted.Event.ReplyToMessageID != "om_parent" ||
		persisted.Event.ThreadID != "omt_backend" {
		t.Fatalf("persisted event=%+v", persisted.Event)
	}

	messageContext, err := serviceim.NewService(contextFixtureCaller{}, "ou_owner").GetMessageContext(
		context.Background(),
		serviceim.MessageContextRequest{
			ChatID:           persisted.Event.ChatID,
			MessageID:        persisted.Event.MessageID,
			RootMessageID:    persisted.Event.RootMessageID,
			ReplyToMessageID: persisted.Event.ReplyToMessageID,
			ThreadID:         persisted.Event.ThreadID,
			CreatedAt:        persisted.Event.CreatedAt,
			Limit:            30,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	conversation := make([]domain.NormalizedEvent, 0, len(messageContext.Messages))
	for _, message := range messageContext.Messages {
		conversation = append(conversation, domain.NormalizedEvent{
			MessageID:        message.MessageID,
			ChatID:           message.ChatID,
			RootMessageID:    message.RootMessageID,
			ReplyToMessageID: message.ReplyToMessageID,
			ThreadID:         message.ThreadID,
			Content:          message.Content,
		})
	}
	bundle, err := (agentcontext.Builder{
		Conversation:     conversation,
		ContextSelection: messageContext.Selection,
	}).Build(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ContextSelection.Mode != domain.ContextModeThread {
		t.Fatalf("selection=%+v", bundle.ContextSelection)
	}
	contextIDs := make([]string, 0, len(bundle.Conversation))
	for _, message := range bundle.Conversation {
		contextIDs = append(contextIDs, message.MessageID)
		if message.ChatID != "oc_lobster" {
			t.Fatalf("cross-chat context leaked: %+v", message)
		}
	}
	if got := strings.Join(contextIDs, ","); got != "om_root,om_parent,om_target" {
		t.Fatalf("context ids=%s", got)
	}
}

func TestRealtimeOwnerIntakeDedupesWithPollFallback(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	agentRouter := router.New(router.Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		Now: func() time.Time {
			return time.Date(2026, 7, 24, 8, 46, 40, 0, time.FixedZone("CST", 8*60*60))
		},
	})
	eventCreatedAt := store.CurrentSession().StartedAt.Add(time.Second)
	source := realtime.NewSource(realtimeFixtureConsumer{payload: fmt.Sprintf(`{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_realtime",
	  "message_id":"om_shared",
	  "create_time":"%d",
	  "chat_id":"oc_private",
	  "chat_type":"p2p",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"几点了？"
	}`, eventCreatedAt.UnixMilli())}, store, realtime.Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		Classify:         agentRouter.Route,
	})
	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	inserted, err := store.EnqueueEvent(domain.NormalizedEvent{
		Source:        domain.SourcePoll,
		EventID:       "poll_shared",
		MessageID:     "om_shared",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "几点了？",
		CreatedAt:     eventCreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("poll fallback inserted duplicate realtime message")
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 ||
		items[0].Event.Source != domain.SourceRealtime ||
		items[0].WorkKind != domain.WorkKindFastPath {
		t.Fatalf("items=%+v", items)
	}
	messenger := &privateReplyMessenger{}
	daemon := app.NewDaemon(
		store,
		agentRouter,
		app.WithReplyHandler(reply.NewController(
			policy.NewReplyGate(policy.Config{
				Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
			}, privateReplyThreadState{}),
			messenger,
			store,
		)),
		app.WithOwnerActivityHandler(feedback.NewController(messenger, store)),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed ||
		strings.Join(messenger.events, ",") != "working_reaction_on,reply,working_reaction_off" {
		t.Fatalf("result=%+v events=%v", result, messenger.events)
	}
}

type privateReplyMessenger struct {
	userReplies  int
	botReplies   int
	ownerNotices int
	reactions    int
	deletions    int
	replyText    string
	events       []string
}

func (m *privateReplyMessenger) ReplyAsUser(_ context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	m.userReplies++
	m.replyText = req.Text
	m.events = append(m.events, "user_reply")
	return tools.ReplyResult{MessageID: "om_user_reply"}, nil
}

func (m *privateReplyMessenger) ReplyAsBot(_ context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	m.botReplies++
	m.replyText = req.Text
	m.events = append(m.events, "reply")
	return tools.ReplyResult{MessageID: "om_bot_reply"}, nil
}

func (m *privateReplyMessenger) NotifyOwner(context.Context, tools.NotifyRequest) error {
	m.ownerNotices++
	return nil
}

func (m *privateReplyMessenger) CreateReactionAsBot(context.Context, string, string) (string, error) {
	m.reactions++
	m.events = append(m.events, "working_reaction_on")
	return "reaction_typing", nil
}

func (m *privateReplyMessenger) DeleteReactionAsBot(context.Context, string, string) error {
	m.deletions++
	m.events = append(m.events, "working_reaction_off")
	return nil
}

type privateReplyThreadState struct{}

func (privateReplyThreadState) OwnerAlreadyReplied(context.Context, domain.WorkItem) (bool, error) {
	return false, nil
}

func (privateReplyThreadState) MessageWithdrawn(context.Context, domain.WorkItem) (bool, error) {
	return false, nil
}

func TestDelegatedReplyScopeControlsOutsideGroupReply(t *testing.T) {
	tests := []struct {
		name        string
		scope       domain.ReplyScope
		wantStatus  domain.ActionStatus
		wantReplies int
	}{
		{
			name:        "default all groups sends",
			scope:       config.Default().Policy.ReplyScope,
			wantStatus:  domain.ActionCompleted,
			wantReplies: 1,
		},
		{
			name:        "configured groups blocks outside group",
			scope:       domain.ReplyScopeConfiguredGroups,
			wantStatus:  domain.ActionBlocked,
			wantReplies: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &privateReplyMessenger{}
			controller := reply.NewController(
				policy.NewReplyGate(policy.Config{
					Mode:       domain.ModeAuto,
					ReplyScope: tt.scope,
				}, privateReplyThreadState{}),
				messenger,
			)
			result, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
				MessageID:   "om_outside_group",
				ChatID:      "oc_other_group",
				ChatType:    "group",
				SenderID:    "ou_other",
				InTestScope: false,
			}), domain.Decision{
				Kind:       domain.DecisionReply,
				Relevance:  domain.RelevanceDirectMention,
				Confidence: 0.99,
				Risk:       domain.RiskLow,
				ReplyText:  "收到，我先确认。",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Action.Status != tt.wantStatus || messenger.userReplies != tt.wantReplies {
				t.Fatalf("result=%+v messenger=%+v", result, messenger)
			}
		})
	}
}

func TestPrivateOwnerRequestUsesBotReplyPath(t *testing.T) {
	messenger := &privateReplyMessenger{}
	controller := reply.NewController(
		policy.NewReplyGate(policy.Config{
			Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
		}, privateReplyThreadState{}),
		messenger,
	)
	result, err := controller.Handle(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_private", ChatID: "oc_private", ChatType: "p2p",
		ChatPartnerID: "ou_bot", SenderID: "ou_owner",
	}), domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "现在是 05:40。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || messenger.botReplies != 1 || messenger.userReplies != 0 {
		t.Fatalf("result=%+v messenger=%+v", result, messenger)
	}
}

func TestPrivateOwnerAutoReplyWithDurableStoreDoesNotRequireApprovalHistory(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:     "om_private_durable_auto",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "请检查代码",
	})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	if err := store.MarkRetry(item.ID, "previous transient failure"); err != nil {
		t.Fatal(err)
	}
	messenger := &privateReplyMessenger{}
	controller := reply.NewController(
		policy.NewReplyGate(policy.Config{
			Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
		}, privateReplyThreadState{}),
		messenger,
		store,
	)
	result, err := controller.Handle(context.Background(), item, domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		Confidence: 0.99, Risk: domain.RiskLow, ReplyText: "已核对生产代码。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Status != domain.ActionCompleted || messenger.botReplies != 1 {
		t.Fatalf("result=%+v messenger=%+v", result, messenger)
	}
}

func TestApprovedAssistantReplyResumesWithBotIdentity(t *testing.T) {
	testApprovedReplyOutcome(t, domain.RelevanceAssistantRequest, false, "ou_owner", false)
}

func TestLegacyApprovedAssistantReplyResumesWithBotIdentity(t *testing.T) {
	testApprovedReplyOutcome(t, domain.RelevanceAssistantRequest, true, "ou_owner", false)
}

func TestApprovedDelegatedReplyResumesWithUserIdentityThenNotifiesOwner(t *testing.T) {
	testApprovedReplyOutcome(t, domain.RelevanceDirectMention, false, "ou_requester", false)
}

func TestApprovedNonOwnerAssistantReplyIsBlockedAfterAuthorizationChange(t *testing.T) {
	testApprovedReplyOutcome(t, domain.RelevanceAssistantRequest, false, "ou_requester", true)
}

func testApprovedReplyOutcome(
	t *testing.T,
	relevance domain.Relevance,
	legacy bool,
	senderID string,
	wantBlocked bool,
) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mentions := []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}}
	if relevance == domain.RelevanceAssistantRequest {
		mentions = []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}}
	}
	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:           domain.SourceRealtime,
		MessageID:        "om_approved_reply",
		ChatID:           "oc_any_group",
		ChatType:         "group",
		SenderID:         senderID,
		Mentions:         mentions,
		InAssistantScope: relevance == domain.RelevanceAssistantRequest,
	})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	persisted, ok, err := store.ClaimNext("approval-fixture")
	if err != nil || !ok {
		t.Fatalf("claim persisted item=%+v ok=%v err=%v", persisted, ok, err)
	}
	actionID, err := store.RequestReplyApproval(
		context.Background(),
		item.DedupKey,
		"已检查生产入口，初步确认缩略图信息由消息创建链路填充。",
		"source-backed approved reply",
		"确认是否还需继续追踪上传层。",
		relevance,
	)
	if err != nil {
		t.Fatal(err)
	}
	decision := domain.Decision{
		Kind:        domain.DecisionRequestApproval,
		Mode:        domain.ModeApproval,
		Relevance:   relevance,
		Confidence:  0.6,
		Risk:        domain.RiskLow,
		Reason:      "source-backed approved reply",
		ReplyText:   "已检查生产入口，初步确认缩略图信息由消息创建链路填充。",
		OwnerAction: "确认是否还需继续追踪上传层。",
	}
	if err := store.Complete(persisted.ID, decision); err != nil {
		t.Fatal(err)
	}
	if legacy {
		rewriteReplyApprovalAsLegacy(
			t,
			statePath,
			actionID,
			item.DedupKey,
			decision,
		)
	}
	if err := store.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	messenger := &privateReplyMessenger{}
	notifier := &approvalNotificationHandler{events: &messenger.events}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{
			OwnerOpenID:      "ou_owner",
			AssistantOpenIDs: []string{"ou_bot"},
			Mode:             domain.ModeAuto,
		}),
		app.WithReplyHandler(reply.NewController(
			policy.NewReplyGate(policy.Config{
				Mode:        domain.ModeAuto,
				OwnerOpenID: "ou_owner",
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
	if wantBlocked {
		if !result.Processed || result.Decision.Kind != domain.DecisionIgnore ||
			!strings.Contains(result.Decision.Reason, "assistant_request_from_non_owner") ||
			messenger.botReplies != 0 || messenger.userReplies != 0 || notifier.calls != 0 {
			t.Fatalf("blocked result=%+v messenger=%+v notifier=%+v", result, messenger, notifier)
		}
		approval, err := store.GetActionAttempt(actionID)
		if err != nil {
			t.Fatal(err)
		}
		if approval.Status != domain.ActionBlocked ||
			!strings.Contains(approval.Error, "assistant_request_from_non_owner") {
			t.Fatalf("blocked approval=%+v", approval)
		}
		return
	}
	if !result.Processed || result.Decision.Relevance != relevance {
		t.Fatalf("result=%+v messenger=%+v", result, messenger)
	}
	wantMessageID := "om_bot_reply"
	if relevance == domain.RelevanceAssistantRequest {
		if messenger.botReplies != 1 || messenger.userReplies != 0 ||
			notifier.calls != 0 || strings.HasPrefix(messenger.replyText, "🤖") {
			t.Fatalf("assistant result=%+v messenger=%+v notifier=%+v", result, messenger, notifier)
		}
	} else {
		wantMessageID = "om_user_reply"
		if messenger.userReplies != 1 || messenger.botReplies != 0 ||
			notifier.calls != 1 || !strings.HasPrefix(messenger.replyText, "🤖") ||
			strings.Join(messenger.events, ",") != "user_reply,notify" {
			t.Fatalf("delegated result=%+v messenger=%+v notifier=%+v", result, messenger, notifier)
		}
	}
	approval, err := store.GetActionAttempt(actionID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != domain.ActionCompleted ||
		!strings.Contains(approval.ResponseJSON, wantMessageID) {
		t.Fatalf("approval=%+v", approval)
	}
}

type approvalNotificationHandler struct {
	calls  int
	events *[]string
}

func (h *approvalNotificationHandler) HandleNotification(
	context.Context,
	domain.WorkItem,
	domain.Decision,
	string,
) error {
	h.calls++
	*h.events = append(*h.events, "notify")
	return nil
}

func rewriteReplyApprovalAsLegacy(
	t *testing.T,
	statePath string,
	actionID int64,
	dedupKey string,
	decision domain.Decision,
) {
	t.Helper()
	requestJSON, err := json.Marshal(map[string]string{
		"text":         decision.ReplyText,
		"reason":       decision.Reason,
		"owner_action": decision.OwnerAction,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(
		dedupKey + "\x00" + decision.ReplyText + "\x00" +
			decision.Reason + "\x00" + decision.OwnerAction,
	))
	legacyKey := fmt.Sprintf("reply:%x", sum[:])
	db, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(
		`UPDATE action_attempts SET request_json = ?, idempotency_key = ? WHERE id = ?`,
		string(requestJSON), legacyKey, actionID,
	); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateOwnerAvailabilityReplyCompletesWithoutModel(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueWorkItem(domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:     "om_private_availability",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: "ou_bot",
		SenderID:      "ou_owner",
		Content:       "在吗？",
	})); err != nil {
		t.Fatal(err)
	}
	messenger := &privateReplyMessenger{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{
			OwnerOpenID:      "ou_owner",
			AssistantOpenIDs: []string{"ou_bot"},
		}),
		app.WithReplyHandler(reply.NewController(
			policy.NewReplyGate(policy.Config{
				Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
			}, privateReplyThreadState{}),
			messenger,
			store,
		)),
		app.WithOwnerActivityHandler(feedback.NewController(messenger, store)),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed ||
		result.Decision.Kind != domain.DecisionReply ||
		result.Decision.WorkKind != domain.WorkKindFastPath ||
		result.Decision.ReplyText != "在的。" {
		t.Fatalf("result=%+v", result)
	}
	if strings.Join(messenger.events, ",") != "working_reaction_on,reply,working_reaction_off" ||
		messenger.botReplies != 1 ||
		messenger.userReplies != 0 {
		t.Fatalf("events=%v messenger=%+v", messenger.events, messenger)
	}
}

func TestGroupOwnerAvailabilityMentionCompletesWithoutModel(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.EnqueueWorkItem(domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_group_availability",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_owner",
		Mentions:  []domain.Mention{{Key: "@_user_1", OpenID: "ou_bot", Name: "Lark Agent"}},
		Content:   "@_user_1 在吗？",
	})); err != nil {
		t.Fatal(err)
	}
	messenger := &privateReplyMessenger{}
	daemon := app.NewDaemon(
		store,
		router.New(router.Config{
			OwnerOpenID:      "ou_owner",
			AssistantOpenIDs: []string{"ou_bot"},
			AssistantNames:   []string{"Lark Agent"},
		}),
		app.WithReplyHandler(reply.NewController(
			policy.NewReplyGate(policy.Config{
				Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
			}, privateReplyThreadState{}),
			messenger,
			store,
		)),
		app.WithOwnerActivityHandler(feedback.NewController(messenger, store)),
	)
	result, err := daemon.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Processed ||
		result.Decision.Kind != domain.DecisionReply ||
		result.Decision.WorkKind != domain.WorkKindFastPath ||
		result.Decision.ReplyText != "在的。" {
		t.Fatalf("result=%+v", result)
	}
	if strings.Join(messenger.events, ",") != "working_reaction_on,reply,working_reaction_off" ||
		messenger.botReplies != 1 ||
		messenger.userReplies != 0 {
		t.Fatalf("events=%v messenger=%+v", messenger.events, messenger)
	}
}

func TestOwnerPrivateFeedbackAndReplyAreAudited(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_private_audit", ChatID: "oc_private", ChatType: "p2p",
		ChatPartnerID: "ou_bot", SenderID: "ou_owner",
	})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	messenger := &privateReplyMessenger{}
	activity := feedback.NewController(messenger, store)
	token, err := activity.Begin(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	controller := reply.NewController(
		policy.NewReplyGate(policy.Config{
			Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
		}, privateReplyThreadState{}),
		messenger,
		store,
	)
	if _, err := controller.Handle(context.Background(), item, domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		Confidence: 1, Risk: domain.RiskLow, ReplyText: "现在是 08:09。",
	}); err != nil {
		t.Fatal(err)
	}
	if err := activity.End(context.Background(), item, token); err != nil {
		t.Fatal(err)
	}
	actions, err := store.ListActionAttempts()
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]domain.ActionStatus{}
	for _, action := range actions {
		statuses[action.Kind] = action.Status
	}
	if messenger.botReplies != 1 || messenger.reactions != 1 || messenger.deletions != 1 ||
		statuses["reply"] != domain.ActionCompleted ||
		statuses["owner_activity"] != domain.ActionCompleted {
		t.Fatalf("messenger=%+v statuses=%+v", messenger, statuses)
	}
}

func runAgent(t *testing.T, bin string, args ...string) (int, string, string) {
	return runAgentWithEnv(t, nil, bin, args...)
}

func runAgentWithEnv(t *testing.T, extraEnv []string, bin string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = cleanAgentTestEnv(os.Environ())
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %v failed: %v", args, err)
		}
	}
	return code, stdout.String(), stderr.String()
}

func cleanAgentTestEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		switch {
		case strings.HasPrefix(item, "LARKSUITE_CLI_"),
			strings.HasPrefix(item, "LARK_AGENT_"),
			strings.HasPrefix(item, "GITHUB_ACTIONS="),
			strings.HasPrefix(item, "GITHUB_EVENT_"),
			strings.HasPrefix(item, "GITHUB_REPOSITORY="),
			strings.HasPrefix(item, "GITHUB_API_URL="),
			strings.HasPrefix(item, "GITHUB_WORKSPACE="),
			strings.HasPrefix(item, "GITHUB_TOKEN="):
			continue
		}
		out = append(out, item)
	}
	return out
}

func TestHelpContract(t *testing.T) {
	bin := buildAgentBinary(t)
	code, stdout, stderr := runAgent(t, bin, "--help")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"lark-agent",
		"daemon",
		"workspace",
		"mode",
		"model",
		"Only the configured owner can mention the assistant bot",
		"assistant bot",
		"independent all-groups or configured-groups scopes",
		"keyboard working reaction",
		"coding investigation",
		"fast path",
		"Time, date, ping",
		"3 model turns",
		"16 tool calls",
		"interactive",
		"queue summary",
		"approval",
		"auto",
		"approval",
		"paused",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help output missing %q\nstdout:\n%s", want, stdout)
		}
	}
	code, _, stderr = runAgent(t, bin, "event", "--help")
	if code == 0 || !strings.Contains(stderr, "unknown command") ||
		!strings.Contains(stderr, "event") {
		t.Fatalf("copied event bus unexpectedly available: exit=%d stderr=%s", code, stderr)
	}
}

func TestBehaviorSpecDocumentsCodingAssistanceContract(t *testing.T) {
	specPath := filepath.Join(repoRoot(t), "spec", "behavior.md")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	spec := string(data)
	for _, want := range []string{
		"## Coding Assistance",
		"## Harness Architecture",
		"fast-path owner work",
		"scheduler claiming still runs",
		"duplicate item links to that result",
		"work-kind specific",
		"unconstrained `find`",
		"CodingQuestion",
		"CodingGoal",
		"investigation plan",
		"receipt-equivalent audit record",
		"fail soft for coding investigations",
		"verify gate checks three properties",
		"completeness",
		"correctness",
		"coherence",
		"owner-request work item",
		"owner-only",
		"keyboard working reaction",
		"`im.message.receive_v1`",
		"without waiting for the next user-token poll",
		"## Conversation Context",
		"direct parent is authoritative",
		"never imports another group/private chat",
		"root, directly referenced message, and target message",
		"missing executor",
		"durable idempotent reply actions",
		"`offline_backlog`",
		"`interrupted`",
		"`queue inspect --work-id <id>`",
		"`queue resume`",
		"never blindly repeated",
		"private offline notice",
		"private online notice",
	} {
		if !strings.Contains(spec, want) {
			t.Fatalf("behavior spec missing %q", want)
		}
	}
}

func TestSchedulerProductionContractCrossesRouterAndStorage(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureScheduler(2*time.Minute, 3)
	r := router.New(router.Config{
		OwnerOpenID: "owner", AssistantOpenIDs: []string{"assistant"}, AssistantNames: []string{"Agent"},
	})
	events := []domain.NormalizedEvent{
		{
			MessageID: "om_goal", ChatID: "private", ChatType: "p2p", SenderID: "owner",
			Content:  "@Agent 请后台处理代码问题，完成后通知我",
			Mentions: []domain.Mention{{OpenID: "assistant", Name: "Agent"}},
		},
		{
			MessageID: "om_fast", ChatID: "group", ChatType: "group", SenderID: "owner",
			Content:  "@Agent ping",
			Mentions: []domain.Mention{{OpenID: "assistant", Name: "Agent"}},
		},
	}
	for _, event := range events {
		item := domain.NewWorkItem(event)
		decision, err := r.Route(context.Background(), item)
		if err != nil {
			t.Fatal(err)
		}
		item.WorkKind = decision.WorkKind
		item.Priority = decision.Priority
		if _, err := store.EnqueueWorkItem(item); err != nil {
			t.Fatal(err)
		}
	}
	foreground, ok, err := store.ClaimNextForLane("foreground", domain.SchedulerLaneForeground)
	if err != nil || !ok || foreground.WorkKind != domain.WorkKindFastPath {
		t.Fatalf("foreground=%+v ok=%v err=%v", foreground, ok, err)
	}
	background, ok, err := store.ClaimNextForLane("background", domain.SchedulerLaneBackground)
	if err != nil || !ok || background.WorkKind != domain.WorkKindCodingGoal {
		t.Fatalf("background=%+v ok=%v err=%v", background, ok, err)
	}
}

func TestApprovalCommandRoundTrip(t *testing.T) {
	bin := buildAgentBinary(t)
	state := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_integration_approval", Content: "run formatter"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	actionID, err := store.RequestShellApproval(context.Background(), domain.DedupKey(event), "gofmt -w .", ".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	code, stdout, stderr := runAgent(t, bin, "--state", state, "approval", "list")
	if code != 0 || !strings.Contains(stdout, `"status":"awaiting_approval"`) {
		t.Fatalf("list exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	writerDB, err := sql.Open("sqlite", state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })
	writer, err := writerDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(
		`UPDATE work_items SET updated_at = ? WHERE dedup_key = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), domain.DedupKey(event),
	); err != nil {
		_ = writer.Rollback()
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(
		ctx, bin, "--state", state, "approval", "approve", fmt.Sprint(actionID),
	)
	cmd.Env = cleanAgentTestEnv(os.Environ())
	var approveOut, approveErr strings.Builder
	cmd.Stdout = &approveOut
	cmd.Stderr = &approveErr
	if err := cmd.Start(); err != nil {
		_ = writer.Rollback()
		t.Fatal(err)
	}
	commandDone := make(chan error, 1)
	go func() { commandDone <- cmd.Wait() }()
	time.Sleep(100 * time.Millisecond)
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	err = <-commandDone
	code = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	stdout, stderr = approveOut.String(), approveErr.String()
	if code != 0 || !strings.Contains(stdout, `"action":"approve"`) {
		t.Fatalf("approve exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	action, err := store.GetActionAttempt(actionID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusReceived {
		t.Fatalf("items=%+v", items)
	}
}

func TestQueueExportRunTranscriptCommand(t *testing.T) {
	bin := buildAgentBinary(t)
	state := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_export", Content: "查代码"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "model", "config")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAgentStep(context.Background(), domain.AgentStep{
		RunID:      run.ID,
		Sequence:   1,
		Kind:       "tool",
		ToolName:   "search_workspace",
		OutputJSON: `{"ok":true}`,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAgent(t, bin, "--state", state, "queue", "export", "--run-id", run.ID)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"format":"jsonl"`) || !strings.Contains(stdout, "search_workspace") {
		t.Fatalf("stdout=%s", stdout)
	}
}

func TestDaemonRunHelpDocumentsLiveFlags(t *testing.T) {
	bin := buildAgentBinary(t)
	code, stdout, stderr := runAgent(t, bin, "daemon", "run", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"--live",
		"--dry-run",
		"--chat-query",
		"--poll-interval",
		"--include-private",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("daemon run help missing %q\nstdout:\n%s", want, stdout)
		}
	}
}

func TestDaemonHelpDocumentsInstallAppControls(t *testing.T) {
	bin := buildAgentBinary(t)
	code, stdout, stderr := runAgent(t, bin, "daemon", "--help")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"install-app", "status", "start", "stop", "restart", "uninstall"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("daemon help missing %q\nstdout:\n%s", want, stdout)
		}
	}
}

func TestDaemonInstallAppPreviewDoesNotWrite(t *testing.T) {
	bin := buildAgentBinary(t)
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	state := filepath.Join(t.TempDir(), "state.db")
	root := t.TempDir()
	code, _, stderr := runAgentWithEnv(t, []string{"HOME=" + home}, bin, "--config", cfg, "init", "--workspace", root, "--app-id", "cli_test", "--owner-open-id", "ou_owner")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	code, stdout, stderr := runAgentWithEnv(t, []string{"HOME=" + home}, bin,
		"--config", cfg,
		"--state", state,
		"daemon", "install-app",
		"--program", bin)
	if code != 0 {
		t.Fatalf("install-app exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"written":false`) || !strings.Contains(stdout, `"--state"`) || !strings.Contains(stdout, `"--chat-query"`) {
		t.Fatalf("unexpected preview stdout:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", "com.liuchong.lark-agent.plist")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote plist unexpectedly: %v", err)
	}
}

func TestDaemonRunRejectsNonPositivePollInterval(t *testing.T) {
	bin := buildAgentBinary(t)
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "agent.yaml")
	statePath := filepath.Join(t.TempDir(), "state.db")
	code, _, stderr := runAgent(t, bin,
		"--config", cfgPath,
		"init", "--workspace", root, "--app-id", "cli_test", "--owner-open-id", "ou_owner")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runAgent(t, bin,
		"--config", cfgPath,
		"--state", statePath,
		"daemon", "run", "--poll-interval", "0", "--once")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--poll-interval") {
		t.Fatalf("stderr missing poll interval error: %s", stderr)
	}
}

func TestInitWritesDeepAgentTurnBudget(t *testing.T) {
	bin := buildAgentBinary(t)
	cfgPath := filepath.Join(t.TempDir(), "agent.yaml")
	code, _, stderr := runAgent(t, bin,
		"--config", cfgPath,
		"init", "--workspace", t.TempDir(), "--app-id", "cli_test", "--owner-open-id", "ou_owner")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := string(data)
	for _, want := range []string{"max_turns: 150", "loop_timeout: 2h0m0s"} {
		if !strings.Contains(configText, want) {
			t.Fatalf("config missing %q:\n%s", want, configText)
		}
	}
}

func TestOwnerAssistantContractIsSharedByPromptAndDecisionTool(t *testing.T) {
	prompt := agentcontext.AgentSystemPrompt()
	decisionTool := agentruntime.SubmitDecisionDefinition()
	decisionSchema, err := decisionTool.Info.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	schemaJSON, err := json.Marshal(decisionSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"two explicit Lark roles",
		"assistant_request",
		"answer the configured owner as the assistant bot",
		"directly mentions the owner",
		"owner_request",
		"act on behalf of that owner",
		"Never answer a non-owner direct assistant invocation",
		"complete bounded relevant read-only work",
		"concrete business questions",
		"run is read-only",
		"read_workspace",
		"coding question",
		"submit_investigation_plan",
		"结论、依据、未知/下一步",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, want := range []string{
		"ignore only irrelevant content",
		"completed bounded read work",
		"assistant_request",
		"owner_request",
		"acknowledgement or restatement",
		"initial finding",
		"assistant_request and owner_request cannot finish as notify only",
		"evidence_status",
		"canonical evidence-limited response",
		"Lark mention placeholders",
		"The runtime renders known mention placeholders",
		"coding replies",
		"结论、依据、未知/下一步",
		"source_refs",
		`"owner_action"`,
		`"enum":["low","medium","high","forbidden"]`,
	} {
		if !strings.Contains(string(schemaJSON), want) {
			t.Fatalf("decision schema missing %q:\n%s", want, schemaJSON)
		}
	}
}

func TestOwnerAssistantInvocationRoutesOnlyOwner(t *testing.T) {
	r := router.New(router.Config{
		OwnerOpenID:         "ou_owner",
		AssistantOpenIDs:    []string{"ou_bot"},
		AssistantNames:      []string{"Lark Agent"},
		AssistantReplyScope: domain.ReplyScopeAllGroups,
		Mode:                domain.ModeAuto,
	})
	owner, err := r.Route(context.Background(), domain.WorkItem{Event: domain.NormalizedEvent{
		MessageID: "om_owner_bot",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_owner",
		Mentions:  []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
		Content:   "@Lark Agent 帮我回答这个编程问题",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Kind != domain.DecisionNotify || owner.Relevance != domain.RelevanceAssistantRequest {
		t.Fatalf("owner decision=%+v", owner)
	}
	other, err := r.Route(context.Background(), domain.WorkItem{Event: domain.NormalizedEvent{
		MessageID: "om_other_bot",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_other",
		Mentions:  []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
		Content:   "@Lark Agent 帮我回答这个编程问题",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if other.Kind != domain.DecisionIgnore ||
		other.Relevance != domain.RelevanceNone ||
		other.Reason != "assistant_request_from_non_owner" {
		t.Fatalf("other decision=%+v", other)
	}
	delegated, err := r.Route(context.Background(), domain.WorkItem{Event: domain.NormalizedEvent{
		MessageID: "om_other_owner",
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_other",
		Mentions:  []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}},
		Content:   "@测试负责人 帮忙确认这个接口",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if delegated.Kind != domain.DecisionNotify ||
		delegated.Relevance != domain.RelevanceDirectMention ||
		delegated.Reason != "direct_mention" {
		t.Fatalf("delegated decision=%+v", delegated)
	}
	private, err := r.Route(context.Background(), domain.WorkItem{Event: domain.NormalizedEvent{
		MessageID: "om_other_private",
		ChatID:    "oc_private",
		ChatName:  "Lark Agent",
		ChatType:  "p2p",
		SenderID:  "ou_other",
		Content:   "帮我确认这个接口",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if private.Kind != domain.DecisionIgnore ||
		private.Relevance != domain.RelevanceNone ||
		private.Reason != "assistant_request_from_non_owner" {
		t.Fatalf("private decision=%+v", private)
	}
}

func TestInvalidAssistantReplyScopeFailsAtConfigLoad(t *testing.T) {
	bin := buildAgentBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.yaml")
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgent(t, bin,
		"--config", configPath,
		"init",
		"--workspace", workspace,
		"--app-id", "cli_test",
		"--owner-open-id", "ou_owner",
	)
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := bytes.Replace(data, []byte("reply_scope: all_groups"), []byte("reply_scope: test_chat"), 1)
	if bytes.Equal(invalid, data) {
		t.Fatalf("initialized config missing explicit reply_scope:\n%s", data)
	}
	if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runAgent(t, bin, "--config", configPath, "config", "show")
	if code == 0 ||
		!strings.Contains(stderr, "assistant.reply_scope") ||
		!strings.Contains(stderr, "test_chat") {
		t.Fatalf("config show exit=%d stderr=%s", code, stderr)
	}
}

func TestInvalidDelegatedReplyScopeFailsAtConfigLoad(t *testing.T) {
	bin := buildAgentBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.yaml")
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgent(t, bin,
		"--config", configPath,
		"init",
		"--workspace", workspace,
		"--app-id", "cli_test",
		"--owner-open-id", "ou_owner",
	)
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	old := []byte("reply_scope: all_groups")
	index := bytes.LastIndex(data, old)
	if index < 0 {
		t.Fatalf("initialized config missing delegated reply_scope:\n%s", data)
	}
	invalid := append([]byte(nil), data[:index]...)
	invalid = append(invalid, []byte("reply_scope: test_chat")...)
	invalid = append(invalid, data[index+len(old):]...)
	if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runAgent(t, bin, "--config", configPath, "config", "show")
	if code == 0 ||
		!strings.Contains(stderr, "policy.reply_scope") ||
		!strings.Contains(stderr, "test_chat") {
		t.Fatalf("config show exit=%d stderr=%s", code, stderr)
	}
}

func TestConfiguredGroupsScopeWithoutChatQueryFailsLiveStartup(t *testing.T) {
	bin := buildAgentBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.yaml")
	statePath := filepath.Join(dir, "state.db")
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgent(t, bin,
		"--config", configPath,
		"init",
		"--workspace", workspace,
		"--app-id", "cli_test",
		"--owner-open-id", "ou_owner",
	)
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Policy.ReplyScope = domain.ReplyScopeConfiguredGroups
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runAgentWithEnv(t, []string{"LARK_AGENT_OFFLINE_LIVE_TEST=1"}, bin,
		"--config", configPath,
		"--state", statePath,
		"daemon", "run",
		"--live",
		"--once",
	)
	if code == 0 ||
		!strings.Contains(stderr, "policy.reply_scope") ||
		!strings.Contains(stderr, "--chat-query") {
		t.Fatalf("daemon run exit=%d stderr=%s", code, stderr)
	}
}

func TestConfiguredAssistantScopeWithoutChatQueryFailsLiveStartup(t *testing.T) {
	bin := buildAgentBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.yaml")
	statePath := filepath.Join(dir, "state.db")
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgent(t, bin,
		"--config", configPath,
		"init",
		"--workspace", workspace,
		"--app-id", "cli_test",
		"--owner-open-id", "ou_owner",
	)
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Assistant.ReplyScope = domain.ReplyScopeConfiguredGroups
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runAgentWithEnv(t, []string{"LARK_AGENT_OFFLINE_LIVE_TEST=1"}, bin,
		"--config", configPath,
		"--state", statePath,
		"daemon", "run",
		"--live",
		"--once",
	)
	if code == 0 ||
		!strings.Contains(stderr, "assistant.reply_scope") ||
		!strings.Contains(stderr, "--chat-query") {
		t.Fatalf("daemon run exit=%d stderr=%s", code, stderr)
	}
}

func TestDoctorReportsAssistantOwnerDirect(t *testing.T) {
	bin := buildAgentBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "agent.yaml")
	statePath := filepath.Join(dir, "state.db")
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgent(t, bin, "--config", configPath, "init", "--workspace", workspace, "--app-id", "cli_test", "--owner-open-id", "ou_owner")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Assistant.OwnerDirect.Enabled = false
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	env := []string{"LARK_AGENT_APP_SECRET=redacted-test-value-one", "LARK_AGENT_USER_ACCESS_TOKEN=redacted-test-value-two"}
	code, stdout, stderr := runAgentWithEnv(t, env, bin,
		"--config", configPath,
		"--state", statePath,
		"doctor",
	)
	if code != 0 {
		t.Fatalf("doctor exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"assistant"`) ||
		!strings.Contains(stdout, `"owner_direct_enabled":false`) ||
		!strings.Contains(stdout, `"assistant_mentions":"all_groups"`) ||
		!strings.Contains(stdout, `"owner_mentions":"all_groups"`) ||
		!strings.Contains(stdout, `"reply_scope":"all_groups"`) ||
		!strings.Contains(stdout, `"github"`) ||
		!strings.Contains(stdout, `"enabled":false`) ||
		!strings.Contains(stdout, `"read_only":true`) ||
		!strings.Contains(stdout, `"single_lark_listener":true`) {
		t.Fatalf("doctor missing assistant owner direct state:\n%s", stdout)
	}
}

func TestInitRequiresAbsoluteWorkspace(t *testing.T) {
	bin := buildAgentBinary(t)
	code, _, stderr := runAgent(t, bin, "init", "--workspace", "relative/path")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("stderr is not JSON envelope: %v\n%s", err, stderr)
	}
	if env.OK || env.Error.Type != "validation" || env.Error.Subtype != "invalid_argument" || env.Error.Param != "--workspace" {
		t.Fatalf("unexpected error envelope: %s", stderr)
	}
}

func TestDoctorRejectsWorkspaceSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	bin := buildAgentBinary(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "notes.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgent(t, bin, "workspace", "validate", "--workspace", root, "--probe", "escape/notes.txt")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "outside workspace") {
		t.Fatalf("expected workspace escape error, got:\n%s", stderr)
	}
}

func TestDaemonRunUsesExplicitStatePath(t *testing.T) {
	bin := buildAgentBinary(t)
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	state := filepath.Join(t.TempDir(), "agent-state.db")

	code, stdout, stderr := runAgent(t, bin, "--config", cfg, "init", "--workspace", root, "--app-id", "cli_test", "--owner-open-id", "ou_owner")
	if code != 0 {
		t.Fatalf("init exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgent(t, bin, "--config", cfg, "--state", state, "daemon", "run", "--once")
	if code != 0 {
		t.Fatalf("daemon run exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("explicit state database was not created: %v", err)
	}
	if !strings.Contains(stdout, `"ready":true`) {
		t.Fatalf("daemon run did not report readiness:\n%s", stdout)
	}
}

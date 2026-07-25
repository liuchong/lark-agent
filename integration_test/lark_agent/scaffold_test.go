package larkagent_test

import (
	"bytes"
	"context"
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
	"github.com/liuchong/lark-agent/agent/realtime"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/router"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	"github.com/liuchong/lark-agent/agent/storage"
	"github.com/liuchong/lark-agent/agent/tools"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

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
			policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, privateReplyThreadState{}),
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
	userReplies int
	botReplies  int
	reactions   int
	deletions   int
	events      []string
}

func (m *privateReplyMessenger) ReplyAsUser(context.Context, tools.ReplyRequest) (tools.ReplyResult, error) {
	m.userReplies++
	return tools.ReplyResult{MessageID: "om_user_reply"}, nil
}

func (m *privateReplyMessenger) ReplyAsBot(context.Context, tools.ReplyRequest) (tools.ReplyResult, error) {
	m.botReplies++
	m.events = append(m.events, "reply")
	return tools.ReplyResult{MessageID: "om_bot_reply"}, nil
}

func (*privateReplyMessenger) NotifyOwner(context.Context, tools.NotifyRequest) error {
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

func TestPrivateOwnerRequestUsesBotReplyPath(t *testing.T) {
	messenger := &privateReplyMessenger{}
	controller := reply.NewController(
		policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, privateReplyThreadState{}),
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
		policy.NewReplyGate(policy.Config{Mode: domain.ModeAuto}, privateReplyThreadState{}),
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
		if strings.HasPrefix(item, "LARKSUITE_CLI_") {
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
		"owner-request",
		"assistant bot",
		"keyboard working reaction",
		"coding investigation",
		"fast path",
		"Time, date, ping",
		"2 model turns",
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAgent(t, bin, "--state", state, "approval", "list")
	if code != 0 || !strings.Contains(stdout, `"status":"awaiting_approval"`) {
		t.Fatalf("list exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgent(t, bin, "--state", state, "approval", "approve", fmt.Sprint(actionID))
	if code != 0 || !strings.Contains(stdout, `"action":"approve"`) {
		t.Fatalf("approve exit=%d stdout=%s stderr=%s", code, stdout, stderr)
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
		"configured human owner",
		"directly mentions the owner",
		"owner_request",
		"owner-only entry point",
		"not a group bot persona",
		"prefer reply",
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
		"owner-irrelevant",
		"status update, handoff, or coordination request",
		"owner_request",
		"safe useful owner response",
		"remaining owner work",
		"privately notifies the owner",
		"direct owner mentions cannot finish as notify only",
		"incomplete facts should be stated as unknowns in reply_text",
		"Lark mention placeholders",
		"runtime renders known mention placeholders",
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
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		AssistantNames:   []string{"Lark Agent"},
		Mode:             domain.ModeAuto,
	})
	owner, err := r.Route(context.Background(), domain.WorkItem{Event: domain.NormalizedEvent{
		MessageID: "om_owner_bot",
		ChatID:    "oc_group",
		SenderID:  "ou_owner",
		Mentions:  []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
		Content:   "@Lark Agent 帮我回答这个编程问题",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if owner.Kind != domain.DecisionNotify || owner.Relevance != domain.RelevanceOwnerRequest {
		t.Fatalf("owner decision=%+v", owner)
	}
	other, err := r.Route(context.Background(), domain.WorkItem{Event: domain.NormalizedEvent{
		MessageID: "om_other_bot",
		ChatID:    "oc_group",
		SenderID:  "ou_other",
		Mentions:  []domain.Mention{{OpenID: "ou_bot", Name: "Lark Agent"}},
		Content:   "@Lark Agent 帮我回答这个编程问题",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if other.Kind != domain.DecisionIgnore || other.Reason != "assistant_request_from_non_owner" {
		t.Fatalf("other decision=%+v", other)
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
	if !strings.Contains(stdout, `"assistant"`) || !strings.Contains(stdout, `"owner_direct_enabled":false`) {
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

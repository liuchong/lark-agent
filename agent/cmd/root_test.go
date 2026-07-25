package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/config"
	"github.com/liuchong/lark-agent/agent/domain"
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

func TestAuthStatusReportsMissingUserTokenSeparately(t *testing.T) {
	t.Setenv("LARK_AGENT_APP_SECRET", "super-secret-value")
	cfg := config.Default()
	cfg.Lark.AppID = "cli_a"
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
	}}, store, r, cfg, "Test Group", true)
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
		!strings.Contains(contract.SubmitDecisionSchema, "unknowns remain") {
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
	for _, path := range [][]string{{"queue", "inspect"}, {"queue", "resume"}} {
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

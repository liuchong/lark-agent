package larkagent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/config"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/storage"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

func TestGitHubNotifyDryRunEmitsStructuredPostWithoutSecrets(t *testing.T) {
	bin := buildAgentBinary(t)
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs/981"):
			_, _ = w.Write([]byte(`{"id":981,"name":"verify","status":"completed","conclusion":"failure"}`))
		case strings.HasSuffix(r.URL.Path, "/actions/runs/981/jobs"):
			_, _ = w.Write([]byte(`{"total_count":2,"jobs":[{"id":1,"name":"unit","status":"completed","conclusion":"failure"}]}`))
		case strings.HasSuffix(r.URL.Path, "/pulls/42/files"):
			_, _ = w.Write([]byte(`[{"filename":"a.go","patch":"12345678"},{"filename":"b.go","patch":"90"}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls/42/reviews"):
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	code, _, stderr := runAgent(t, bin,
		"--config", cfgPath,
		"init", "--workspace", root, "--app-id", "cli_synthetic", "--owner-open-id", "ou_owner", "--owner-name", "测试负责人")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.Enabled = true
	cfg.GitHub.APIBaseURL = server.URL
	cfg.GitHub.AllowedRepositories = []string{"example/widgets"}
	cfg.GitHub.MaxFiles = 1
	cfg.GitHub.MaxPatchBytes = 4
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(t.TempDir(), "event.json")
	event := `{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":2,"name":"verify",
	    "status":"completed","conclusion":"failure",
	    "head_sha":"0123456789abcdef0123456789abcdef01234567",
	    "html_url":"https://github.example/example/widgets/actions/runs/981",
	    "pull_requests":[{"number":42}]
	  }
	}`
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAgentWithEnv(t, []string{
		"GITHUB_EVENT_NAME=workflow_run",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=must-not-appear",
		"LARK_AGENT_APP_SECRET=must-not-appear",
	}, bin, "--config", cfgPath, "github", "notify", "--chat-id", "oc_synthetic", "--dry-run")
	if code != 0 {
		t.Fatalf("notify exit=%d stderr=%s", code, stderr)
	}
	for _, secret := range []string{"must-not-appear"} {
		if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
			t.Fatalf("secret leaked stdout=%s stderr=%s", stdout, stderr)
		}
	}
	var envelope struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if !envelope.OK ||
		envelope.Data["dry_run"] != true ||
		envelope.Data["chat_id"] != "oc_synthetic" ||
		envelope.Data["message_type"] != "post" ||
		envelope.Data["partial"] != true ||
		envelope.Data["idempotency_key"] == "" {
		t.Fatalf("result=%+v", envelope)
	}
	if !strings.Contains(envelope.Data["content"].(string), "jobs=1") ||
		!strings.Contains(envelope.Data["content"].(string), "files=1") {
		t.Fatalf("notification did not report known omissions: %+v", envelope.Data)
	}
}

func TestGitHubActionNotifyDryRunWorksWithoutLocalConfig(t *testing.T) {
	bin := buildAgentBinary(t)
	workspace := t.TempDir()
	eventPath := filepath.Join(t.TempDir(), "event.json")
	event := `{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":2,"name":"verify",
	    "status":"completed","conclusion":"failure",
	    "head_sha":"0123456789abcdef0123456789abcdef01234567",
	    "html_url":"https://github.example/example/widgets/actions/runs/981",
	    "pull_requests":[{"number":42}]
	  }
	}`
	if err := os.WriteFile(eventPath, []byte(event), 0o600); err != nil {
		t.Fatal(err)
	}
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	code, stdout, stderr := runAgentWithEnv(t, []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_API_URL=https://api.github.example",
		"GITHUB_EVENT_NAME=workflow_run",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-lark-app-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
	}, bin, "--config", missingConfig, "github", "notify", "--chat-id", "oc_synthetic", "--dry-run")
	if code != 0 {
		t.Fatalf("notify exit=%d stderr=%s", code, stderr)
	}
	var envelope struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if !envelope.OK || envelope.Data["dry_run"] != true || envelope.Data["partial"] != true {
		t.Fatalf("result=%+v stderr=%s", envelope, stderr)
	}

	code, _, stderr = runAgentWithEnv(t, []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_API_URL=https://api.github.example",
		"GITHUB_EVENT_NAME=workflow_run",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-lark-app-secret",
		"LARK_AGENT_LARK_BASE_URL=",
	}, bin, "--config", missingConfig, "github", "notify", "--chat-id", "oc_synthetic", "--dry-run")
	if code == 0 || !strings.Contains(stderr, "LARK_AGENT_LARK_BASE_URL") {
		t.Fatalf("missing Lark domain exit=%d stderr=%s", code, stderr)
	}
}

func TestGitHubWorkflowUsesOnlyTrustedDefaultBranchInputs(t *testing.T) {
	root := repoRoot(t)
	actionData, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	actionText := string(actionData)
	for _, want := range []string{
		"using: docker",
		"image: Dockerfile",
		"LARK_AGENT_APP_SECRET",
		"lark_base_url:",
		"GITHUB_TOKEN",
	} {
		if !strings.Contains(actionText, want) {
			t.Fatalf("action.yml missing %q:\n%s", want, actionText)
		}
	}

	workflowData, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "lark-notify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflowText := string(workflowData)
	for _, want := range []string{
		"workflow_run:",
		"actions: read",
		"checks: read",
		"contents: read",
		"pull-requests: read",
		"environment: lark-production",
		"ref: ${{ github.event.repository.default_branch }}",
		"persist-credentials: false",
		"uses: ./",
		"lark_base_url: ${{ vars.LARK_BASE_URL }}",
	} {
		if !strings.Contains(workflowText, want) {
			t.Fatalf("workflow missing %q:\n%s", want, workflowText)
		}
	}
	for _, forbidden := range []string{
		"pull_request_target",
		"workflow_run.head",
		"head_sha",
		"download-artifact",
		"/artifacts",
		"\n        run:",
	} {
		if strings.Contains(workflowText, forbidden) {
			t.Fatalf("workflow contains untrusted execution surface %q:\n%s", forbidden, workflowText)
		}
	}
}

func TestVerifiedGitHubReplyChainSurvivesStorageAndFeedsReadOnlyTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/widgets/actions/runs/981" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "id":981,"run_attempt":2,"name":"verify","status":"completed",
		  "conclusion":"failure","head_sha":"0123456789abcdef0123456789abcdef01234567",
		  "html_url":"https://github.example/run/981"
		}`))
	}))
	defer server.Close()
	client, err := internalgithub.NewClient(internalgithub.ClientConfig{
		BaseURL: server.URL,
		Token:   "synthetic-token",
		Limits:  internalgithub.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.GitHubReference{
		SchemaVersion:      1,
		Repository:         "example/widgets",
		Kind:               domain.GitHubReferenceWorkflowRun,
		WorkflowRunID:      981,
		WorkflowRunAttempt: 2,
	}
	marker, err := internalgithub.EncodeReferenceMarker(ref, "synthetic-lark-app-secret")
	if err != nil {
		t.Fatal(err)
	}
	target := domain.NormalizedEvent{
		MessageID:        "om_question",
		ChatID:           "oc_synthetic",
		SenderID:         "ou_other",
		SenderType:       "user",
		ReplyToMessageID: "om_notification",
	}
	verified, ok, err := agentcontext.ResolveGitHubReference(target, []domain.NormalizedEvent{
		{
			MessageID: "om_notification", ChatID: "oc_synthetic",
			SenderID: "cli_current", SenderType: "app", Content: marker,
		},
		target,
	}, "cli_current", []string{"example/widgets"}, "synthetic-lark-app-secret")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	store, err := storage.OpenInspection(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	persisted, err := store.UpsertExternalReference(context.Background(), verified)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(agenttools.GitHubContextDefinition(client))
	if err != nil {
		t.Fatal(err)
	}
	scope := agenttools.InvocationScope{
		Owner: false, ReadOnly: true, ChatID: target.ChatID,
		GitHubReference: &persisted.Reference,
	}
	ctx := agenttools.WithInvocationScope(context.Background(), scope)
	execution, err := registry.Execute(ctx, "get_github_context", []byte(`{"sections":["summary"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(execution.Content, `"failure"`) ||
		len(execution.Sources) != 1 ||
		execution.Sources[0].Kind != "github" {
		t.Fatalf("execution=%+v", execution)
	}
}

func TestSpoofedGitHubMarkerCannotEnableReadOnlyTool(t *testing.T) {
	ref := domain.GitHubReference{
		SchemaVersion: 1, Repository: "example/widgets",
		Kind: domain.GitHubReferenceWorkflowRun, WorkflowRunID: 981,
	}
	marker, _ := internalgithub.EncodeReferenceMarker(ref, "synthetic-lark-app-secret")
	target := domain.NormalizedEvent{
		MessageID: "om_q", ChatID: "oc_synthetic", ReplyToMessageID: "om_spoof",
	}
	_, ok, err := agentcontext.ResolveGitHubReference(target, []domain.NormalizedEvent{{
		MessageID: "om_spoof", ChatID: "oc_synthetic",
		SenderID: "ou_attacker", SenderType: "user", Content: marker,
	}}, "cli_current", []string{"example/widgets"}, "synthetic-lark-app-secret")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("spoofed reference was trusted")
	}
}

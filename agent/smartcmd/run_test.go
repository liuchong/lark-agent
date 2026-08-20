package smartcmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/config"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

type recordingModel struct {
	calls     int
	responses []*schema.Message
	bodies    []string
	toolNames []string
}

func (m *recordingModel) Generate(_ context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	m.calls++
	for _, msg := range input {
		if msg == nil {
			continue
		}
		m.bodies = append(m.bodies, msg.Content)
		for _, part := range msg.UserInputMultiContent {
			m.bodies = append(m.bodies, part.Text)
		}
	}
	options := einomodel.GetCommonOptions(nil, opts...)
	for _, tool := range options.Tools {
		if tool != nil {
			m.toolNames = append(m.toolNames, tool.Name)
		}
	}
	if m.calls > len(m.responses) {
		return nil, errors.New("unexpected model call")
	}
	return m.responses[m.calls-1], nil
}

func (m *recordingModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Lark.AppID = "cli_synthetic"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "synthetic-owner"
	cfg.Workspace.Root = t.TempDir()
	cfg.GitHub.Enabled = true
	cfg.GitHub.AllowedRepositories = []string{"example/widgets"}
	cfg.GitHub.APIBaseURL = "https://github.example"
	return cfg
}

func writeEvent(t *testing.T, name, body string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Dir(path)
}

// unusedFinalizer errors if the terminal finalizer is consulted, so tests that
// expect the loop model to converge on its own stay honest.
func unusedFinalizer() *recordingModel {
	return &recordingModel{}
}

func recordDecision() *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   "record",
		Type: "function",
		Function: schema.FunctionCall{
			Name: "submit_decision",
			Arguments: `{
				"decision":"record",
				"relevance_confidence":0.9,
				"risk":"low",
				"reason":"done"
			}`,
		},
	}})
}

func TestGitHubRunSkipsWithoutMention(t *testing.T) {
	model := &recordingModel{responses: []*schema.Message{recordDecision()}}
	eventPath, _ := writeEvent(t, "event.json", `{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"please look","user":{"login":"example-user","type":"User"}}
	}`)
	result, err := Run(context.Background(), Options{
		Config:            testConfig(t),
		GitHub:            true,
		Message:           "unused",
		EventPath:         eventPath,
		EventName:         "issue_comment",
		Model:             model,
		TerminalFinalizer: unusedFinalizer(),
		DryRun:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || model.calls != 0 {
		t.Fatalf("SC-17 result=%+v calls=%d", result, model.calls)
	}
}

func TestGitHubRunUnknownSlashPostsHelp(t *testing.T) {
	var posted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/issues/7/comments") {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		posted = payload.Body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1001}`))
	}))
	t.Cleanup(server.Close)
	client, err := internalgithub.NewClient(internalgithub.ClientConfig{
		BaseURL: server.URL, Token: "synthetic-token", Limits: internalgithub.DefaultLimits(), HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModel{responses: []*schema.Message{recordDecision()}}
	eventPath, _ := writeEvent(t, "event.json", `{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"@lark-agent /nope","user":{"login":"example-user","type":"User"}}
	}`)
	result, err := Run(context.Background(), Options{
		Config:            testConfig(t),
		GitHub:            true,
		Message:           "unused",
		AllowedActions:    "post_github_comment",
		EventPath:         eventPath,
		EventName:         "issue_comment",
		Model:             model,
		TerminalFinalizer: unusedFinalizer(),
		GitHubClient:      client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 0 || result.CommentID != "1001" || !strings.Contains(posted, "/nope") ||
		!strings.Contains(posted, "/review") || !strings.Contains(posted, "/title") ||
		!strings.Contains(posted, "/check") || !strings.Contains(posted, "--dry-run") {
		t.Fatalf("SC-09 result=%+v body=%q calls=%d", result, posted, model.calls)
	}
}

func TestGitHubRunReviewOnIssuePostsPullRequestHelp(t *testing.T) {
	var posted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(raw, &payload)
		posted = payload.Body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":2002}`))
	}))
	t.Cleanup(server.Close)
	client, err := internalgithub.NewClient(internalgithub.ClientConfig{
		BaseURL: server.URL, Token: "synthetic-token", Limits: internalgithub.DefaultLimits(), HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModel{responses: []*schema.Message{recordDecision()}}
	eventPath, _ := writeEvent(t, "event.json", `{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"@lark-agent /review","user":{"login":"example-user","type":"User"}}
	}`)
	result, err := Run(context.Background(), Options{
		Config:            testConfig(t),
		GitHub:            true,
		Message:           "unused",
		AllowedActions:    "post_github_comment",
		EventPath:         eventPath,
		EventName:         "issue_comment",
		Model:             model,
		TerminalFinalizer: unusedFinalizer(),
		GitHubClient:      client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 0 || result.Command != "review" ||
		!strings.Contains(strings.ToLower(posted), "pull request") {
		t.Fatalf("SC-28 result=%+v body=%q calls=%d", result, posted, model.calls)
	}
}

func TestGitHubRunDryRunEnvelopeAndNoWrites(t *testing.T) {
	model := &recordingModel{responses: []*schema.Message{recordDecision()}}
	eventPath, _ := writeEvent(t, "event.json", `{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":2,"name":"verify","status":"completed","conclusion":"failure",
	    "head_sha":"0123456789abcdef0123456789abcdef01234567",
	    "html_url":"https://github.example/example/widgets/actions/runs/981"
	  }
	}`)
	result, err := Run(context.Background(), Options{
		Config:            testConfig(t),
		GitHub:            true,
		Message:           "summarize",
		DryRun:            true,
		EventPath:         eventPath,
		EventName:         "workflow_run",
		Model:             model,
		TerminalFinalizer: unusedFinalizer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "run" || !result.DryRun || result.EventName != "workflow_run" ||
		len(result.AllowedActions) != 0 || model.calls != 1 {
		t.Fatalf("SC-03 result=%+v calls=%d", result, model.calls)
	}
	joined := strings.Join(model.bodies, "\n")
	if !strings.Contains(joined, "github_event_summary") || !strings.Contains(joined, "example/widgets") {
		t.Fatalf("SC-20 bodies=%s", joined)
	}
	for _, name := range model.toolNames {
		if name == "shell" || name == "write_workspace" {
			t.Fatalf("SC-50 tool=%s names=%v", name, model.toolNames)
		}
	}
}

func TestGitHubRunRejectsUnknownActionAndRequiresChat(t *testing.T) {
	cfg := testConfig(t)
	if _, err := Run(context.Background(), Options{
		Config: cfg, GitHub: true, AllowedActions: "merge",
		Model: &recordingModel{}, TerminalFinalizer: unusedFinalizer(),
	}); err == nil || !strings.Contains(err.Error(), "unknown allowed action") {
		t.Fatalf("SC-21 err=%v", err)
	}
	eventPath, _ := writeEvent(t, "event.json", `{
	  "action":"opened",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"}
	}`)
	if _, err := Run(context.Background(), Options{
		Config:            cfg,
		GitHub:            true,
		AllowedActions:    "send_lark_message",
		Message:           "summarize",
		EventPath:         eventPath,
		EventName:         "issues",
		Model:             &recordingModel{responses: []*schema.Message{recordDecision()}},
		TerminalFinalizer: unusedFinalizer(),
	}); err == nil || !strings.Contains(err.Error(), "--chat-id is required") {
		t.Fatalf("SC-59 err=%v", err)
	}
}

func TestBareRunRegistersWorkspaceReadsOnly(t *testing.T) {
	model := &recordingModel{responses: []*schema.Message{recordDecision()}}
	cfg := testConfig(t)
	result, err := Run(context.Background(), Options{
		Config:            cfg,
		Message:           "list files",
		WorkspaceRoot:     cfg.Workspace.Root,
		Model:             model,
		TerminalFinalizer: unusedFinalizer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "run" || result.EventName != "" {
		t.Fatalf("result=%+v", result)
	}
	joined := strings.Join(model.toolNames, ",")
	if strings.Contains(joined, "shell") || strings.Contains(joined, "write_workspace") ||
		strings.Contains(joined, "get_github_file") {
		t.Fatalf("tools=%v", model.toolNames)
	}
	if !strings.Contains(joined, "read_workspace") || !strings.Contains(joined, "list_workspace") {
		t.Fatalf("workspace read tools missing: %v", model.toolNames)
	}
}

func TestCatalogHasSubmitDecision(t *testing.T) {
	defs, err := catalog(Options{}, nil, nil, &agenttools.WriteGate{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Info.Name] = true
	}
	if !names["submit_decision"] || names["shell"] {
		t.Fatalf("names=%v", names)
	}
}

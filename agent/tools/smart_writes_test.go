package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

type recordingGitHubWriter struct {
	comments  int
	titles    int
	checks    int
	lastBody  string
	lastTitle string
}

func (r *recordingGitHubWriter) PostIssueComment(_ context.Context, _ internalgithub.Reference, body string) (internalgithub.CommentResult, error) {
	r.comments++
	r.lastBody = body
	return internalgithub.CommentResult{ID: 1001}, nil
}

func (r *recordingGitHubWriter) UpdateIssueTitle(_ context.Context, _ internalgithub.Reference, title string) error {
	r.titles++
	r.lastTitle = title
	return nil
}

func (r *recordingGitHubWriter) UpsertCheck(_ context.Context, _ internalgithub.Reference, _ internalgithub.CheckUpsert) (internalgithub.CheckResult, error) {
	r.checks++
	return internalgithub.CheckResult{ID: 44}, nil
}

type recordingSender struct {
	sends int
	text  string
	key   string
}

func (s *recordingSender) Send(_ context.Context, _, _, contentJSON, idempotencyKey string) (string, error) {
	s.sends++
	s.text = contentJSON
	s.key = idempotencyKey
	return "om_synthetic", nil
}

func smartWriteScope() context.Context {
	ref := domain.GitHubReference{
		SchemaVersion:     1,
		Repository:        "example/widgets",
		Kind:              domain.GitHubReferencePullRequest,
		PullRequestNumber: 12,
		IssueNumber:       12,
		HeadSHA:           "0123456789abcdef0123456789abcdef01234567",
	}
	return WithInvocationScope(context.Background(), InvocationScope{
		Owner:           true,
		GitHubReference: &ref,
		WorkKind:        domain.WorkKindSmartCommand,
	})
}

func TestSmartWriteToolsEnforceAllowlistDryRunAndOneShot(t *testing.T) {
	writer := &recordingGitHubWriter{}
	gate := &WriteGate{Allow: map[string]bool{internalgithub.ActionPostGitHubComment: true}}
	registry, err := NewRegistry(
		PostGitHubCommentDefinition(writer, gate),
		UpdateGitHubIssueTitleDefinition(writer, gate),
		UpsertGitHubCheckDefinition(writer, gate),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := smartWriteScope()
	if _, err := registry.Execute(ctx, internalgithub.ActionUpdateGitHubIssueTitle, json.RawMessage(`{"title":"x"}`)); err == nil ||
		!strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("SC-04 err=%v", err)
	}
	if writer.titles != 0 {
		t.Fatal("SC-04 sent PATCH")
	}
	if _, err := registry.Execute(ctx, internalgithub.ActionPostGitHubComment, json.RawMessage(
		`{"body":"ok","repository":"attacker/override"}`,
	)); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("SC-05 err=%v", err)
	}
	if writer.comments != 0 {
		t.Fatal("SC-05 sent HTTP")
	}
	if _, err := registry.Execute(ctx, internalgithub.ActionPostGitHubComment, json.RawMessage(`{"body":"first"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(ctx, internalgithub.ActionPostGitHubComment, json.RawMessage(`{"body":"second"}`)); err == nil ||
		!strings.Contains(err.Error(), "already posted") {
		t.Fatalf("SC-41 err=%v", err)
	}
	if writer.comments != 1 {
		t.Fatalf("SC-41 comments=%d", writer.comments)
	}

	dry := &WriteGate{Allow: map[string]bool{}}
	dryRegistry, err := NewRegistry(PostGitHubCommentDefinition(writer, dry))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dryRegistry.Execute(ctx, internalgithub.ActionPostGitHubComment, json.RawMessage(`{"body":"nope"}`)); err == nil ||
		!strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("SC-10 err=%v", err)
	}

	gate.Allow[internalgithub.ActionUpsertGitHubCheck] = true
	if _, err := registry.Execute(ctx, internalgithub.ActionUpsertGitHubCheck, json.RawMessage(
		`{"conclusion":"cancelled","title":"x","summary":"y"}`,
	)); err == nil {
		t.Fatal("SC-32 expected invalid conclusion")
	}
	if writer.checks != 0 {
		t.Fatal("SC-32 sent HTTP")
	}
	if _, err := registry.Execute(ctx, internalgithub.ActionUpsertGitHubCheck, json.RawMessage(
		`{"conclusion":"success","title":"`+strings.Repeat("a", 256)+`","summary":"y"}`,
	)); err == nil {
		t.Fatal("SC-62 expected title limit")
	}
	if _, err := registry.Execute(ctx, internalgithub.ActionPostGitHubComment, json.RawMessage(
		`{"body":"`+strings.Repeat("b", 65537)+`"}`,
	)); err == nil {
		t.Fatal("SC-63 expected body limit")
	}
}

func TestWriteJobOutputNameAndDelimiter(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/github-output"
	gate := &WriteGate{Allow: map[string]bool{internalgithub.ActionWriteJobOutput: true}, JobOutputPath: path}
	registry, err := NewRegistry(WriteJobOutputDefinition(gate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), internalgithub.ActionWriteJobOutput, json.RawMessage(
		`{"name":"notes","value":"x"}`,
	)); err == nil {
		t.Fatal("SC-33 expected name denial")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("SC-33 wrote file err=%v", err)
	}
	if _, err := registry.Execute(context.Background(), internalgithub.ActionWriteJobOutput, json.RawMessage(
		`{"name":"changelog","value":"line\nLARK_AGENT_EOF\nrest"}`,
	)); err == nil {
		t.Fatal("SC-42 expected delimiter denial")
	}
	if _, err := registry.Execute(context.Background(), internalgithub.ActionWriteJobOutput, json.RawMessage(
		`{"name":"changelog","value":"ok notes"}`,
	)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "changelog<<LARK_AGENT_EOF") || gate.Outputs["changelog"] != "ok notes" {
		t.Fatalf("output=%s gate=%v", data, gate.Outputs)
	}
}

func TestSendLarkMessageAppendsMarkerAndRejectsSecrets(t *testing.T) {
	sender := &recordingSender{}
	ref := internalgithub.Reference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          internalgithub.ReferenceIssue,
		IssueNumber:   7,
	}
	gate := &WriteGate{
		Allow:          map[string]bool{internalgithub.ActionSendLarkMessage: true},
		ChatID:         "oc_synthetic",
		AppSecret:      "synthetic-lark-app-secret",
		Reference:      &ref,
		EncodeMarker:   internalgithub.EncodeReferenceMarker,
		IdempotencyKey: internalgithub.StableSmartCommandKey,
		Secrets:        []string{"must-not-appear"},
	}
	registry, err := NewRegistry(SendLarkMessageDefinition(sender, gate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), internalgithub.ActionSendLarkMessage, json.RawMessage(
		`{"text":"hello must-not-appear"}`,
	)); err == nil {
		t.Fatal("SC-22 expected secret rejection")
	}
	if sender.sends != 0 {
		t.Fatal("SC-22 sent Lark")
	}
	if _, err := registry.Execute(context.Background(), internalgithub.ActionSendLarkMessage, json.RawMessage(
		`{"text":"hello widgets"}`,
	)); err != nil {
		t.Fatal(err)
	}
	if sender.sends != 1 || !strings.Contains(sender.text, "[lark-agent-github-ref:v1:") ||
		!strings.HasPrefix(sender.key, "ghs-") {
		t.Fatalf("SC-39 sender=%+v", sender)
	}
}

// TestWriteGateEnforcesOutwardLanguage covers SC-87. Outward prose must match
// the resolved language; titles are repository artifacts and stay exempt.
func TestWriteGateEnforcesOutwardLanguage(t *testing.T) {
	const english = "This pull request updates the notification pipeline and adds regression coverage."
	const chinese = "已确认通知链路更新，并补齐了回归覆盖。"

	writer := &recordingGitHubWriter{}
	sender := &recordingSender{}
	outputPath := t.TempDir() + "/github-output"
	gate := &WriteGate{
		Allow: map[string]bool{
			internalgithub.ActionPostGitHubComment:      true,
			internalgithub.ActionUpdateGitHubIssueTitle: true,
			internalgithub.ActionUpsertGitHubCheck:      true,
			internalgithub.ActionSendLarkMessage:        true,
			internalgithub.ActionWriteJobOutput:         true,
		},
		ChatID:        "oc_synthetic",
		JobOutputPath: outputPath,
		Language:      agentlocale.LanguageChinese,
	}
	registry, err := NewRegistry(
		PostGitHubCommentDefinition(writer, gate),
		UpdateGitHubIssueTitleDefinition(writer, gate),
		UpsertGitHubCheckDefinition(writer, gate),
		SendLarkMessageDefinition(sender, gate),
		WriteJobOutputDefinition(gate),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := smartWriteScope()

	for _, testCase := range []struct {
		tool string
		args string
	}{
		{internalgithub.ActionPostGitHubComment, `{"body":"` + english + `"}`},
		{internalgithub.ActionSendLarkMessage, `{"text":"` + english + `"}`},
		{
			internalgithub.ActionUpsertGitHubCheck,
			`{"conclusion":"success","title":"lark-agent gate","summary":"` + english + `"}`,
		},
		{internalgithub.ActionWriteJobOutput, `{"name":"changelog","value":"` + english + `"}`},
	} {
		_, err := registry.Execute(ctx, testCase.tool, json.RawMessage(testCase.args))
		if err == nil || !strings.Contains(err.Error(), "zh-CN") {
			t.Fatalf("SC-87 %s err=%v", testCase.tool, err)
		}
	}
	if writer.comments != 0 || writer.checks != 0 || sender.sends != 0 {
		t.Fatalf("SC-87 a rejected write reached its transport: %+v %+v", writer, sender)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("SC-87 rejected job output was written: %v", statErr)
	}

	// An English Conventional Commits title stays legal under zh-CN.
	if _, err := registry.Execute(ctx, internalgithub.ActionUpdateGitHubIssueTitle, json.RawMessage(
		`{"title":"fix: keep the notification pipeline gate deterministic for every workflow run"}`,
	)); err != nil {
		t.Fatalf("SC-87 title must be exempt from language enforcement: %v", err)
	}

	// The gate stayed unused, so the model can rewrite and call again.
	if _, err := registry.Execute(ctx, internalgithub.ActionPostGitHubComment, json.RawMessage(
		`{"body":"`+chinese+`"}`,
	)); err != nil {
		t.Fatalf("SC-87 rewrite rejected: %v", err)
	}
	if writer.comments != 1 || writer.lastBody != chinese {
		t.Fatalf("SC-87 writer=%+v", writer)
	}
}

func TestBoundLarkContextRejectsOtherChat(t *testing.T) {
	registry, err := NewRegistry(BoundLarkContextDefinitions(fakeLarkProvider{}, "oc_synthetic")...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "get_lark_context", json.RawMessage(
		`{"chat_id":"oc_other"}`,
	)); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("SC-60 err=%v", err)
	}
}

type fakeLarkProvider struct{}

func (fakeLarkProvider) RecentMessages(context.Context, LarkContextRequest) (LarkContextResult, error) {
	return LarkContextResult{}, nil
}

func (fakeLarkProvider) SearchMessages(context.Context, string, []string, int) ([]domain.NormalizedEvent, error) {
	return nil, nil
}

package larkagent_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/liuchong/lark-agent/agent/config"
)

// TestSmartCommandResolvesConfiguredOutputLanguage covers SC-85, SC-86 and
// SC-89. Every shipped prompt file is English, so a run that inferred its
// outward language from prompt text could never honour a Chinese
// configuration.
func TestSmartCommandResolvesConfiguredOutputLanguage(t *testing.T) {
	bin := buildAgentBinary(t)
	workspace := t.TempDir()

	var mu sync.Mutex
	var firstBody string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		if firstBody == "" {
			firstBody = string(raw)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_decision","arguments":"{\"decision\":\"record\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"recorded\",\"reply_outcome\":\"complete\"}"}}]}}]}`))
	}))
	t.Cleanup(model.Close)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("SC-85 mutating github HTTP %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(github.Close)

	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":1,"name":"verify","status":"completed","conclusion":"success",
	    "head_sha":"`+smartHeadSHA+`",
	    "html_url":"https://github.example/example/widgets/actions/runs/981"
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	baseEnv := []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_API_URL=" + github.URL,
		"GITHUB_EVENT_NAME=workflow_run",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=synthetic-token",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
		"OPENAI_MODEL=test-model",
	}
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	englishPrompt := "Summarize the repository workflow run for the configured chat."

	for _, testCase := range []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{
			name: "configured fallback wins over english prompt text",
			args: []string{"--message", englishPrompt},
			want: "zh-CN",
		},
		{
			name: "flag selects the outward language",
			args: []string{"--message", englishPrompt, "--output-language", "en-US"},
			want: "en-US",
		},
		{
			name: "environment selects the outward language",
			env:  []string{"LARK_AGENT_OUTPUT_LANGUAGE=en-US"},
			args: []string{"--message", englishPrompt},
			want: "en-US",
		},
		{
			name: "flag wins over environment",
			env:  []string{"LARK_AGENT_OUTPUT_LANGUAGE=en-US"},
			args: []string{"--message", englishPrompt, "--output-language", "zh-CN"},
			want: "zh-CN",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mu.Lock()
			firstBody = ""
			mu.Unlock()
			args := append([]string{"--config", missingConfig, "github", "run", "--dry-run"}, testCase.args...)
			code, _, stderr := runAgentWithEnv(t, append(baseEnv, testCase.env...), bin, args...)
			if code != 0 {
				t.Fatalf("SC-85 exit=%d stderr=%s", code, stderr)
			}
			mu.Lock()
			body := firstBody
			mu.Unlock()
			if !strings.Contains(body, "Required outward language: "+testCase.want) {
				t.Fatalf("SC-85 required outward language %q missing from first model request: %s",
					testCase.want, body)
			}
			for _, other := range []string{"zh-CN", "en-US"} {
				if other != testCase.want &&
					strings.Contains(body, "Required outward language: "+other) {
					t.Fatalf("SC-85 resolved %q but the model also saw %q", testCase.want, other)
				}
			}
			// SC-89: outward content rules travel with the system prompt, not
			// with each repository's prompt file.
			for _, rule := range []string{
				"State the conclusion first",
				"only means something inside this repository",
				"Copy it exactly as the event or repository gives it",
			} {
				if !strings.Contains(body, rule) {
					t.Fatalf("SC-89 system prompt is missing %q: %s", rule, body)
				}
			}
		})
	}

	code, stdout, stderr := runAgentWithEnv(t, baseEnv, bin, "--config", missingConfig,
		"github", "run", "--dry-run", "--message", englishPrompt, "--output-language", "ja-JP")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "unsupported output language: ja-JP") {
		t.Fatalf("SC-86 code=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

// TestSmartCommandReadsOutputLanguageFromConfigFile covers SC-85 for the
// configured daemon layout, where no Actions environment exists.
func TestSmartCommandReadsOutputLanguageFromConfigFile(t *testing.T) {
	bin := buildAgentBinary(t)

	var mu sync.Mutex
	var firstBody string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		if firstBody == "" {
			firstBody = string(raw)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_decision","arguments":"{\"decision\":\"record\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"recorded\",\"reply_outcome\":\"complete\"}"}}]}}]}`))
	}))
	t.Cleanup(model.Close)

	cfg := config.Default()
	cfg.Lark.AppID = "cli_synthetic"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Workspace.Root = t.TempDir()
	cfg.Output.Language = "en-US"
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runAgentWithEnv(t, []string{
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
		"OPENAI_MODEL=test-model",
	}, bin, "--config", configPath, "run", "--message", "请汇总一下当前的工作区规则。")
	if code != 0 {
		t.Fatalf("SC-85 exit=%d stderr=%s", code, stderr)
	}
	mu.Lock()
	body := firstBody
	mu.Unlock()
	if !strings.Contains(body, "Required outward language: en-US") {
		t.Fatalf("SC-85 output.language was not honoured: %s", body)
	}
}

// TestSmartCommandRejectsOutwardLanguageMismatch covers SC-87. Smart commands
// finish as record, so the reply-language gate never sees their outward text;
// the write gate is the only place that can enforce it.
func TestSmartCommandRejectsOutwardLanguageMismatch(t *testing.T) {
	bin := buildAgentBinary(t)
	workspace := t.TempDir()

	const englishBody = "This pull request updates the notification pipeline and adds regression coverage for the new gate."
	const chineseBody = "已确认通知链路更新，并补齐了门禁的回归覆盖。"

	var turn atomic.Int32
	var mu sync.Mutex
	var bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch turn.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"post_github_comment","arguments":` +
				quoteJSONArgs(t, map[string]string{"body": englishBody}) + `}}]}}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c2","type":"function","function":{"name":"post_github_comment","arguments":` +
				quoteJSONArgs(t, map[string]string{"body": chineseBody}) + `}}]}}]}`))
		default:
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c3","type":"function","function":{"name":"submit_decision","arguments":"{\"decision\":\"record\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"commented\",\"reply_outcome\":\"complete\"}"}}]}}]}`))
		}
	}))
	t.Cleanup(model.Close)

	var posts atomic.Int32
	var postedBody string
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/comments") {
			posts.Add(1)
			var payload struct {
				Body string `json:"body"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &payload)
			mu.Lock()
			postedBody = payload.Body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":2002}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(github.Close)

	eventPath := filepath.Join(t.TempDir(), "comment.json")
	if err := os.WriteFile(eventPath, []byte(`{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"@lark-agent 汇总一下","user":{"login":"example-user","type":"User"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	code, stdout, stderr := runAgentWithEnv(t, []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_API_URL=" + github.URL,
		"GITHUB_EVENT_NAME=issue_comment",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=synthetic-token",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
		"OPENAI_MODEL=test-model",
	}, bin, "--config", missingConfig, "github", "run",
		"--allowed-actions", "post_github_comment",
		"--message", "Comment once on the issue with post_github_comment.")
	if code != 0 {
		t.Fatalf("SC-87 exit=%d stderr=%s", code, stderr)
	}
	if posts.Load() != 1 {
		t.Fatalf("SC-87 github comment POST count=%d", posts.Load())
	}
	mu.Lock()
	posted := postedBody
	seen := append([]string{}, bodies...)
	mu.Unlock()
	if !strings.Contains(posted, "已确认通知链路更新") {
		t.Fatalf("SC-87 posted body=%q", posted)
	}
	if len(seen) < 2 || !strings.Contains(seen[1], "zh-CN") {
		t.Fatalf("SC-87 the language rejection was not fed back to the model: %v", seen)
	}
	if data := decodeSmartCommandData(t, stdout); data.CommentID != "2002" {
		t.Fatalf("SC-87 data=%+v", data)
	}
}

// TestSmartCommandHelpCommentFollowsOutputLanguage covers SC-88.
func TestSmartCommandHelpCommentFollowsOutputLanguage(t *testing.T) {
	bin := buildAgentBinary(t)
	workspace := t.TempDir()

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("SC-88 deterministic help must not call the model")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(model.Close)

	var mu sync.Mutex
	var postedBody string
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/7/comments") {
			var payload struct {
				Body string `json:"body"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &payload)
			mu.Lock()
			postedBody = payload.Body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":3003}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(github.Close)

	eventPath := filepath.Join(t.TempDir(), "comment.json")
	if err := os.WriteFile(eventPath, []byte(`{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"@lark-agent /nope","user":{"login":"example-user","type":"User"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	baseEnv := []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_API_URL=" + github.URL,
		"GITHUB_EVENT_NAME=issue_comment",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=synthetic-token",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
		"OPENAI_MODEL=test-model",
	}
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")

	for _, testCase := range []struct {
		name     string
		args     []string
		contains string
		absent   string
	}{
		{name: "chinese", contains: "未知命令", absent: "Unknown slash command"},
		{
			name:     "english",
			args:     []string{"--output-language", "en-US"},
			contains: "Unknown slash command",
			absent:   "未知命令",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mu.Lock()
			postedBody = ""
			mu.Unlock()
			args := append([]string{
				"--config", missingConfig, "github", "run",
				"--allowed-actions", "post_github_comment",
			}, testCase.args...)
			code, _, stderr := runAgentWithEnv(t, baseEnv, bin, args...)
			if code != 0 {
				t.Fatalf("SC-88 exit=%d stderr=%s", code, stderr)
			}
			mu.Lock()
			body := postedBody
			mu.Unlock()
			if !strings.Contains(body, testCase.contains) || strings.Contains(body, testCase.absent) {
				t.Fatalf("SC-88 help body=%q", body)
			}
			for _, token := range []string{"/review", "/title", "/check", "--dry-run"} {
				if !strings.Contains(body, token) {
					t.Fatalf("SC-88 help body lost %q: %q", token, body)
				}
			}
		})
	}
}

func quoteJSONArgs(t *testing.T, args map[string]string) string {
	t.Helper()
	inner, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(string(inner))
	if err != nil {
		t.Fatal(err)
	}
	return string(quoted)
}

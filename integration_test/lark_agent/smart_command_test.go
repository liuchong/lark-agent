package larkagent_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/liuchong/lark-agent/agent/config"
)

const smartHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type smartCommandData struct {
	Mode           string            `json:"mode"`
	DryRun         bool              `json:"dry_run"`
	Skipped        bool              `json:"skipped"`
	Partial        bool              `json:"partial"`
	EventName      string            `json:"event_name"`
	Command        string            `json:"command"`
	AllowedActions []string          `json:"allowed_actions"`
	Repository     string            `json:"repository"`
	CommentID      string            `json:"comment_id"`
	CheckID        string            `json:"check_id"`
	MessageID      string            `json:"message_id"`
	Title          string            `json:"title"`
	OutputLanguage string            `json:"output_language"`
	Outputs        map[string]string `json:"outputs"`
	Reference      json.RawMessage   `json:"reference"`
}

func TestSmartCommandCLIContracts(t *testing.T) {
	bin := buildAgentBinary(t)
	root := repoRoot(t)
	workspace := t.TempDir()

	var modelHits atomic.Int32
	var firstBody string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelHits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if firstBody == "" {
			firstBody = string(raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_decision","arguments":"{\"decision\":\"record\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"ok\"}"}}]}}]}`))
	}))
	t.Cleanup(model.Close)

	var githubHits atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		githubHits.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("mutating github HTTP %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(github.Close)

	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":2,"name":"verify","status":"completed","conclusion":"failure",
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
		"GITHUB_TOKEN=must-not-appear",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=must-not-appear",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
		"OPENAI_MODEL=test-model",
	}
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	code, stdout, stderr := runAgentWithEnv(t, baseEnv, bin, "--config", missingConfig,
		"github", "run", "--dry-run", "--message", "summarize this event")
	if code != 0 {
		t.Fatalf("SC-03 exit=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "must-not-appear") || strings.Contains(stderr, "must-not-appear") {
		t.Fatalf("SC-22 secret leaked stdout=%s stderr=%s", stdout, stderr)
	}
	data := decodeSmartCommandData(t, stdout)
	if data.Mode != "run" || !data.DryRun || data.EventName != "workflow_run" ||
		data.AllowedActions == nil || len(data.AllowedActions) != 0 {
		t.Fatalf("SC-03 data=%+v", data)
	}
	if !strings.Contains(firstBody, "github_event_summary") || !strings.Contains(firstBody, "example/widgets") {
		t.Fatalf("SC-20 first body=%s", firstBody)
	}
	if githubHits.Load() != 0 {
		t.Fatalf("SC-03 github hits=%d", githubHits.Load())
	}

	code, stdout, stderr = runAgentWithEnv(t, append(baseEnv, "GITHUB_EVENT_NAME=fork"), bin,
		"--config", missingConfig, "github", "run", "--message", "x")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "unsupported github event") {
		t.Fatalf("SC-02 code=%d stdout=%q stderr=%s", code, stdout, stderr)
	}

	code, stdout, stderr = runAgentWithEnv(t, []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-lark-app-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
	}, bin, "--config", missingConfig, "github", "run", "--message", "x")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "GITHUB_EVENT_PATH and GITHUB_EVENT_NAME are required") {
		t.Fatalf("SC-15 code=%d stderr=%s", code, stderr)
	}

	code, stdout, stderr = runAgentWithEnv(t, baseEnv, bin, "--config", missingConfig,
		"github", "run", "--not-a-real-flag")
	if code == 0 || stdout != "" || (!strings.Contains(stderr, "unknown flag") && !strings.Contains(stderr, "unknown")) {
		t.Fatalf("SC-14 code=%d stderr=%s", code, stderr)
	}

	otherEvent := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(otherEvent, []byte(`{
	  "repository":{"full_name":"other/other"},
	  "workflow_run":{"id":1,"head_sha":"`+smartHeadSHA+`","html_url":"https://github.example/other/other/actions/runs/1"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runAgentWithEnv(t, []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_EVENT_NAME=workflow_run",
		"GITHUB_EVENT_PATH=" + otherEvent,
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-lark-app-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
	}, bin, "--config", missingConfig, "github", "run", "--message", "x")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "github repository is not allowed") {
		t.Fatalf("SC-16/SC-79 code=%d stderr=%s", code, stderr)
	}

	code, stdout, stderr = runAgentWithEnv(t, baseEnv, bin, "--config", missingConfig,
		"github", "run", "--allowed-actions=merge", "--message", "x")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "unknown allowed action") {
		t.Fatalf("SC-21 code=%d stderr=%s", code, stderr)
	}

	code, stdout, stderr = runAgentWithEnv(t, baseEnv, bin, "--config", missingConfig, "github", "run")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "prompt") {
		t.Fatalf("SC-18 code=%d stderr=%s", code, stderr)
	}

	commentPath := filepath.Join(t.TempDir(), "comment.json")
	if err := os.WriteFile(commentPath, []byte(`{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"please look","user":{"login":"example-user","type":"User"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeHits := modelHits.Load()
	code, stdout, stderr = runAgentWithEnv(t, []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_EVENT_NAME=issue_comment",
		"GITHUB_EVENT_PATH=" + commentPath,
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=synthetic-lark-app-secret",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
	}, bin, "--config", missingConfig, "github", "run", "--message", "x")
	if code != 0 {
		t.Fatalf("SC-17 exit=%d stderr=%s", code, stderr)
	}
	skipped := decodeSmartCommandData(t, stdout)
	if !skipped.Skipped || modelHits.Load() != beforeHits {
		t.Fatalf("SC-17 skipped=%+v hits=%d before=%d", skipped, modelHits.Load(), beforeHits)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	code, _, stderr = runAgent(t, bin, "--config", cfgPath, "init",
		"--workspace", workspace, "--app-id", "cli_synthetic", "--owner-open-id", "ou_owner", "--owner-name", "测试负责人")
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	primary := cfg.Model.Profiles["primary"]
	primary.CredentialKeychainKey = "model/primary/api-key-sc11-missing"
	cfg.Model.Profiles["primary"] = primary
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runAgentWithEnv(t, nil, bin, "--config", cfgPath, "run", "--message", "hello")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "model") {
		t.Fatalf("SC-11 code=%d stderr=%s", code, stderr)
	}

	home := t.TempDir()
	stateDir := filepath.Join(home, "Library", "Application Support", "lark-agent")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(stateDir, "state.db")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runAgentWithEnv(t, append(baseEnv, "HOME="+home), bin, "--config", missingConfig,
		"github", "run", "--dry-run", "--message", "x")
	if code != 0 {
		t.Fatalf("SC-19 exit=%d stderr=%s", code, stderr)
	}
	after, err := os.Stat(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		t.Fatalf("SC-19 opened daemon state size %d->%d", info.Size(), after.Size())
	}

	source, err := os.ReadFile(filepath.Join(root, "agent", "smartcmd", "run.go"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("realtime")) || bytes.Contains(source, []byte("Consume(")) {
		t.Fatal("SC-01 smart command constructed a WebSocket consumer path")
	}
}

func TestSmartCommandActionDispatcherAndPrompts(t *testing.T) {
	root := repoRoot(t)
	action, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(action)
	if !strings.Contains(text, "default: notify") || !strings.Contains(text, "LARK_AGENT_MODE") {
		t.Fatalf("SC-53 action.yml=%s", text)
	}
	docker, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(docker), `ENTRYPOINT ["/usr/local/bin/lark-agent", "github", "notify"]`) {
		t.Fatal("SC-52 Dockerfile hardcodes notify")
	}
	cmd := exec.Command("sh", filepath.Join(root, "scripts", "lark-agent-action"))
	cmd.Env = append(cleanAgentTestEnv(os.Environ()), "LARK_AGENT_MODE=other")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Dir = root
	err = cmd.Run()
	if err == nil {
		t.Fatal("SC-34 expected unknown mode to fail")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 || !strings.Contains(stderr.String(), "unknown mode") {
		t.Fatalf("SC-34 err=%v stderr=%s", err, stderr.String())
	}
	notify, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "lark-notify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(notify), "mode:") {
		t.Fatal("SC-54 lark-notify.yml must omit mode")
	}

	promptDir := filepath.Join(root, ".github", "lark-agent", "prompts")
	entries, err := os.ReadDir(promptDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(promptDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(body))
		for _, want := range []string{"smart command", "submit_decision", "do not merge", "do not invent"} {
			if !strings.Contains(text, want) {
				t.Fatalf("prompt %s missing %q", entry.Name(), want)
			}
		}
	}
	notifyStyle, err := os.ReadFile(filepath.Join(promptDir, "notify-style.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notifyStyle), "Skip") || !strings.Contains(string(notifyStyle), "Send") {
		t.Fatal("notify-style.md missing Skip/Send headings")
	}
	titleRules, err := os.ReadFile(filepath.Join(promptDir, "title-rules.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(titleRules), "max 72") {
		t.Fatal("title-rules.md missing max 72")
	}
	mergeCheck, err := os.ReadFile(filepath.Join(promptDir, "merge-check.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mergeCheck), "lark-agent-gate") || !strings.Contains(strings.ToLower(string(mergeCheck)), "do not merge") {
		t.Fatal("merge-check.md missing gate name")
	}
}

// TestSmartCommandTerminalFinalizerConverges covers SC-81: a loop model that
// answers in prose without ever calling submit_decision must still converge
// through the terminal finalizer instead of failing the command.
func TestSmartCommandTerminalFinalizerConverges(t *testing.T) {
	bin := buildAgentBinary(t)
	workspace := t.TempDir()

	var loopTurns, finalizerTurns atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), `"tools"`) {
			loopTurns.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_decision","arguments":"{\"decision\":\"reply\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"tried to answer without recording\"}"}}]}}]}`))
			return
		}
		finalizerTurns.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"decision\":\"record\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"finalizer recorded the event without writes\",\"reply_outcome\":\"complete\"}"}}]}`))
	}))
	t.Cleanup(model.Close)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("SC-81 mutating github HTTP %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(github.Close)

	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":1,"name":"verify","status":"completed","conclusion":"failure",
	    "head_sha":"`+smartHeadSHA+`",
	    "html_url":"https://github.example/example/widgets/actions/runs/981"
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + workspace,
		"GITHUB_REPOSITORY=example/widgets",
		"GITHUB_API_URL=" + github.URL,
		"GITHUB_EVENT_NAME=workflow_run",
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_TOKEN=must-not-appear",
		"LARK_AGENT_APP_ID=cli_synthetic",
		"LARK_AGENT_APP_SECRET=must-not-appear",
		"LARK_AGENT_LARK_BASE_URL=https://open.larksuite.com",
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + model.URL,
		"OPENAI_MODEL=test-model",
	}
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	code, stdout, stderr := runAgentWithEnv(t, env, bin, "--config", missingConfig,
		"github", "run", "--dry-run", "--allowed-actions", "send_lark_message",
		"--message", "Summarize the event and send it to Lark using send_lark_message.")
	if code != 0 {
		t.Fatalf("SC-81 exit=%d stderr=%s", code, stderr)
	}
	if finalizerTurns.Load() != 1 {
		t.Fatalf("SC-81 finalizer turns=%d loop turns=%d", finalizerTurns.Load(), loopTurns.Load())
	}
	if strings.Contains(stdout, "must-not-appear") || strings.Contains(stderr, "must-not-appear") {
		t.Fatalf("SC-22 secret leaked stdout=%s stderr=%s", stdout, stderr)
	}
	data := decodeSmartCommandData(t, stdout)
	if !data.DryRun || len(data.AllowedActions) != 0 {
		t.Fatalf("SC-81 dry run must stay a dry run: %+v", data)
	}
}

// TestSmartCommandLivePromptsAcceptRecord covers SC-84. The shipped prompts say
// "repository", which matches the coding-question markers, so a content-based
// reclassification would reject the only terminal decision this work kind may
// use. Every live prompt runs against a fake model that submits record once.
func TestSmartCommandLivePromptsAcceptRecord(t *testing.T) {
	bin := buildAgentBinary(t)
	root := repoRoot(t)

	promptDir := filepath.Join(root, ".github", "lark-agent", "prompts")
	entries, err := os.ReadDir(promptDir)
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") {
			prompts = append(prompts, entry.Name())
		}
	}
	if len(prompts) < 8 {
		t.Fatalf("expected the live prompt set, got %v", prompts)
	}

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"tools"`) {
			t.Errorf("SC-84 terminal finalizer was consulted for a valid record")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"submit_decision","arguments":"{\"decision\":\"record\",\"relevance_confidence\":0.9,\"risk\":\"low\",\"reason\":\"recorded the event\",\"reply_outcome\":\"complete\"}"}}]}}]}`))
	}))
	t.Cleanup(model.Close)

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	t.Cleanup(github.Close)

	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,"run_attempt":1,"name":"verify","status":"completed","conclusion":"failure",
	    "head_sha":"`+smartHeadSHA+`",
	    "html_url":"https://github.example/example/widgets/actions/runs/981"
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=" + root,
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
	for _, prompt := range prompts {
		t.Run(prompt, func(t *testing.T) {
			code, stdout, stderr := runAgentWithEnv(t, env, bin, "--config", missingConfig,
				"github", "run", "--dry-run",
				"--prompt-file", filepath.Join(".github", "lark-agent", "prompts", prompt))
			if code != 0 {
				t.Fatalf("SC-84 %s exit=%d stderr=%s", prompt, code, stderr)
			}
			if !decodeSmartCommandData(t, stdout).DryRun {
				t.Fatalf("SC-84 %s lost dry run: %s", prompt, stdout)
			}
		})
	}
}

// TestSmartCommandRejectsMissingFinalizerProfile covers SC-82.
func TestSmartCommandRejectsMissingFinalizerProfile(t *testing.T) {
	bin := buildAgentBinary(t)
	cfg := config.Default()
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Workspace.Root = t.TempDir()
	cfg.GitHub.Enabled = true
	cfg.GitHub.AllowedRepositories = []string{"example/widgets"}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	// config.Save validates role bindings, so break the binding in the file.
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(saved), "finalizer: primary", "finalizer: absent-profile", 1)
	if broken == string(saved) {
		t.Fatal("SC-82 could not rebind model.roles.finalizer")
	}
	if err := os.WriteFile(configPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAgentWithEnv(t, []string{"OPENAI_API_KEY=test-key"}, bin,
		"--config", configPath, "run", "--message", "x")
	if code != 2 || stdout != "" ||
		!strings.Contains(stderr, "model role finalizer references missing profile") {
		t.Fatalf("SC-82 code=%d stdout=%q stderr=%s", code, stdout, stderr)
	}
}

func TestSmartCommandWorkflowYAMLContracts(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, dir := range []string{
		filepath.Join(root, ".github", "workflows"),
		filepath.Join(root, "examples", "github-agent", "workflows"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml") {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	if len(files) < 11 {
		t.Fatalf("expected live notify plus GW twins, got %d", len(files))
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, forbidden := range []string{"pull_request_target", "download-artifact", "persist-credentials: true"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("SC-66/67 %s contains %q", path, forbidden)
			}
		}
		var parsed map[string]any
		if err := yaml.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("yaml %s: %v", path, err)
		}
		jobs, _ := parsed["jobs"].(map[string]any)
		onValue := parsed["on"]
		hasPullRequest := yamlHasPullRequest(onValue)
		assertWorkflowRunTrigger(t, path, onValue)
		base := filepath.Base(path)
		larkWorkflow := strings.HasPrefix(base, "lark-") || strings.Contains(path, "examples/github-agent")
		for jobName, rawJob := range jobs {
			job, _ := rawJob.(map[string]any)
			ifExpr, _ := job["if"].(string)
			if hasPullRequest && larkWorkflow && jobNeedsAction(job) {
				if !strings.Contains(ifExpr, "github.event.pull_request.head.repo.full_name == github.repository") &&
					!strings.Contains(base, "comment.yml") {
					t.Fatalf("SC-38 %s job %s missing same-repo if: %s", path, jobName, ifExpr)
				}
			}
			if env, _ := job["environment"].(string); env != "" && env != "lark-production" {
				t.Fatalf("SC-68 %s job %s environment=%s", path, jobName, env)
			}
			if larkWorkflow && jobNeedsSecrets(job) && job["environment"] != "lark-production" {
				t.Fatalf("SC-68 %s job %s missing lark-production", path, jobName)
			}
			if larkWorkflow && jobNeedsSecrets(job) {
				if timeout, _ := asInt(job["timeout-minutes"]); timeout != 10 {
					t.Fatalf("timeout-minutes %s job %s = %v", path, jobName, job["timeout-minutes"])
				}
			}
			if larkWorkflow {
				assertCheckoutSteps(t, path, job)
				assertPermissions(t, path, jobName, parsed, job)
			}
		}
		if strings.Contains(base, "pr-review") {
			if strings.Contains(text, "synchronize") {
				t.Fatalf("GW-03.2 %s lists synchronize", path)
			}
			if !strings.Contains(text, "labeled") {
				t.Fatalf("GW-03.3 %s missing labeled", path)
			}
		}
		if strings.Contains(base, "event-summary") || strings.Contains(base, "notify-style") {
			if !strings.Contains(text, `!= 'CI'`) && !strings.Contains(text, `!= "CI"`) {
				t.Fatalf("SC-69 %s missing CI exclusion", path)
			}
		}
		if strings.Contains(base, "comment.yml") {
			for _, fragment := range []string{
				"contains(github.event.comment.body, '@lark-agent')",
				"github.event.comment.user.type != 'Bot'",
				"github.event.comment.author_association == 'OWNER'",
				"COLLABORATOR",
			} {
				if !strings.Contains(text, fragment) {
					t.Fatalf("GW-01 %s missing %q", path, fragment)
				}
			}
		}
		if strings.Contains(base, "release.yml") {
			releaseJob, _ := jobs["release"].(map[string]any)
			if releaseJob["if"] != "success()" {
				t.Fatalf("GW-06 release if=%v", releaseJob["if"])
			}
			if textContainsGitHubRun(releaseJob) {
				t.Fatal("GW-06 release job must not call github run")
			}
			perms, _ := releaseJob["permissions"].(map[string]any)
			if stringify(perms["contents"]) != "write" {
				t.Fatalf("SC-65 release contents=%v", perms["contents"])
			}
		}
	}
}

func decodeSmartCommandData(t *testing.T, stdout string) smartCommandData {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var envelope struct {
		OK   bool             `json:"ok"`
		Data smartCommandData `json:"data"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("SC-35 decode: %v\n%s", err, stdout)
	}
	if !envelope.OK {
		t.Fatalf("ok=false %s", stdout)
	}
	return envelope.Data
}

// assertWorkflowRunTrigger enforces SC-83. GitHub rejects the whole workflow
// file when `on.workflow_run` omits `workflows`, so the job never starts.
func assertWorkflowRunTrigger(t *testing.T, path string, onValue any) {
	t.Helper()
	triggers, ok := onValue.(map[string]any)
	if !ok {
		return
	}
	raw, ok := triggers["workflow_run"]
	if !ok {
		return
	}
	trigger, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("SC-83 %s workflow_run trigger is not a mapping: %T", path, raw)
	}
	workflows, _ := trigger["workflows"].([]any)
	if len(workflows) == 0 {
		t.Fatalf("SC-83 %s workflow_run must declare a non-empty workflows list", path)
	}
	for _, name := range workflows {
		if strings.TrimSpace(stringify(name)) == "" {
			t.Fatalf("SC-83 %s workflow_run workflows contains an empty name", path)
		}
	}
}

func yamlHasPullRequest(onValue any) bool {
	switch typed := onValue.(type) {
	case string:
		return typed == "pull_request"
	case map[string]any:
		_, ok := typed["pull_request"]
		return ok
	default:
		return false
	}
}

func jobNeedsAction(job map[string]any) bool {
	steps, _ := job["steps"].([]any)
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		if uses, _ := step["uses"].(string); uses == "./" {
			return true
		}
	}
	return false
}

func jobNeedsSecrets(job map[string]any) bool {
	if !jobNeedsAction(job) {
		return false
	}
	steps, _ := job["steps"].([]any)
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		with, _ := step["with"].(map[string]any)
		for _, key := range []string{"lark_app_secret", "lark_app_id"} {
			if stringify(with[key]) != "" {
				return true
			}
		}
		env, _ := step["env"].(map[string]any)
		if stringify(env["OPENAI_API_KEY"]) != "" {
			return true
		}
	}
	return false
}

func assertCheckoutSteps(t *testing.T, path string, job map[string]any) {
	t.Helper()
	steps, _ := job["steps"].([]any)
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		uses, _ := step["uses"].(string)
		if !strings.HasPrefix(uses, "actions/checkout@") {
			continue
		}
		with, _ := step["with"].(map[string]any)
		if stringify(with["persist-credentials"]) != "false" {
			t.Fatalf("SC-67 %s persist-credentials=%v", path, with["persist-credentials"])
		}
		if stringify(with["ref"]) != "${{ github.event.repository.default_branch }}" {
			t.Fatalf("SC-67 %s checkout ref=%v", path, with["ref"])
		}
		if stringify(with["ref"]) == "${{ github.event.pull_request.head.sha }}" {
			t.Fatalf("SC-67 %s checks out PR head", path)
		}
	}
}

func assertPermissions(t *testing.T, path, jobName string, parsed, job map[string]any) {
	t.Helper()
	perms := map[string]any{}
	if top, ok := parsed["permissions"].(map[string]any); ok {
		for key, value := range top {
			perms[key] = value
		}
	}
	if jobPerms, ok := job["permissions"].(map[string]any); ok {
		perms = jobPerms
	}
	if stringify(perms["contents"]) == "write" && jobName != "release" {
		t.Fatalf("SC-65 %s job %s has contents: write", path, jobName)
	}
}

func textContainsGitHubRun(job map[string]any) bool {
	raw, _ := yaml.Marshal(job)
	return strings.Contains(string(raw), "github run") || strings.Contains(string(raw), "mode: run")
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

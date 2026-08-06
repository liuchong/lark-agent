package larkagent_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	agentconfig "github.com/liuchong/lark-agent/agent/config"
)

func TestSubscriptionCommandsPersistBaseURL(t *testing.T) {
	bin := buildAgentBinary(t)
	state := filepath.Join(t.TempDir(), "state.db")
	url := "https://example.larksuite.com/base/basExampleAppToken001?table=tblExampleTable001&view=vewExampleView001"
	code, stdout, stderr := runAgent(t, bin, "--state", state, "subscription", "add", url)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"app_token":"basExampleAppToken001"`) ||
		!strings.Contains(stdout, `"view_id":"vewExampleView001"`) ||
		!strings.Contains(stdout, `"monitor_modes":["base_record","base_field","cloud_docs_notice"]`) {
		t.Fatalf("add stdout=%s", stdout)
	}
	code, stdout, stderr = runAgent(t, bin, "--state", state, "subscription", "list")
	if code != 0 || !strings.Contains(stdout, `"subscriptions"`) {
		t.Fatalf("list exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgent(t, bin, "--state", state, "subscription", "inspect", url)
	if code != 0 || !strings.Contains(stdout, `"table_id":"tblExampleTable001"`) {
		t.Fatalf("inspect exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgent(
		t,
		bin,
		"--state",
		state,
		"subscription",
		"remove",
		url,
		"--remote",
	)
	if code != 0 || !strings.Contains(stdout, `"status":"removed"`) {
		t.Fatalf("remove exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestSubscriptionCommandsPersistDocumentURL(t *testing.T) {
	bin := buildAgentBinary(t)
	state := filepath.Join(t.TempDir(), "state.db")
	url := "https://example.larksuite.com/docx/DocTokenABC123"
	code, stdout, stderr := runAgent(t, bin, "--state", state, "subscription", "add", url)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"resource_type":"document"`) ||
		!strings.Contains(stdout, `"file_token":"DocTokenABC123"`) {
		t.Fatalf("add stdout=%s", stdout)
	}
}

func TestSubscriptionRemoveRemoteSkipsDocumentAPIWhenWikiResolvesToBase(t *testing.T) {
	var wikiCalls atomic.Int32
	var documentSubscriptionCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/wiki/v2/spaces/get_node":
			wikiCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"node":{"obj_type":"bitable","obj_token":"bas_bug","title":"Bug 管理"}}}`))
		case "/open-apis/drive/v1/files/bas_bug/subscriptions":
			documentSubscriptionCalls.Add(1)
			http.Error(w, "Base must not use document comment subscriptions", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	bin := buildAgentBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	statePath := filepath.Join(dir, "state.db")
	env := []string{
		"LARK_AGENT_APP_SECRET=synthetic-secret",
		"LARK_AGENT_USER_ACCESS_TOKEN=synthetic-user-token",
		"LARK_AGENT_LARK_BASE_URL=" + server.URL,
	}
	code, _, stderr := runAgentWithEnv(
		t,
		env,
		bin,
		"--config",
		configPath,
		"init",
		"--workspace",
		dir,
		"--app-id",
		"cli_synthetic",
		"--owner-open-id",
		"ou_owner",
		"--owner-name",
		"测试负责人",
	)
	if code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr)
	}
	cfg, err := agentconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Lark.BaseURL = server.URL
	if err := agentconfig.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	resourceURL := "https://example.larksuite.com/wiki/wik_bug?table=tbl_bug"
	code, _, stderr = runAgentWithEnv(
		t,
		env,
		bin,
		"--config",
		configPath,
		"--state",
		statePath,
		"subscription",
		"add",
		resourceURL,
	)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr)
	}
	code, stdout, stderr := runAgentWithEnv(
		t,
		env,
		bin,
		"--config",
		configPath,
		"--state",
		statePath,
		"subscription",
		"remove",
		resourceURL,
		"--remote",
	)
	if code != 0 || !strings.Contains(stdout, `"status":"removed"`) {
		t.Fatalf("remove exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if wikiCalls.Load() != 1 || documentSubscriptionCalls.Load() != 0 {
		t.Fatalf(
			"wikiCalls=%d documentSubscriptionCalls=%d",
			wikiCalls.Load(),
			documentSubscriptionCalls.Load(),
		)
	}
}

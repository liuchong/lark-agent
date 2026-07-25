package larkagent_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/config"
)

func TestAuthStatusUsesConfiguredSDKCredentialBoundary(t *testing.T) {
	bin := buildAgentBinary(t)
	cfg := config.Default()
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Workspace.Root = t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAgentWithEnv(t, []string{
		"LARK_AGENT_APP_SECRET=redacted-test-value-one",
		"LARK_AGENT_USER_ACCESS_TOKEN=redacted-test-value-two",
	}, bin, "--config", configPath, "auth", "status")
	if code != 0 {
		t.Fatalf("auth status exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"configured":true`) ||
		strings.Contains(stdout, "redacted-test-value-one") ||
		strings.Contains(stdout, "redacted-test-value-two") {
		t.Fatalf("auth status stdout=%s", stdout)
	}
}

func TestAuthStatusAllowsBotOnlyCredentialBoundary(t *testing.T) {
	bin := buildAgentBinary(t)
	cfg := config.Default()
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Workspace.Root = t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runAgentWithEnv(t, []string{
		"LARK_AGENT_APP_SECRET=redacted-test-value-one",
	}, bin, "--config", configPath, "auth", "status")
	if code != 0 {
		t.Fatalf("auth status exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"configured":true`) ||
		strings.Contains(stdout, "redacted-test-value-one") {
		t.Fatalf("auth status stdout=%s", stdout)
	}
}

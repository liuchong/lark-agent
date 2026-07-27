package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestDefaultConfigRequiresWorkspace(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted missing workspace")
	}
}

func TestAgentTurnBudgetSupportsDeepInvestigation(t *testing.T) {
	cfg := validConfigForTest(t)
	if cfg.Agent.MaxTurns != 150 {
		t.Fatalf("default max turns=%d, want 150", cfg.Agent.MaxTurns)
	}
	if cfg.Agent.LoopTimeout != 2*time.Hour {
		t.Fatalf("default loop timeout=%s, want 2h", cfg.Agent.LoopTimeout)
	}
	cfg.Agent.MaxTurns = 300
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected 300 turns: %v", err)
	}
	cfg.Agent.MaxTurns = 301
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted more than 300 turns")
	}
}

func TestDefaultCodingConfig(t *testing.T) {
	cfg := validConfigForTest(t)
	if !cfg.Coding.Enabled || !cfg.Coding.RequireSourceRefs {
		t.Fatalf("coding defaults=%+v", cfg.Coding)
	}
	if cfg.Coding.MaxEvidenceFiles <= 0 || cfg.Coding.MaxLarkContextCalls <= 0 {
		t.Fatalf("coding limits=%+v", cfg.Coding)
	}
	cfg.Coding.MaxEvidenceFiles = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted invalid coding max evidence files")
	}
}

func TestDefaultHarnessConfig(t *testing.T) {
	cfg := validConfigForTest(t)
	if !cfg.FastPath.Enabled {
		t.Fatalf("fast path disabled by default: %+v", cfg.FastPath)
	}
	if cfg.FastPath.SimpleMaxTurns != 2 || cfg.FastPath.CodingMaxTurns != 20 {
		t.Fatalf("fast path turn budgets=%+v", cfg.FastPath)
	}
	if cfg.Scheduler.FastPathLease <= 0 ||
		cfg.Scheduler.PollIndexLookback <= 0 ||
		cfg.Scheduler.CodingQuestionLease <= cfg.Scheduler.SimpleLease ||
		cfg.Scheduler.ForegroundWorkers <= 0 ||
		cfg.Scheduler.BackgroundWorkers <= 0 {
		t.Fatalf("scheduler defaults=%+v", cfg.Scheduler)
	}
	if !cfg.ToolPolicy.DenyUnboundedShellSearch ||
		cfg.ToolPolicy.CodingMaxToolCalls != 10 ||
		cfg.ToolPolicy.MaxNoProgress != 3 {
		t.Fatalf("tool policy defaults=%+v", cfg.ToolPolicy)
	}
	if !cfg.Goal.Enabled || cfg.Goal.MaxActive <= 0 || cfg.Goal.MaxInvestigationTurns <= 0 {
		t.Fatalf("goal defaults=%+v", cfg.Goal)
	}
	cfg.Scheduler.FastPathLease = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted an invalid scheduler lease")
	}
	cfg = validConfigForTest(t)
	cfg.Scheduler.ForegroundWorkers = 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted no reserved interactive worker")
	}
}

func TestDefaultAssistantConfigAllowsOwnerInvocations(t *testing.T) {
	cfg := validConfigForTest(t)
	if !cfg.Assistant.OwnerDirect.Enabled {
		t.Fatalf("assistant defaults=%+v", cfg.Assistant)
	}
	if len(cfg.Assistant.Names) == 0 {
		t.Fatalf("assistant names are required: %+v", cfg.Assistant)
	}
	cfg.Assistant.OwnerDirect.Enabled = true
	cfg.Assistant.Names = nil
	cfg.Assistant.OpenIDs = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted owner direct mode without assistant names or open ids")
	}
}

func TestDefaultReplyScopeAllowsAllGroups(t *testing.T) {
	cfg := validConfigForTest(t)
	if cfg.Policy.ReplyScope != domain.ReplyScopeAllGroups {
		t.Fatalf("default reply scope=%q, want %q", cfg.Policy.ReplyScope, domain.ReplyScopeAllGroups)
	}
}

func TestValidateRejectsUnsupportedReplyScope(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Policy.ReplyScope = domain.ReplyScope("test_chat")
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "policy.reply_scope") {
		t.Fatalf("Validate error=%v", err)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg := validConfigForTest(t)
	cfg.Lark.AppID = "cli_test"
	cfg.Workspace.Root = root
	cfg.Model.Provider = "openai-compatible"
	cfg.Model.BaseURL = "https://example.test/v1"
	cfg.Model.Name = "test-model"
	cfg.Policy.Mode = domain.ModeAuto
	cfg.Policy.ReplyScope = domain.ReplyScopeConfiguredGroups

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace.Root != root ||
		got.Policy.Mode != domain.ModeAuto ||
		got.Policy.ReplyScope != domain.ReplyScopeConfiguredGroups {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func validConfigForTest(t *testing.T) Config {
	t.Helper()
	cfg := Default()
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Workspace.Root = t.TempDir()
	return cfg
}

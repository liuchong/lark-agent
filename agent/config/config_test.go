package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
)

func TestDefaultConfigRequiresWorkspace(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted missing workspace")
	}
}

func TestConfigRequiresConcreteOwnerName(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Owner.Name = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "owner.name") {
		t.Fatalf("missing owner name error=%v", err)
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

func TestDefaultContextImageAndInvestigationProgressConfig(t *testing.T) {
	cfg := Default()
	if cfg.Agent.MaxContextImages != 2 ||
		cfg.Agent.MaxContextImageBytes != 1<<20 ||
		cfg.Agent.MaxContextImageTotalBytes != 2<<20 {
		t.Fatalf("agent image limits=%+v", cfg.Agent)
	}
	if cfg.Policy.InvestigationProgress != "enabled" {
		t.Fatalf("investigation progress=%q", cfg.Policy.InvestigationProgress)
	}
	cfg.Agent.MaxContextImages = 3
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted more than two context images")
	}
}

func TestDefaultHarnessConfig(t *testing.T) {
	cfg := validConfigForTest(t)
	if !cfg.FastPath.Enabled {
		t.Fatalf("fast path disabled by default: %+v", cfg.FastPath)
	}
	if cfg.FastPath.SimpleMaxTurns != 3 || cfg.FastPath.CodingMaxTurns != 20 {
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
		cfg.ToolPolicy.CodingMaxToolCalls != 16 ||
		cfg.ToolPolicy.MaxNoProgress != 3 {
		t.Fatalf("tool policy defaults=%+v", cfg.ToolPolicy)
	}
	if cfg.Agent.MaxContextBytes != 64*1024 {
		t.Fatalf("max context bytes=%d, want %d", cfg.Agent.MaxContextBytes, 64*1024)
	}
	if cfg.Agent.ContextCompaction != 0.80 {
		t.Fatalf("context compaction ratio=%v", cfg.Agent.ContextCompaction)
	}
	if cfg.Owner.PreferredLanguage != agentlocale.LanguageAuto ||
		cfg.Owner.FallbackLanguage != agentlocale.LanguageChinese {
		t.Fatalf("owner language defaults=%+v", cfg.Owner)
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
	if cfg.Assistant.ReplyScope != domain.ReplyScopeAllGroups {
		t.Fatalf("default assistant reply scope=%q, want %q", cfg.Assistant.ReplyScope, domain.ReplyScopeAllGroups)
	}
	if cfg.Policy.ReplyScope != domain.ReplyScopeAllGroups {
		t.Fatalf("default delegated reply scope=%q, want %q", cfg.Policy.ReplyScope, domain.ReplyScopeAllGroups)
	}
	if cfg.Policy.PrivateReplyScope != domain.PrivateReplyScopeAll {
		t.Fatalf(
			"default private reply scope=%q, want %q",
			cfg.Policy.PrivateReplyScope,
			domain.PrivateReplyScopeAll,
		)
	}
	if cfg.Policy.OwnerWait != 3*time.Minute ||
		cfg.Policy.OwnerReplyConfidenceMin != 0.85 ||
		cfg.Policy.OwnerReplyRetry != 30*time.Second {
		t.Fatalf("semantic delegated reply defaults=%+v", cfg.Policy)
	}
}

func TestValidateRejectsInvalidSemanticOwnerReplyPolicy(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Policy.OwnerReplyConfidenceMin = 1.1
	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "policy.owner_reply_confidence_min") {
		t.Fatalf("confidence error=%v", err)
	}
	cfg = validConfigForTest(t)
	cfg.Policy.OwnerReplyRetry = 0
	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "policy.owner_reply_retry") {
		t.Fatalf("retry error=%v", err)
	}
}

func TestGitHubConfigIsDisabledByDefaultAndBoundedWhenEnabled(t *testing.T) {
	cfg := validConfigForTest(t)
	if cfg.GitHub.Enabled {
		t.Fatalf("github enabled by default: %+v", cfg.GitHub)
	}
	if cfg.GitHub.APIBaseURL != "https://api.github.com" ||
		cfg.GitHub.MaxFiles != 50 ||
		cfg.GitHub.MaxPatchBytes != 64*1024 ||
		cfg.GitHub.MaxAnnotations != 50 ||
		cfg.GitHub.MaxReviews != 50 {
		t.Fatalf("github defaults=%+v", cfg.GitHub)
	}
	cfg.GitHub.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "github.allowed_repositories") {
		t.Fatalf("enabled config without allowlist error=%v", err)
	}
	cfg.GitHub.AllowedRepositories = []string{"example/widgets"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.GitHub.AllowedRepositories = []string{"invalid"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted invalid repository")
	}
}

func TestLarkBaseURLMustBeAbsoluteWhenConfigured(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Lark.BaseURL = "open.larksuite.com"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "lark.base_url") {
		t.Fatalf("accepted invalid Lark base URL: %v", err)
	}
	cfg.Lark.BaseURL = "https://open.larksuite.com"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rejected valid Lark base URL: %v", err)
	}
}

func TestValidateRejectsUnsupportedReplyScope(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Policy.ReplyScope = domain.ReplyScope("test_chat")
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "policy.reply_scope") {
		t.Fatalf("Validate error=%v", err)
	}
	cfg = validConfigForTest(t)
	cfg.Assistant.ReplyScope = domain.ReplyScope("test_chat")
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "assistant.reply_scope") {
		t.Fatalf("Validate assistant reply scope error=%v", err)
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
	cfg.Assistant.ReplyScope = domain.ReplyScopeConfiguredGroups
	cfg.Policy.ReplyScope = domain.ReplyScopeConfiguredGroups
	cfg.GitHub.Enabled = true
	cfg.GitHub.AllowedRepositories = []string{"example/widgets"}

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
		got.Assistant.ReplyScope != domain.ReplyScopeConfiguredGroups ||
		got.Policy.ReplyScope != domain.ReplyScopeConfiguredGroups ||
		!got.GitHub.Enabled ||
		len(got.GitHub.AllowedRepositories) != 1 ||
		got.GitHub.AllowedRepositories[0] != "example/widgets" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func validConfigForTest(t *testing.T) Config {
	t.Helper()
	cfg := Default()
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Workspace.Root = t.TempDir()
	return cfg
}

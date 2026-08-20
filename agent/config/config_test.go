package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	modelruntime "github.com/liuchong/lark-agent/agent/runtime/model"
	"github.com/liuchong/lark-agent/agent/taskrules"
	"gopkg.in/yaml.v3"
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
	if !cfg.Coding.Enabled {
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

func TestRetentionDaysMustBePositive(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Retention.Days = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "retention.days") {
		t.Fatalf("retention validation error=%v", err)
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
	if cfg.FastPath.SimpleMaxTurns != 3 || cfg.FastPath.CodingMaxTurns != 100 {
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

func TestDefaultModelProfilesAndRoleBindings(t *testing.T) {
	cfg := validConfigForTest(t)
	if cfg.Version != 6 {
		t.Fatalf("version=%d, want 6", cfg.Version)
	}
	primary, ok := cfg.Model.Profiles["primary"]
	if !ok {
		t.Fatalf("primary profile missing: %+v", cfg.Model.Profiles)
	}
	if primary.Provider != "kimi" ||
		primary.Protocol != "openai_chat" ||
		primary.BaseURL != "https://api.kimi.com/coding/v1" ||
		primary.Name != "k3-256k" ||
		primary.CredentialKeychainKey != "model/primary/api-key" ||
		primary.Reasoning.Mode != "provider_default" {
		t.Fatalf("primary profile=%+v", primary)
	}
	for role, profile := range map[string]string{
		"agent":     cfg.Model.Roles.Agent,
		"semantic":  cfg.Model.Roles.Semantic,
		"finalizer": cfg.Model.Roles.Finalizer,
		"compactor": cfg.Model.Roles.Compactor,
		"vision":    cfg.Model.Roles.Vision,
	} {
		if profile != "primary" {
			t.Fatalf("role %s profile=%q", role, profile)
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "api_key") ||
		strings.Contains(string(data), "sk-test") {
		t.Fatalf("model config serialized a secret-shaped field:\n%s", data)
	}
}

// TestDefaultModelProfileCarriesItsOwnCallBudget pins the shipped per-call
// budget: one attempt is long enough for a reasoning model to answer a long
// prompt, and a single provider blip does not fail the run.
func TestDefaultModelProfileCarriesItsOwnCallBudget(t *testing.T) {
	cfg := validConfigForTest(t)
	primary := cfg.Model.Profiles["primary"]
	if primary.Timeout != 120*time.Second {
		t.Fatalf("primary timeout=%s, want 120s", primary.Timeout)
	}
	if primary.MaxAttempts != 3 {
		t.Fatalf("primary max_attempts=%d, want 3", primary.MaxAttempts)
	}
	if cfg.Model.Timeout != 120*time.Second {
		t.Fatalf("model timeout=%s, want 120s", cfg.Model.Timeout)
	}
}

func TestValidateRejectsUnusableModelAttemptBudget(t *testing.T) {
	for _, attempts := range []int{-1, 0, 11} {
		cfg := validConfigForTest(t)
		primary := cfg.Model.Profiles["primary"]
		primary.MaxAttempts = attempts
		cfg.Model.Profiles["primary"] = primary
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "max_attempts") {
			t.Fatalf("max_attempts=%d error=%v", attempts, err)
		}
	}
}

// TestRuntimeProfileCarriesDeclaredProviderTraits proves the declared reasoning
// and capability fields survive the trip to the model runtime, so no calling
// path can build a request from part of a profile.
func TestRuntimeProfileCarriesDeclaredProviderTraits(t *testing.T) {
	cfg := validConfigForTest(t)
	primary := cfg.Model.Profiles["primary"]
	primary.Reasoning = ModelReasoningConfig{Mode: "enabled", Effort: "high"}
	primary.Capabilities.MaxOutputTokens = 4096

	runtime := primary.RuntimeProfile("primary")
	if runtime.Name != "primary" || runtime.Model != "k3-256k" {
		t.Fatalf("runtime profile identity=%+v", runtime)
	}
	if runtime.Provider != modelruntime.ProviderKimi ||
		runtime.Protocol != modelruntime.ProtocolOpenAIChat ||
		runtime.Stream != modelruntime.StreamAuto {
		t.Fatalf("runtime profile transport=%+v", runtime)
	}
	if runtime.Reasoning.Mode != modelruntime.ReasoningEnabled || runtime.Reasoning.Effort != "high" {
		t.Fatalf("runtime reasoning=%+v", runtime.Reasoning)
	}
	if !runtime.Capabilities.Thinking || !runtime.Capabilities.ToolUse ||
		runtime.Capabilities.MaxOutputTokens != 4096 {
		t.Fatalf("runtime capabilities=%+v", runtime.Capabilities)
	}
	if runtime.Timeout != primary.Timeout {
		t.Fatalf("runtime timeout=%s want %s", runtime.Timeout, primary.Timeout)
	}
}

func TestLoadV4ModelConfigMigratesToPrimaryProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig := `
version: 4
lark:
  app_id: cli_test
owner:
  open_id: ou_owner
  name: 测试负责人
assistant:
  names: ["Lark Agent"]
model:
  provider: openai-compatible
  base_url: https://api.kimi.com/coding/v1
  name: k3-256k
  timeout: 45s
workspace:
  root: ` + t.TempDir() + `
`
	mustWriteConfigFile(t, path, writeConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 6 {
		t.Fatalf("migrated version=%d", cfg.Version)
	}
	primary := cfg.Model.Profiles["primary"]
	if primary.Provider != "kimi" ||
		primary.Protocol != "openai_chat" ||
		primary.BaseURL != "https://api.kimi.com/coding/v1" ||
		primary.Name != "k3-256k" ||
		primary.Timeout != 45*time.Second {
		t.Fatalf("migrated primary=%+v", primary)
	}
	if cfg.Model.Roles.Agent != "primary" || cfg.Model.Roles.Finalizer != "primary" {
		t.Fatalf("migrated roles=%+v", cfg.Model.Roles)
	}
}

func TestLoadLegacyModelProfileCanUseNonSecretEnvDefaults(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://api.kimi.com/coding/v1")
	t.Setenv("OPENAI_MODEL", "k3-256k")
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig := `
version: 4
lark:
  app_id: cli_test
owner:
  open_id: ou_owner
  name: 测试负责人
assistant:
  names: ["Lark Agent"]
model:
  profiles: {}
  roles:
    agent: primary
workspace:
  root: ` + t.TempDir() + `
`
	mustWriteConfigFile(t, path, writeConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	primary := cfg.Model.Profiles["primary"]
	if primary.Provider != "kimi" ||
		primary.BaseURL != "https://api.kimi.com/coding/v1" ||
		primary.Name != "k3-256k" {
		t.Fatalf("primary=%+v", primary)
	}
}

func TestValidateRejectsInvalidModelRoleBinding(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.Model.Roles.Finalizer = "missing"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "finalizer") {
		t.Fatalf("role binding error=%v", err)
	}
	cfg = validConfigForTest(t)
	cfg.Model.Profiles["primary"] = ModelProfileConfig{
		Provider:              "kimi",
		Protocol:              "anthropic_messages",
		BaseURL:               "https://api.kimi.com/coding/v1",
		Name:                  "k3-256k",
		CredentialKeychainKey: "model/primary/api-key",
		Reasoning:             ModelReasoningConfig{Mode: "provider_default"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "anthropic_messages") {
		t.Fatalf("profile protocol error=%v", err)
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
		cfg.Policy.OwnerReplyRetry != 30*time.Second ||
		cfg.Policy.OwnerReplyMaxRetries != 3 {
		t.Fatalf("semantic delegated reply defaults=%+v", cfg.Policy)
	}
}

func TestDefaultDelegatedReplyConfidenceSendsVerifiedLowRiskReplies(t *testing.T) {
	cfg := Default()
	if cfg.Policy.ReplyConfidenceMin != 0.70 {
		t.Fatalf("reply confidence min=%v, want 0.70", cfg.Policy.ReplyConfidenceMin)
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
	cfg = validConfigForTest(t)
	cfg.Policy.OwnerReplyMaxRetries = 0
	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "policy.owner_reply_max_retries") {
		t.Fatalf("max retries error=%v", err)
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

func TestExistingInstallKeepsTaskRulesDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	mustWriteConfigFile(t, path, `
version: 5
lark:
  app_id: cli_test
owner:
  open_id: ou_owner
  name: 测试负责人
workspace:
  root: `+t.TempDir()+`
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 6 {
		t.Fatalf("version=%d, want 6", cfg.Version)
	}
	if cfg.TaskRules.Enabled {
		t.Fatal("existing install enabled private task rules by default")
	}
	if cfg.TaskRules.Path != taskrules.DefaultFileName {
		t.Fatalf("path=%q", cfg.TaskRules.Path)
	}
	if cfg.TaskRules.MaxBytes != taskrules.DefaultMaxBytes {
		t.Fatalf("max_bytes=%d", cfg.TaskRules.MaxBytes)
	}
	if cfg.ConfigDirectory() != filepath.Dir(path) {
		t.Fatalf("config dir=%q", cfg.ConfigDirectory())
	}
}

func TestTaskRulesPathMustStayInsideConfigDirectory(t *testing.T) {
	cfg := validConfigForTest(t)
	cfg.TaskRules.Path = "../TASK_RULES.md"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "task_rules.path") {
		t.Fatalf("escaped path error=%v", err)
	}
	cfg = validConfigForTest(t)
	cfg.TaskRules.Path = "/tmp/TASK_RULES.md"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "task_rules.path") {
		t.Fatalf("absolute path error=%v", err)
	}
	cfg = validConfigForTest(t)
	cfg.TaskRules.MaxBytes = 16
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "task_rules.max_bytes") {
		t.Fatalf("tiny max_bytes error=%v", err)
	}
}

func mustWriteConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

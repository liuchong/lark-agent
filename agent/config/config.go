// Package config loads and validates lark-agent configuration.
package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
	"github.com/liuchong/lark-agent/internal/fsx"
)

const currentVersion = 3

// Config is the YAML configuration stored under the standalone lark-agent config directory.
type Config struct {
	Version    int              `json:"version" yaml:"version"`
	Lark       LarkConfig       `json:"lark" yaml:"lark"`
	Owner      OwnerConfig      `json:"owner" yaml:"owner"`
	Assistant  AssistantConfig  `json:"assistant" yaml:"assistant"`
	Model      ModelConfig      `json:"model" yaml:"model"`
	Agent      AgentConfig      `json:"agent" yaml:"agent"`
	FastPath   FastPathConfig   `json:"fast_path" yaml:"fast_path"`
	Scheduler  SchedulerConfig  `json:"scheduler" yaml:"scheduler"`
	Coding     CodingConfig     `json:"coding" yaml:"coding"`
	ToolPolicy ToolPolicyConfig `json:"tool_policy" yaml:"tool_policy"`
	Goal       GoalConfig       `json:"goal" yaml:"goal"`
	State      StateConfig      `json:"state" yaml:"state"`
	Policy     PolicyConfig     `json:"policy" yaml:"policy"`
	Workspace  WorkspaceConfig  `json:"workspace" yaml:"workspace"`
	Retention  RetentionConfig  `json:"retention" yaml:"retention"`
}

// AgentConfig bounds the multi-step loop and workspace shell.
type AgentConfig struct {
	MaxTurns           int           `json:"max_turns" yaml:"max_turns"`
	MaxRetries         int           `json:"max_retries" yaml:"max_retries"`
	MaxToolOutput      int           `json:"max_tool_output_bytes" yaml:"max_tool_output_bytes"`
	MaxTotalToolOutput int           `json:"max_total_tool_output_bytes" yaml:"max_total_tool_output_bytes"`
	MaxContextBytes    int           `json:"max_context_bytes" yaml:"max_context_bytes"`
	LoopTimeout        time.Duration `json:"loop_timeout" yaml:"loop_timeout"`
	MaxRepeatedCalls   int           `json:"max_repeated_calls" yaml:"max_repeated_calls"`
	ShellTimeout       time.Duration `json:"shell_timeout" yaml:"shell_timeout"`
	ShellApproval      bool          `json:"shell_approval" yaml:"shell_approval"`
}

// CodingConfig controls read-only coding investigations.
type CodingConfig struct {
	Enabled             bool              `json:"enabled" yaml:"enabled"`
	MaxEvidenceFiles    int               `json:"max_evidence_files" yaml:"max_evidence_files"`
	MaxLarkContextCalls int               `json:"max_lark_context_calls" yaml:"max_lark_context_calls"`
	RequireSourceRefs   bool              `json:"require_source_refs" yaml:"require_source_refs"`
	ToolPermission      map[string]string `json:"tool_permission,omitempty" yaml:"tool_permission,omitempty"`
}

// FastPathConfig controls deterministic owner-only local answers.
type FastPathConfig struct {
	Enabled        bool `json:"enabled" yaml:"enabled"`
	SimpleMaxTurns int  `json:"simple_max_turns" yaml:"simple_max_turns"`
	CodingMaxTurns int  `json:"coding_max_turns" yaml:"coding_max_turns"`
}

// SchedulerConfig controls foreground/background lane behavior.
type SchedulerConfig struct {
	DuplicateWindow     time.Duration `json:"duplicate_window" yaml:"duplicate_window"`
	PollIndexLookback   time.Duration `json:"poll_index_lookback" yaml:"poll_index_lookback"`
	FastPathLease       time.Duration `json:"fast_path_lease" yaml:"fast_path_lease"`
	SimpleLease         time.Duration `json:"simple_lease" yaml:"simple_lease"`
	CodingQuestionLease time.Duration `json:"coding_question_lease" yaml:"coding_question_lease"`
	CodingGoalLease     time.Duration `json:"coding_goal_lease" yaml:"coding_goal_lease"`
	ForegroundWorkers   int           `json:"foreground_workers" yaml:"foreground_workers"`
	BackgroundWorkers   int           `json:"background_workers" yaml:"background_workers"`
}

// ToolPolicyConfig controls model-visible tool safety gates.
type ToolPolicyConfig struct {
	DenyUnboundedShellSearch bool `json:"deny_unbounded_shell_search" yaml:"deny_unbounded_shell_search"`
	CodingMaxToolCalls       int  `json:"coding_max_tool_calls" yaml:"coding_max_tool_calls"`
	MaxNoProgress            int  `json:"max_no_progress" yaml:"max_no_progress"`
}

// GoalConfig controls durable coding follow-up.
type GoalConfig struct {
	Enabled               bool `json:"enabled" yaml:"enabled"`
	MaxActive             int  `json:"max_active" yaml:"max_active"`
	MaxInvestigationTurns int  `json:"max_investigation_turns" yaml:"max_investigation_turns"`
}

// StateConfig controls local state lifecycle during live installs.
type StateConfig struct {
	AllowReset bool `json:"allow_reset" yaml:"allow_reset"`
}

// LarkConfig stores public SDK settings and references to Keychain-held
// credentials. Secrets and tokens are never serialized into this struct.
type LarkConfig struct {
	AppID                   string                 `json:"app_id" yaml:"app_id"`
	Brand                   string                 `json:"brand" yaml:"brand"`
	BaseURL                 string                 `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	KeychainService         string                 `json:"keychain_service" yaml:"keychain_service"`
	AppSecretKeychainKey    string                 `json:"app_secret_keychain_key" yaml:"app_secret_keychain_key"`
	UserTokenKeychainKey    string                 `json:"user_token_keychain_key" yaml:"user_token_keychain_key"`
	RefreshTokenKeychainKey string                 `json:"refresh_token_keychain_key" yaml:"refresh_token_keychain_key"`
	OAuthCallback           string                 `json:"oauth_callback,omitempty" yaml:"oauth_callback,omitempty"`
	Subscriptions           []ResourceSubscription `json:"subscriptions,omitempty" yaml:"subscriptions,omitempty"`
}

// ResourceSubscription is the config-level projection used before the durable
// SQLite subscription row is synchronized.
type ResourceSubscription struct {
	ID                   string   `json:"id" yaml:"id"`
	URL                  string   `json:"url" yaml:"url"`
	ResourceType         string   `json:"resource_type" yaml:"resource_type"`
	FileToken            string   `json:"file_token,omitempty" yaml:"file_token,omitempty"`
	AppToken             string   `json:"app_token,omitempty" yaml:"app_token,omitempty"`
	WikiNodeToken        string   `json:"wiki_node_token,omitempty" yaml:"wiki_node_token,omitempty"`
	TableID              string   `json:"table_id,omitempty" yaml:"table_id,omitempty"`
	ViewID               string   `json:"view_id,omitempty" yaml:"view_id,omitempty"`
	MonitorModes         []string `json:"monitor_modes,omitempty" yaml:"monitor_modes,omitempty"`
	RemoteSubscriptionID string   `json:"remote_subscription_id,omitempty" yaml:"remote_subscription_id,omitempty"`
	Status               string   `json:"status" yaml:"status"`
	Cursor               string   `json:"cursor,omitempty" yaml:"cursor,omitempty"`
	LastError            string   `json:"last_error,omitempty" yaml:"last_error,omitempty"`
}

// Paths are the standalone agent's platform-local directories.
type Paths struct {
	ConfigDir string
	StateDir  string
	LogDir    string
}

// DefaultPaths returns paths that are independent from external CLI configuration.
func DefaultPaths(home string) Paths {
	return Paths{
		ConfigDir: filepath.Join(home, ".config", "lark-agent"),
		StateDir:  filepath.Join(home, "Library", "Application Support", "lark-agent"),
		LogDir:    filepath.Join(home, "Library", "Logs", "lark-agent"),
	}
}

// OwnerConfig identifies the human owner.
type OwnerConfig struct {
	OpenID string `json:"open_id" yaml:"open_id"`
}

// AssistantConfig identifies bot-facing owner request entry points.
type AssistantConfig struct {
	OpenIDs     []string                 `json:"open_ids,omitempty" yaml:"open_ids,omitempty"`
	Names       []string                 `json:"names,omitempty" yaml:"names,omitempty"`
	OwnerDirect OwnerDirectRequestConfig `json:"owner_direct" yaml:"owner_direct"`
}

// OwnerDirectRequestConfig controls owner-only bot invocation routing.
type OwnerDirectRequestConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// ModelConfig configures an OpenAI-compatible endpoint.
type ModelConfig struct {
	Provider string        `json:"provider" yaml:"provider"`
	BaseURL  string        `json:"base_url" yaml:"base_url"`
	Name     string        `json:"name" yaml:"name"`
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`
}

// PolicyConfig controls routing and reply behavior.
type PolicyConfig struct {
	Mode               domain.Mode        `json:"mode" yaml:"mode"`
	ReplyScope         domain.ReplyScope  `json:"reply_scope" yaml:"reply_scope"`
	Sensitivity        domain.Sensitivity `json:"sensitivity" yaml:"sensitivity"`
	OwnerWait          time.Duration      `json:"owner_wait" yaml:"owner_wait"`
	MentionPoll        time.Duration      `json:"mention_poll" yaml:"mention_poll"`
	ReplyConfidenceMin float64            `json:"reply_confidence_min" yaml:"reply_confidence_min"`
	AllowChats         []string           `json:"allow_chats,omitempty" yaml:"allow_chats,omitempty"`
	BlockChats         []string           `json:"block_chats,omitempty" yaml:"block_chats,omitempty"`
	BlockUsers         []string           `json:"block_users,omitempty" yaml:"block_users,omitempty"`
}

// WorkspaceConfig defines the single local context boundary.
type WorkspaceConfig struct {
	Root     string   `json:"root" yaml:"root"`
	Excludes []string `json:"excludes,omitempty" yaml:"excludes,omitempty"`
}

// RetentionConfig controls local audit retention.
type RetentionConfig struct {
	Days int `json:"days" yaml:"days"`
}

// Default returns conservative defaults. Workspace is intentionally empty and
// must be set explicitly.
func Default() Config {
	return Config{
		Version: currentVersion,
		Lark: LarkConfig{
			Brand:                   "feishu",
			KeychainService:         "lark-agent",
			AppSecretKeychainKey:    "app_secret",
			UserTokenKeychainKey:    "user_access_token",
			RefreshTokenKeychainKey: "user_refresh_token",
		},
		Assistant: AssistantConfig{
			Names:       []string{"Lark Agent", "lark-agent", "机器人", "Agent"},
			OwnerDirect: OwnerDirectRequestConfig{Enabled: true},
		},
		Model: ModelConfig{
			Provider: "openai-compatible",
			Timeout:  60 * time.Second,
		},
		Agent: AgentConfig{
			MaxTurns:           150,
			MaxRetries:         20,
			MaxToolOutput:      32 * 1024,
			MaxTotalToolOutput: 128 * 1024,
			MaxContextBytes:    192 * 1024,
			LoopTimeout:        2 * time.Hour,
			MaxRepeatedCalls:   3,
			ShellTimeout:       2 * time.Minute,
			ShellApproval:      false,
		},
		FastPath: FastPathConfig{Enabled: true, SimpleMaxTurns: 2, CodingMaxTurns: 20},
		Scheduler: SchedulerConfig{
			DuplicateWindow:     2 * time.Minute,
			PollIndexLookback:   2 * time.Minute,
			FastPathLease:       time.Minute,
			SimpleLease:         5 * time.Minute,
			CodingQuestionLease: 30 * time.Minute,
			CodingGoalLease:     2 * time.Hour,
			ForegroundWorkers:   2,
			BackgroundWorkers:   1,
		},
		Coding: CodingConfig{
			Enabled:             true,
			MaxEvidenceFiles:    12,
			MaxLarkContextCalls: 2,
			RequireSourceRefs:   true,
			ToolPermission: map[string]string{
				"read_workspace":        "allow",
				"search_code_symbols":   "allow",
				"trace_code_path":       "allow",
				"explore_workspace":     "allow",
				"shell":                 "allow",
				"direct_lark_im_send":   "deny",
				"production_file_write": "deny",
			},
		},
		ToolPolicy: ToolPolicyConfig{
			DenyUnboundedShellSearch: true,
			CodingMaxToolCalls:       10,
			MaxNoProgress:            3,
		},
		Goal:  GoalConfig{Enabled: true, MaxActive: 3, MaxInvestigationTurns: 150},
		State: StateConfig{AllowReset: false},
		Policy: PolicyConfig{
			Mode:               domain.ModeAuto,
			ReplyScope:         domain.ReplyScopeAllGroups,
			Sensitivity:        domain.SensitivityNormal,
			OwnerWait:          60 * time.Second,
			MentionPoll:        30 * time.Second,
			ReplyConfidenceMin: 0.85,
		},
		Workspace: WorkspaceConfig{
			Excludes: []string{".git", ".env*", "node_modules", "vendor", "dist", "build", "*.pem", "*.key"},
		},
		Retention: RetentionConfig{Days: 30},
	}
}

// Load reads and validates a config file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errs.NewConfigError(errs.SubtypeNotConfigured, "read agent config: %s", path).
			WithHint("run `lark-agent init --workspace <absolute-dir>` first").
			WithCause(err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, errs.NewConfigError(errs.SubtypeInvalidConfig, "parse agent config: %s", path).WithCause(err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Save writes a config file atomically.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errs.NewInternalError(errs.SubtypeFileIO, "create config directory: %s", filepath.Dir(path)).WithCause(err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "marshal agent config").WithCause(err)
	}
	if err := fsx.AtomicWrite(path, data, 0o600); err != nil {
		return errs.NewInternalError(errs.SubtypeFileIO, "write agent config: %s", path).WithCause(err)
	}
	return nil
}

// Validate checks semantic configuration constraints.
func (c Config) Validate() error {
	if c.Version == 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent config version is required").WithField("version")
	}
	if c.Version > currentVersion {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent config version %d is newer than supported version %d", c.Version, currentVersion).WithField("version")
	}
	if c.Lark.AppID == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "lark.app_id is required").WithField("lark.app_id")
	}
	if c.Lark.KeychainService == "" || c.Lark.AppSecretKeychainKey == "" ||
		c.Lark.UserTokenKeychainKey == "" || c.Lark.RefreshTokenKeychainKey == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "lark keychain references are required").WithField("lark.keychain")
	}
	if c.Owner.OpenID == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "owner.open_id is required").WithField("owner.open_id")
	}
	if _, err := domain.ParseMode(string(c.Policy.Mode)); err != nil {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "invalid policy.mode: %s", c.Policy.Mode).
			WithField("policy.mode").
			WithCause(err)
	}
	if _, err := domain.ParseReplyScope(string(c.Policy.ReplyScope)); err != nil {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "invalid policy.reply_scope: %s", c.Policy.ReplyScope).
			WithField("policy.reply_scope").
			WithCause(err)
	}
	if c.Policy.OwnerWait <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "policy.owner_wait must be positive").WithField("policy.owner_wait")
	}
	if c.Policy.MentionPoll <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "policy.mention_poll must be positive").WithField("policy.mention_poll")
	}
	if c.Policy.ReplyConfidenceMin < 0 || c.Policy.ReplyConfidenceMin > 1 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "policy.reply_confidence_min must be between 0 and 1").WithField("policy.reply_confidence_min")
	}
	if c.Assistant.OwnerDirect.Enabled && len(c.Assistant.OpenIDs) == 0 && len(c.Assistant.Names) == 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "assistant owner_direct requires assistant.open_ids or assistant.names").WithField("assistant")
	}
	if c.Agent.MaxTurns <= 0 || c.Agent.MaxTurns > 300 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent.max_turns must be between 1 and 300").WithField("agent.max_turns")
	}
	if c.Agent.MaxRetries <= 0 || c.Agent.MaxRetries > 100 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent.max_retries must be between 1 and 100").WithField("agent.max_retries")
	}
	if c.Agent.MaxToolOutput <= 0 || c.Agent.MaxTotalToolOutput < c.Agent.MaxToolOutput {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent tool output budgets are invalid").WithField("agent.max_total_tool_output_bytes")
	}
	if c.Agent.MaxContextBytes < 32*1024 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent.max_context_bytes must be at least 32768").WithField("agent.max_context_bytes")
	}
	if c.Agent.LoopTimeout <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent.loop_timeout must be positive").WithField("agent.loop_timeout")
	}
	if c.Agent.MaxRepeatedCalls <= 0 || c.Agent.MaxRepeatedCalls > 10 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent.max_repeated_calls must be between 1 and 10").WithField("agent.max_repeated_calls")
	}
	if c.Agent.ShellTimeout <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "agent.shell_timeout must be positive").WithField("agent.shell_timeout")
	}
	if c.Scheduler.DuplicateWindow <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "scheduler.duplicate_window must be positive").WithField("scheduler.duplicate_window")
	}
	if c.Scheduler.PollIndexLookback <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "scheduler.poll_index_lookback must be positive").WithField("scheduler.poll_index_lookback")
	}
	if c.Scheduler.FastPathLease <= 0 || c.Scheduler.SimpleLease <= 0 || c.Scheduler.CodingQuestionLease <= 0 || c.Scheduler.CodingGoalLease <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "scheduler leases must be positive").WithField("scheduler")
	}
	if c.Scheduler.ForegroundWorkers < 2 || c.Scheduler.BackgroundWorkers <= 0 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"scheduler requires at least two foreground workers and one background worker",
		).WithField("scheduler")
	}
	if c.Coding.MaxEvidenceFiles <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "coding.max_evidence_files must be positive").WithField("coding.max_evidence_files")
	}
	if c.Coding.MaxLarkContextCalls <= 0 || c.Coding.MaxLarkContextCalls > 10 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "coding.max_lark_context_calls must be between 1 and 10").WithField("coding.max_lark_context_calls")
	}
	if c.Goal.MaxActive <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "goal.max_active must be positive").WithField("goal.max_active")
	}
	if c.FastPath.SimpleMaxTurns <= 0 || c.FastPath.CodingMaxTurns <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "fast_path turn budgets must be positive").WithField("fast_path")
	}
	if c.ToolPolicy.CodingMaxToolCalls <= 0 || c.ToolPolicy.MaxNoProgress <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "tool_policy budgets must be positive").WithField("tool_policy")
	}
	if c.Goal.MaxInvestigationTurns <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "goal.max_investigation_turns must be positive").WithField("goal.max_investigation_turns")
	}
	if c.Workspace.Root == "" || !filepath.IsAbs(c.Workspace.Root) {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"workspace.root must be an absolute path",
		).WithField("workspace.root")
	}
	return nil
}

// Package config loads and validates lark-agent configuration.
package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/agent/taskrules"
	errs "github.com/liuchong/lark-agent/internal/apperr"
	"github.com/liuchong/lark-agent/internal/fsx"
)

const currentVersion = 6

// Config is the YAML configuration stored under the standalone lark-agent config directory.
type Config struct {
	Version    int              `json:"version" yaml:"version"`
	Lark       LarkConfig       `json:"lark" yaml:"lark"`
	GitHub     GitHubConfig     `json:"github" yaml:"github"`
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
	TaskRules  TaskRulesConfig  `json:"task_rules" yaml:"task_rules"`
	configDir  string           `json:"-" yaml:"-"`
}

// AgentConfig bounds the multi-step loop and workspace shell.
type AgentConfig struct {
	MaxTurns                  int           `json:"max_turns" yaml:"max_turns"`
	MaxRetries                int           `json:"max_retries" yaml:"max_retries"`
	MaxToolOutput             int           `json:"max_tool_output_bytes" yaml:"max_tool_output_bytes"`
	MaxTotalToolOutput        int           `json:"max_total_tool_output_bytes" yaml:"max_total_tool_output_bytes"`
	MaxContextBytes           int           `json:"max_context_bytes" yaml:"max_context_bytes"`
	ContextCompaction         float64       `json:"context_compaction_ratio" yaml:"context_compaction_ratio"`
	LoopTimeout               time.Duration `json:"loop_timeout" yaml:"loop_timeout"`
	MaxRepeatedCalls          int           `json:"max_repeated_calls" yaml:"max_repeated_calls"`
	ShellTimeout              time.Duration `json:"shell_timeout" yaml:"shell_timeout"`
	ShellApproval             bool          `json:"shell_approval" yaml:"shell_approval"`
	VisionModel               string        `json:"vision_model,omitempty" yaml:"vision_model,omitempty"`
	MaxContextImages          int           `json:"max_context_images" yaml:"max_context_images"`
	MaxContextImageBytes      int64         `json:"max_context_image_bytes" yaml:"max_context_image_bytes"`
	MaxContextImageTotalBytes int64         `json:"max_context_image_total_bytes" yaml:"max_context_image_total_bytes"`
}

// CodingConfig controls read-only coding investigations.
type CodingConfig struct {
	Enabled             bool              `json:"enabled" yaml:"enabled"`
	MaxEvidenceFiles    int               `json:"max_evidence_files" yaml:"max_evidence_files"`
	MaxLarkContextCalls int               `json:"max_lark_context_calls" yaml:"max_lark_context_calls"`
	RequireSourceRefs   bool              `json:"require_source_refs" yaml:"require_source_refs"`
	ToolPermission      map[string]string `json:"tool_permission,omitempty" yaml:"tool_permission,omitempty"`
}

// FastPathConfig controls deterministic local answers for direct requests.
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

// GitHubConfig controls the optional trusted GitHub evidence bridge. Tokens are
// referenced by Keychain account and never serialized here.
type GitHubConfig struct {
	Enabled              bool                        `json:"enabled" yaml:"enabled"`
	APIBaseURL           string                      `json:"api_base_url" yaml:"api_base_url"`
	TokenKeychainService string                      `json:"token_keychain_service" yaml:"token_keychain_service"`
	TokenKeychainKey     string                      `json:"token_keychain_key" yaml:"token_keychain_key"`
	AllowedRepositories  []string                    `json:"allowed_repositories,omitempty" yaml:"allowed_repositories,omitempty"`
	MaxFiles             int                         `json:"max_files" yaml:"max_files"`
	MaxPatchBytes        int                         `json:"max_patch_bytes" yaml:"max_patch_bytes"`
	MaxAnnotations       int                         `json:"max_annotations" yaml:"max_annotations"`
	MaxReviews           int                         `json:"max_reviews" yaml:"max_reviews"`
	ProactiveReview      GitHubProactiveReviewConfig `json:"proactive_review" yaml:"proactive_review"`
}

// GitHubProactiveReviewConfig controls optional private Owner review of
// unmentioned pull-request requests in exact allowlisted chats.
type GitHubProactiveReviewConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	ChatIDs []string `json:"chat_ids,omitempty" yaml:"chat_ids,omitempty"`
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
	OpenID            string               `json:"open_id" yaml:"open_id"`
	Name              string               `json:"name,omitempty" yaml:"name,omitempty"`
	PreferredLanguage agentlocale.Language `json:"preferred_language" yaml:"preferred_language"`
	FallbackLanguage  agentlocale.Language `json:"fallback_language" yaml:"fallback_language"`
}

// AssistantConfig identifies bot-facing request entry points.
type AssistantConfig struct {
	OpenIDs     []string                 `json:"open_ids,omitempty" yaml:"open_ids,omitempty"`
	Names       []string                 `json:"names,omitempty" yaml:"names,omitempty"`
	ReplyScope  domain.ReplyScope        `json:"reply_scope" yaml:"reply_scope"`
	OwnerDirect OwnerDirectRequestConfig `json:"owner_direct" yaml:"owner_direct"`
}

// OwnerDirectRequestConfig controls owner-only bot invocation routing.
type OwnerDirectRequestConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// ModelConfig configures role-bound model profiles. Provider/BaseURL/Name are
// retained as legacy v4 fields and as a temporary mirror for code paths not yet
// switched to role-bound profiles. They are not credential fields.
type ModelConfig struct {
	Provider string                        `json:"provider,omitempty" yaml:"provider,omitempty"`
	BaseURL  string                        `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	Name     string                        `json:"name,omitempty" yaml:"name,omitempty"`
	Timeout  time.Duration                 `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Profiles map[string]ModelProfileConfig `json:"profiles,omitempty" yaml:"profiles,omitempty"`
	Roles    ModelRoleBindingsConfig       `json:"roles,omitempty" yaml:"roles,omitempty"`
}

type ModelProfileConfig struct {
	Provider              string                  `json:"provider" yaml:"provider"`
	Protocol              string                  `json:"protocol" yaml:"protocol"`
	BaseURL               string                  `json:"base_url" yaml:"base_url"`
	Name                  string                  `json:"name" yaml:"name"`
	KeychainService       string                  `json:"keychain_service,omitempty" yaml:"keychain_service,omitempty"`
	CredentialKeychainKey string                  `json:"credential_keychain_key" yaml:"credential_keychain_key"`
	Timeout               time.Duration           `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Stream                string                  `json:"stream,omitempty" yaml:"stream,omitempty"`
	Reasoning             ModelReasoningConfig    `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Capabilities          ModelCapabilitiesConfig `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

type ModelReasoningConfig struct {
	Mode   string `json:"mode,omitempty" yaml:"mode,omitempty"`
	Effort string `json:"effort,omitempty" yaml:"effort,omitempty"`
}

type ModelCapabilitiesConfig struct {
	ToolUse          bool `json:"tool_use,omitempty" yaml:"tool_use,omitempty"`
	Thinking         bool `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	ParallelToolCall bool `json:"parallel_tool_call,omitempty" yaml:"parallel_tool_call,omitempty"`
	ImageInput       bool `json:"image_input,omitempty" yaml:"image_input,omitempty"`
	MaxContextTokens int  `json:"max_context_tokens,omitempty" yaml:"max_context_tokens,omitempty"`
	MaxOutputTokens  int  `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
}

type ModelRoleBindingsConfig struct {
	Agent     string `json:"agent" yaml:"agent"`
	Semantic  string `json:"semantic" yaml:"semantic"`
	Finalizer string `json:"finalizer" yaml:"finalizer"`
	Compactor string `json:"compactor" yaml:"compactor"`
	Vision    string `json:"vision" yaml:"vision"`
}

// PolicyConfig controls routing and reply behavior.
type PolicyConfig struct {
	Mode                    domain.Mode              `json:"mode" yaml:"mode"`
	ReplyScope              domain.ReplyScope        `json:"reply_scope" yaml:"reply_scope"`
	PrivateReplyScope       domain.PrivateReplyScope `json:"private_reply_scope" yaml:"private_reply_scope"`
	Sensitivity             domain.Sensitivity       `json:"sensitivity" yaml:"sensitivity"`
	OwnerWait               time.Duration            `json:"owner_wait" yaml:"owner_wait"`
	OwnerReplyConfidenceMin float64                  `json:"owner_reply_confidence_min" yaml:"owner_reply_confidence_min"`
	OwnerReplyRetry         time.Duration            `json:"owner_reply_retry" yaml:"owner_reply_retry"`
	OwnerReplyMaxRetries    int                      `json:"owner_reply_max_retries" yaml:"owner_reply_max_retries"`
	MentionPoll             time.Duration            `json:"mention_poll" yaml:"mention_poll"`
	ReplyConfidenceMin      float64                  `json:"reply_confidence_min" yaml:"reply_confidence_min"`
	InvestigationProgress   string                   `json:"investigation_progress" yaml:"investigation_progress"`
	AllowChats              []string                 `json:"allow_chats,omitempty" yaml:"allow_chats,omitempty"`
	BlockChats              []string                 `json:"block_chats,omitempty" yaml:"block_chats,omitempty"`
	BlockUsers              []string                 `json:"block_users,omitempty" yaml:"block_users,omitempty"`
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

// TaskRulesConfig locates the owner's private Markdown file beside this config.
// The file content is never compiled into business policy.
type TaskRulesConfig struct {
	Enabled  bool   `json:"enabled" yaml:"enabled"`
	Path     string `json:"path" yaml:"path"`
	MaxBytes int    `json:"max_bytes" yaml:"max_bytes"`
}

// ConfigDirectory is the directory containing the loaded YAML file.
func (c Config) ConfigDirectory() string {
	return c.configDir
}

// TaskRulesLoad returns the snapshot loader settings for the current config file.
func (c Config) TaskRulesLoad() taskrules.Config {
	path := strings.TrimSpace(c.TaskRules.Path)
	if path == "" {
		path = taskrules.DefaultFileName
	}
	return taskrules.Config{
		Enabled:   c.TaskRules.Enabled,
		ConfigDir: c.configDir,
		Path:      path,
		MaxBytes:  c.TaskRules.MaxBytes,
		FileName:  filepath.Base(path),
	}
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
		GitHub: GitHubConfig{
			APIBaseURL:           "https://api.github.com",
			TokenKeychainService: "lark-agent",
			TokenKeychainKey:     "github_token",
			MaxFiles:             50,
			MaxPatchBytes:        64 * 1024,
			MaxAnnotations:       50,
			MaxReviews:           50,
		},
		Owner: OwnerConfig{
			PreferredLanguage: agentlocale.LanguageAuto,
			FallbackLanguage:  agentlocale.LanguageChinese,
		},
		Assistant: AssistantConfig{
			Names:       []string{"Lark Agent", "lark-agent", "机器人", "Agent"},
			ReplyScope:  domain.ReplyScopeAllGroups,
			OwnerDirect: OwnerDirectRequestConfig{Enabled: true},
		},
		Model: ModelConfig{
			Provider: "kimi",
			BaseURL:  "https://api.kimi.com/coding/v1",
			Name:     "k3-256k",
			Timeout:  60 * time.Second,
			Profiles: map[string]ModelProfileConfig{
				"primary": {
					Provider:              "kimi",
					Protocol:              "openai_chat",
					BaseURL:               "https://api.kimi.com/coding/v1",
					Name:                  "k3-256k",
					KeychainService:       "lark-agent",
					CredentialKeychainKey: "model/primary/api-key",
					Timeout:               60 * time.Second,
					Stream:                "auto",
					Reasoning:             ModelReasoningConfig{Mode: "provider_default"},
					Capabilities: ModelCapabilitiesConfig{
						ToolUse:          true,
						Thinking:         true,
						ParallelToolCall: true,
					},
				},
			},
			Roles: ModelRoleBindingsConfig{
				Agent:     "primary",
				Semantic:  "primary",
				Finalizer: "primary",
				Compactor: "primary",
				Vision:    "primary",
			},
		},
		Agent: AgentConfig{
			MaxTurns:                  150,
			MaxRetries:                20,
			MaxToolOutput:             32 * 1024,
			MaxTotalToolOutput:        128 * 1024,
			MaxContextBytes:           64 * 1024,
			ContextCompaction:         0.80,
			LoopTimeout:               2 * time.Hour,
			MaxRepeatedCalls:          3,
			ShellTimeout:              2 * time.Minute,
			ShellApproval:             false,
			MaxContextImages:          2,
			MaxContextImageBytes:      1 << 20,
			MaxContextImageTotalBytes: 2 << 20,
		},
		FastPath: FastPathConfig{Enabled: true, SimpleMaxTurns: 3, CodingMaxTurns: 100},
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
				"edit_workspace":        "allow",
				"write_workspace":       "allow",
				"shell":                 "allow",
				"direct_lark_im_send":   "deny",
				"production_file_write": "deny",
			},
		},
		ToolPolicy: ToolPolicyConfig{
			DenyUnboundedShellSearch: true,
			CodingMaxToolCalls:       16,
			MaxNoProgress:            3,
		},
		Goal:  GoalConfig{Enabled: true, MaxActive: 3, MaxInvestigationTurns: 150},
		State: StateConfig{AllowReset: false},
		Policy: PolicyConfig{
			Mode:                    domain.ModeAuto,
			ReplyScope:              domain.ReplyScopeAllGroups,
			PrivateReplyScope:       domain.PrivateReplyScopeAll,
			Sensitivity:             domain.SensitivityNormal,
			OwnerWait:               3 * time.Minute,
			OwnerReplyConfidenceMin: 0.85,
			OwnerReplyRetry:         30 * time.Second,
			OwnerReplyMaxRetries:    3,
			MentionPoll:             30 * time.Second,
			ReplyConfidenceMin:      0.70,
			InvestigationProgress:   "enabled",
		},
		Workspace: WorkspaceConfig{
			Excludes: []string{".git", ".env*", "node_modules", "vendor", "dist", "build", "*.pem", "*.key"},
		},
		Retention: RetentionConfig{Days: 30},
		TaskRules: TaskRulesConfig{
			Enabled:  false,
			Path:     taskrules.DefaultFileName,
			MaxBytes: taskrules.DefaultMaxBytes,
		},
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
	cfg.Normalize()
	cfg.configDir = filepath.Dir(path)
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

// Normalize upgrades legacy in-memory shapes after YAML parsing. It never reads
// secrets; legacy OPENAI_BASE_URL and OPENAI_MODEL may seed the primary profile.
func (c *Config) Normalize() {
	if c == nil {
		return
	}
	legacyVersion := c.Version > 0 && c.Version < currentVersion
	if c.Version == 0 || c.Version < currentVersion {
		c.Version = currentVersion
	}
	c.Model.normalize(legacyVersion)
	if strings.TrimSpace(c.TaskRules.Path) == "" {
		c.TaskRules.Path = taskrules.DefaultFileName
	}
	if c.TaskRules.MaxBytes <= 0 {
		c.TaskRules.MaxBytes = taskrules.DefaultMaxBytes
	}
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
	if c.Lark.BaseURL != "" {
		parsed, err := url.Parse(strings.TrimSpace(c.Lark.BaseURL))
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "lark.base_url must be an absolute HTTP URL").
				WithField("lark.base_url")
		}
	}
	if err := validateGitHubConfig(c.GitHub); err != nil {
		return err
	}
	if err := validateModelConfig(c.Model); err != nil {
		return err
	}
	if c.Owner.OpenID == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "owner.open_id is required").WithField("owner.open_id")
	}
	if strings.TrimSpace(c.Owner.Name) == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "owner.name is required").WithField("owner.name")
	}
	if _, err := agentlocale.ParsePreferred(string(c.Owner.PreferredLanguage)); err != nil {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"invalid owner.preferred_language: %s",
			c.Owner.PreferredLanguage,
		).WithField("owner.preferred_language").WithCause(err)
	}
	if _, err := agentlocale.ParseConcrete(string(c.Owner.FallbackLanguage)); err != nil {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"invalid owner.fallback_language: %s",
			c.Owner.FallbackLanguage,
		).WithField("owner.fallback_language").WithCause(err)
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
	if _, err := domain.ParsePrivateReplyScope(string(c.Policy.PrivateReplyScope)); err != nil {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "invalid policy.private_reply_scope: %s", c.Policy.PrivateReplyScope).
			WithField("policy.private_reply_scope").
			WithCause(err)
	}
	if _, err := domain.ParseReplyScope(string(c.Assistant.ReplyScope)); err != nil {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "invalid assistant.reply_scope: %s", c.Assistant.ReplyScope).
			WithField("assistant.reply_scope").
			WithCause(err)
	}
	if c.Policy.OwnerWait <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "policy.owner_wait must be positive").WithField("policy.owner_wait")
	}
	if c.Policy.OwnerReplyConfidenceMin <= 0 || c.Policy.OwnerReplyConfidenceMin > 1 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"policy.owner_reply_confidence_min must be greater than 0 and at most 1",
		).WithField("policy.owner_reply_confidence_min")
	}
	if c.Policy.OwnerReplyRetry <= 0 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"policy.owner_reply_retry must be positive",
		).WithField("policy.owner_reply_retry")
	}
	if c.Policy.OwnerReplyMaxRetries <= 0 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"policy.owner_reply_max_retries must be positive",
		).WithField("policy.owner_reply_max_retries")
	}
	if c.Policy.MentionPoll <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "policy.mention_poll must be positive").WithField("policy.mention_poll")
	}
	if c.Policy.ReplyConfidenceMin < 0 || c.Policy.ReplyConfidenceMin > 1 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "policy.reply_confidence_min must be between 0 and 1").WithField("policy.reply_confidence_min")
	}
	if c.Policy.InvestigationProgress != "enabled" &&
		c.Policy.InvestigationProgress != "disabled" {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"policy.investigation_progress must be enabled or disabled",
		).WithField("policy.investigation_progress")
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
	if c.Agent.MaxContextImages < 0 || c.Agent.MaxContextImages > 2 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"agent.max_context_images must be between 0 and 2",
		).WithField("agent.max_context_images")
	}
	if c.Agent.MaxContextImageBytes <= 0 || c.Agent.MaxContextImageBytes > 1<<20 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"agent.max_context_image_bytes must be between 1 and 1048576",
		).WithField("agent.max_context_image_bytes")
	}
	if c.Agent.MaxContextImageTotalBytes < c.Agent.MaxContextImageBytes ||
		c.Agent.MaxContextImageTotalBytes > 2<<20 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"agent.max_context_image_total_bytes must cover one image and be at most 2097152",
		).WithField("agent.max_context_image_total_bytes")
	}
	if c.Agent.ContextCompaction < 0.5 || c.Agent.ContextCompaction > 0.95 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"agent.context_compaction_ratio must be between 0.5 and 0.95",
		).WithField("agent.context_compaction_ratio")
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
	if err := validateTaskRulesConfig(c.TaskRules); err != nil {
		return err
	}
	return nil
}

func validateTaskRulesConfig(cfg TaskRulesConfig) error {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = taskrules.DefaultFileName
	}
	if filepath.IsAbs(path) || strings.Contains(path, "..") {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"task_rules.path must be a relative path inside the config directory",
		).WithField("task_rules.path")
	}
	if cfg.MaxBytes < taskrules.MinMaxBytes || cfg.MaxBytes > taskrules.MaxMaxBytes {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"task_rules.max_bytes must be between %d and %d",
			taskrules.MinMaxBytes,
			taskrules.MaxMaxBytes,
		).WithField("task_rules.max_bytes")
	}
	return nil
}

func (m *ModelConfig) normalize(forceLegacyPrimary bool) {
	if m == nil {
		return
	}
	if forceLegacyPrimary && (strings.TrimSpace(m.Name) != "" || strings.TrimSpace(m.BaseURL) != "") {
		m.Profiles = nil
	}
	if len(m.Profiles) == 0 {
		baseURL := firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), strings.TrimSpace(m.BaseURL))
		modelName := firstNonEmpty(os.Getenv("OPENAI_MODEL"), strings.TrimSpace(m.Name))
		provider := normalizeModelProvider(m.Provider, baseURL, modelName)
		if baseURL == "" && provider == "kimi" {
			baseURL = "https://api.kimi.com/coding/v1"
		}
		if modelName == "" && provider == "kimi" {
			modelName = "k3-256k"
		}
		profile := ModelProfileConfig{
			Provider:              provider,
			Protocol:              "openai_chat",
			BaseURL:               baseURL,
			Name:                  modelName,
			KeychainService:       "lark-agent",
			CredentialKeychainKey: "model/primary/api-key",
			Timeout:               m.Timeout,
			Stream:                "auto",
			Reasoning:             ModelReasoningConfig{Mode: "provider_default"},
			Capabilities: ModelCapabilitiesConfig{
				ToolUse:          true,
				Thinking:         true,
				ParallelToolCall: true,
			},
		}
		if profile.Timeout <= 0 {
			profile.Timeout = 60 * time.Second
		}
		m.Profiles = map[string]ModelProfileConfig{"primary": profile}
	}
	if m.Roles.Agent == "" {
		m.Roles.Agent = "primary"
	}
	if m.Roles.Semantic == "" {
		m.Roles.Semantic = m.Roles.Agent
	}
	if m.Roles.Finalizer == "" {
		m.Roles.Finalizer = m.Roles.Agent
	}
	if m.Roles.Compactor == "" {
		m.Roles.Compactor = m.Roles.Agent
	}
	if m.Roles.Vision == "" {
		m.Roles.Vision = m.Roles.Agent
	}
	if primary, ok := m.Profiles[m.Roles.Agent]; ok {
		m.Provider = primary.Provider
		m.BaseURL = primary.BaseURL
		m.Name = primary.Name
		m.Timeout = primary.Timeout
	}
}

func validateModelConfig(cfg ModelConfig) error {
	if len(cfg.Profiles) == 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "model.profiles is required").
			WithField("model.profiles")
	}
	for name, profile := range cfg.Profiles {
		if strings.TrimSpace(name) == "" {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile name is required").
				WithField("model.profiles")
		}
		field := "model.profiles." + name
		if err := validateModelProfile(field, profile); err != nil {
			return err
		}
	}
	for role, profileName := range map[string]string{
		"agent":     cfg.Roles.Agent,
		"semantic":  cfg.Roles.Semantic,
		"finalizer": cfg.Roles.Finalizer,
		"compactor": cfg.Roles.Compactor,
		"vision":    cfg.Roles.Vision,
	} {
		if strings.TrimSpace(profileName) == "" {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "model role %s is not bound", role).
				WithField("model.roles." + role)
		}
		if _, ok := cfg.Profiles[profileName]; !ok {
			return errs.NewConfigError(
				errs.SubtypeInvalidConfig,
				"model role %s references missing profile %q",
				role,
				profileName,
			).WithField("model.roles." + role)
		}
	}
	return nil
}

func validateModelProfile(field string, profile ModelProfileConfig) error {
	provider := strings.TrimSpace(profile.Provider)
	switch provider {
	case "kimi", "openai", "anthropic":
	default:
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "unsupported model provider %q", provider).
			WithField(field + ".provider")
	}
	switch strings.TrimSpace(profile.Protocol) {
	case "openai_chat":
		if provider == "anthropic" {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "anthropic provider must use anthropic_messages protocol").
				WithField(field + ".protocol")
		}
	case "openai_responses":
		if provider != "openai" {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "openai_responses protocol requires openai provider").
				WithField(field + ".protocol")
		}
	case "anthropic_messages":
		if provider != "anthropic" {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "anthropic_messages protocol requires anthropic provider").
				WithField(field + ".protocol")
		}
	default:
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "unsupported model protocol %q", profile.Protocol).
			WithField(field + ".protocol")
	}
	if strings.TrimSpace(profile.Name) == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile name is required").
			WithField(field + ".name")
	}
	parsed, err := url.Parse(strings.TrimSpace(profile.BaseURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile base_url must be an absolute HTTP URL").
			WithField(field + ".base_url")
	}
	if strings.TrimSpace(profile.CredentialKeychainKey) == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile credential keychain key is required").
			WithField(field + ".credential_keychain_key")
	}
	if profile.KeychainService == "" {
		profile.KeychainService = "lark-agent"
	}
	if profile.Timeout <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile timeout must be positive").
			WithField(field + ".timeout")
	}
	if profile.Stream != "" && profile.Stream != "auto" && profile.Stream != "disabled" && profile.Stream != "required" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile stream must be auto, disabled, or required").
			WithField(field + ".stream")
	}
	if profile.Reasoning.Mode != "" &&
		profile.Reasoning.Mode != "provider_default" &&
		profile.Reasoning.Mode != "enabled" &&
		profile.Reasoning.Mode != "disabled" {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"model profile reasoning mode must be provider_default, enabled, or disabled",
		).WithField(field + ".reasoning.mode")
	}
	return nil
}

func normalizeModelProvider(provider, baseURL, modelName string) string {
	provider = strings.TrimSpace(provider)
	switch provider {
	case "kimi", "openai", "anthropic":
		return provider
	}
	lowerURL := strings.ToLower(strings.TrimSpace(baseURL))
	lowerModel := strings.ToLower(strings.TrimSpace(modelName))
	if strings.Contains(lowerURL, "kimi.com") ||
		strings.HasPrefix(lowerModel, "k3") ||
		strings.Contains(lowerModel, "kimi") {
		return "kimi"
	}
	if strings.Contains(lowerURL, "anthropic.com") ||
		strings.Contains(lowerModel, "claude") {
		return "anthropic"
	}
	return "openai"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func validateGitHubConfig(cfg GitHubConfig) error {
	parsed, err := url.Parse(strings.TrimSpace(cfg.APIBaseURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "github.api_base_url must be an absolute HTTP URL").
			WithField("github.api_base_url")
	}
	if cfg.TokenKeychainService == "" || cfg.TokenKeychainKey == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "github keychain references are required").
			WithField("github.token_keychain")
	}
	if cfg.MaxFiles <= 0 || cfg.MaxPatchBytes <= 0 || cfg.MaxAnnotations <= 0 || cfg.MaxReviews <= 0 {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "github result limits must be positive").
			WithField("github")
	}
	if cfg.Enabled && len(cfg.AllowedRepositories) == 0 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"github.allowed_repositories is required when github is enabled",
		).WithField("github.allowed_repositories")
	}
	if cfg.ProactiveReview.Enabled && !cfg.Enabled {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"github.proactive_review requires github.enabled",
		).WithField("github.proactive_review.enabled")
	}
	if cfg.ProactiveReview.Enabled && len(cfg.ProactiveReview.ChatIDs) == 0 {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"github.proactive_review.chat_ids is required when proactive review is enabled",
		).WithField("github.proactive_review.chat_ids")
	}
	seen := map[string]bool{}
	for _, repository := range cfg.AllowedRepositories {
		parts := strings.Split(repository, "/")
		if len(parts) != 2 || !validRepositoryPart(parts[0]) || !validRepositoryPart(parts[1]) {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "invalid github repository %q", repository).
				WithField("github.allowed_repositories")
		}
		canonical := strings.ToLower(repository)
		if seen[canonical] {
			return errs.NewConfigError(errs.SubtypeInvalidConfig, "duplicate github repository %q", repository).
				WithField("github.allowed_repositories")
		}
		seen[canonical] = true
	}
	seenChats := map[string]bool{}
	for _, chatID := range cfg.ProactiveReview.ChatIDs {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" || seenChats[chatID] {
			return errs.NewConfigError(
				errs.SubtypeInvalidConfig,
				"github.proactive_review.chat_ids must contain unique non-empty chat ids",
			).WithField("github.proactive_review.chat_ids")
		}
		seenChats[chatID] = true
	}
	return nil
}

func validRepositoryPart(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

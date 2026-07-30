// Package cmd implements the lark-agent command tree.
package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/liuchong/lark-agent/agent/app"
	"github.com/liuchong/lark-agent/agent/config"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/control"
	"github.com/liuchong/lark-agent/agent/daemon"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/feedback"
	"github.com/liuchong/lark-agent/agent/investigation"
	"github.com/liuchong/lark-agent/agent/lifecycle"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/agent/memory"
	"github.com/liuchong/lark-agent/agent/policy"
	"github.com/liuchong/lark-agent/agent/poll"
	"github.com/liuchong/lark-agent/agent/realtime"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/rules"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	"github.com/liuchong/lark-agent/agent/storage"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
	"github.com/liuchong/lark-agent/internal/secretstore"
)

// Execute runs the command tree.
func Execute(in io.Reader, out, errOut io.Writer, args []string) int {
	root := NewRootCommand(in, out)
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	ctx, stop := commandSignalContext(context.Background())
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		writeError(errOut, err)
		return exitCodeOf(err)
	}
	return 0
}

// NewRootCommand builds the lark-agent command tree.
func NewRootCommand(in io.Reader, out io.Writer) *cobra.Command {
	var configPath string
	var statePath string
	cmd := &cobra.Command{
		Use:   "lark-agent",
		Short: "Autonomous personal Lark AI assistant",
		Long: `lark-agent runs a personal Lark AI assistant.

It monitors Lark messages, applies workspace-bounded rules, routes relevant
work to an Eino-based agent loop, and can reply as the owner in auto mode.
Only the configured owner can mention the assistant bot in an allowed group to
ask a question or request an operation; that path replies with bot identity.
The configured owner can also privately message the assistant. Non-owner bot
private messages and direct assistant mentions stay silent. Human messages in
the owner's private chats and group messages that mention the human owner may
enter delegated reply after a three-minute semantic owner-answer window.
Assistant mentions and owner mentions have independent all-groups or
configured-groups scopes. Private delegated messages have an independent
all-private or disabled scope and still honor user and chat block lists.
Direct assistant requests add a keyboard working reaction before work starts
and remove it when work finishes.
Programming questions use a bounded coding investigation path with planning,
code search fallback, source-backed verify, and replay transcript export.
Simple questions use at most 3 model turns; coding questions use 20 turns,
16 tool calls, a 3-step no-progress stop, and at most 3 terminal-only attempts
after forced convergence by default. One interactive
worker is reserved from the foreground pool, while CodingGoal work uses
background workers. Time, date, ping, status, doctor, queue summary, and help
use a deterministic fast path before any model loop.
The configured owner can use /help, /status, /tasks, /task, /approvals, and
/approval in the assistant's private chat to inspect and safely close durable
work without invoking the model. Group commands disclose no queue details and
redirect the owner to private chat; non-owner commands remain silent. Local
operators can use queue tasks, queue acknowledge, and queue reconcile for the
same bounded task views and durable closure rules.
After restart, safe read-only and model work is automatically re-evaluated in
the new ready session. Exact approval waits are preserved and privately
reported. An interrupted external action is never replayed: it is terminalized
with an exact reconciliation instruction. Use queue inspect for durable
evidence and queue resume only for an explicitly reviewed terminal or manually
paused item. 安全工作会自动续跑，结果不确定的外部动作绝不重放。
Intentional shutdown and every successfully ready session are reported through
the assistant bot's private chat.

Modes:
  auto      AI may autonomously decide and reply when gates pass
  approval  AI drafts replies but waits for owner approval
  paused    new model calls and unsent side effects are stopped`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "agent config path (default: ~/.config/lark-agent/config.yaml)")
	cmd.PersistentFlags().StringVar(&statePath, "state", "", "agent state database path (default: ~/Library/Application Support/lark-agent/state.db)")
	cmd.AddCommand(
		newInitCommand(out, &configPath),
		newAuthCommand(in, out, &configPath),
		newConfigCommand(out, &configPath),
		newWorkspaceCommand(out, &configPath),
		newModelCommand(in, out, &configPath),
		newDaemonCommand(out, &configPath, &statePath),
		newModeCommand(out, &configPath),
		newQueueCommand(out, &configPath, &statePath),
		newSubscriptionCommand(out, &statePath),
		newApprovalCommand(out, &statePath),
		newMemoryCommand(out),
		newRulesCommand(out, &configPath),
		newGitHubCommand(in, out, &configPath),
		newDoctorCommand(out, &configPath, &statePath),
	)
	return cmd
}

func newAuthCommand(in io.Reader, out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage Lark SDK credentials in Keychain"}
	cmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Read Lark credentials as JSON from stdin and store them in Keychain",
		Long: "Read JSON from stdin with app_secret, optional user_access_token, and optional " +
			"refresh_token, then store those values in macOS Keychain accounts named by config. " +
			"If app_secret is already stored, stdin may omit it when only adding or replacing " +
			"user tokens. Secrets are never accepted as command arguments or written to stdout.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if in == nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "stdin is required").WithParam("stdin")
			}
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			var input authLoginInput
			if err := json.NewDecoder(in).Decode(&input); err != nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "decode auth login JSON from stdin").WithCause(err)
			}
			existing, _ := serviceim.LoadCredentials(cmd.Context(), credentialRefs(cfg))
			credentials, err := mergeAuthLoginInput(existing, input)
			if err != nil {
				return err
			}
			if err := serviceim.StoreCredentials(cmd.Context(), credentialRefs(cfg), credentials); err != nil {
				return err
			}
			return writeData(out, map[string]any{
				"stored":           true,
				"keychain_service": cfg.Lark.KeychainService,
				"accounts": map[string]string{
					"app_secret":         cfg.Lark.AppSecretKeychainKey,
					"user_access_token":  cfg.Lark.UserTokenKeychainKey,
					"user_refresh_token": cfg.Lark.RefreshTokenKeychainKey,
				},
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Check whether Lark credentials are readable without printing them",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			credentials, err := serviceim.LoadCredentials(cmd.Context(), credentialRefs(cfg))
			return writeData(out, map[string]any{
				"keychain_service": cfg.Lark.KeychainService,
				"app_secret":       err == nil && credentials.AppSecret != "",
				"user_token":       credentials.UserAccessToken != "",
				"refresh_token":    credentials.RefreshToken != "",
				"configured":       err == nil && credentials.AppSecret != "",
				"error":            errorString(err),
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Delete configured Lark credentials from Keychain",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			if err := serviceim.DeleteCredentials(cmd.Context(), credentialRefs(cfg)); err != nil {
				return err
			}
			return writeData(out, map[string]any{"deleted": true, "keychain_service": cfg.Lark.KeychainService})
		},
	})
	return cmd
}

type authLoginInput struct {
	AppSecret       string `json:"app_secret"`
	UserAccessToken string `json:"user_access_token"`
	RefreshToken    string `json:"refresh_token"`
}

func newGitHubCommand(in io.Reader, out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Bridge trusted GitHub workflow facts into Lark",
		Long: "Send HTTP-only GitHub workflow notifications and manage the local read-only " +
			"GitHub token. The token is read as JSON from stdin and stored in Keychain.",
	}
	cmd.AddCommand(newGitHubAuthCommand(in, out, configPath))

	var chatID, eventPath, eventName string
	var dryRun bool
	notify := &cobra.Command{
		Use:   "notify",
		Short: "Send one trusted GitHub event notification to an exact Lark chat",
		Long: "Read a typed event from GITHUB_EVENT_PATH and GITHUB_EVENT_NAME, optionally " +
			"enrich it through the GitHub API, and send one HTTP-only Lark bot post. " +
			"This command never starts a Lark WebSocket consumer.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadGitHubNotifyConfig(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			if !cfg.GitHub.Enabled {
				return errs.NewConfigError(
					errs.SubtypeFailedPrecondition,
					"github bridge is disabled",
				).WithField("github.enabled")
			}
			if strings.TrimSpace(chatID) == "" {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "--chat-id is required").
					WithParam("--chat-id")
			}
			path := firstNonEmpty(eventPath, os.Getenv("GITHUB_EVENT_PATH"))
			name := firstNonEmpty(eventName, os.Getenv("GITHUB_EVENT_NAME"))
			if path == "" || name == "" {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"GITHUB_EVENT_PATH and GITHUB_EVENT_NAME are required",
				)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return errs.NewInternalError(errs.SubtypeFileIO, "read GitHub event path").WithCause(err)
			}
			snapshot, err := internalgithub.ParseEvent(name, data)
			if err != nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "parse GitHub event").WithCause(err)
			}
			if !repositoryAllowed(snapshot.Reference.Repository, cfg.GitHub.AllowedRepositories) {
				return errs.NewPermissionError(
					errs.SubtypeFailedPrecondition,
					"github repository is not allowed: %s",
					snapshot.Reference.Repository,
				)
			}

			token, tokenErr := secretstore.Read(
				cmd.Context(),
				cfg.GitHub.TokenKeychainService,
				cfg.GitHub.TokenKeychainKey,
				"GITHUB_TOKEN",
			)
			if tokenErr == nil {
				client, err := newGitHubClient(cfg, token)
				if err != nil {
					return err
				}
				result, err := client.FetchContext(
					cmd.Context(),
					snapshot.Reference,
					[]internalgithub.Section{
						internalgithub.SectionSummary,
						internalgithub.SectionChecks,
						internalgithub.SectionFiles,
						internalgithub.SectionReviews,
					},
				)
				if err != nil {
					snapshot.Partial = true
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "GitHub enrichment unavailable: %s\n", err)
				} else {
					snapshot.Name = firstNonEmpty(result.Name, snapshot.Name)
					snapshot.Status = firstNonEmpty(result.Status, snapshot.Status)
					snapshot.Conclusion = firstNonEmpty(result.Conclusion, snapshot.Conclusion)
					snapshot.FailedJobs = failedGitHubJobs(result.Jobs)
					snapshot.Files = result.Files
					snapshot.Reviews = result.Reviews
					snapshot.Annotations = result.Annotations
					snapshot.Omitted = result.Omitted
					snapshot.Partial = result.Partial || result.Truncated.Any()
				}
			} else {
				snapshot.Partial = true
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "GitHub enrichment unavailable: read-only token is not configured")
			}
			credentials, err := serviceim.LoadCredentials(cmd.Context(), credentialRefs(cfg))
			if err != nil {
				return err
			}
			notification, err := internalgithub.RenderNotification(snapshot, credentials.AppSecret)
			if err != nil {
				return errs.NewInternalError(errs.SubtypeInvalidResponse, "render GitHub notification").WithCause(err)
			}
			idempotencyKey := internalgithub.StableNotificationKey(chatID, snapshot.Reference)
			result := map[string]any{
				"chat_id":         chatID,
				"message_type":    notification.MessageType,
				"idempotency_key": idempotencyKey,
				"reference":       snapshot.Reference,
				"partial":         snapshot.Partial,
				"dry_run":         dryRun,
			}
			if dryRun {
				result["content"] = notification.Content
				return writeData(out, result)
			}
			larkClient, err := serviceim.NewClient(serviceim.ClientConfig{
				AppID:     cfg.Lark.AppID,
				AppSecret: credentials.AppSecret,
				BaseURL:   cfg.Lark.BaseURL,
				Timeout:   30 * time.Second,
			})
			if err != nil {
				return err
			}
			sent, err := serviceim.NewService(larkClient, cfg.Owner.OpenID).SendMessageAsBot(
				cmd.Context(),
				serviceim.SendMessageRequest{
					ChatID:         chatID,
					MessageType:    notification.MessageType,
					Content:        notification.Content,
					IdempotencyKey: idempotencyKey,
				},
			)
			if err != nil {
				return err
			}
			result["message_id"] = sent.MessageID
			result["chat_id"] = sent.ChatID
			return writeData(out, result)
		},
	}
	notify.Flags().StringVar(&chatID, "chat-id", "", "exact destination Lark chat ID")
	notify.Flags().StringVar(&eventPath, "event-path", "", "typed GitHub event JSON path (default: GITHUB_EVENT_PATH)")
	notify.Flags().StringVar(&eventName, "event-name", "", "GitHub event name (default: GITHUB_EVENT_NAME)")
	notify.Flags().BoolVar(&dryRun, "dry-run", false, "render structured output without sending to Lark")
	cmd.AddCommand(notify)
	return cmd
}

func newGitHubAuthCommand(in io.Reader, out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the read-only GitHub token",
		Long:  "Use login to read a token as JSON from stdin and store it in Keychain; use status without printing the token.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Read {\"token\":\"...\"} from stdin and store it in Keychain",
		RunE: func(cmd *cobra.Command, args []string) error {
			if in == nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "stdin is required").WithParam("stdin")
			}
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			var input struct {
				Token string `json:"token"`
			}
			decoder := json.NewDecoder(in)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "decode GitHub token JSON from stdin").WithCause(err)
			}
			if err := secretstore.Write(
				cmd.Context(),
				cfg.GitHub.TokenKeychainService,
				cfg.GitHub.TokenKeychainKey,
				strings.TrimSpace(input.Token),
			); err != nil {
				return err
			}
			return writeData(out, map[string]any{
				"stored":           true,
				"keychain_service": cfg.GitHub.TokenKeychainService,
				"account":          cfg.GitHub.TokenKeychainKey,
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Check whether the GitHub token is readable without printing it",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			token, readErr := secretstore.Read(
				cmd.Context(),
				cfg.GitHub.TokenKeychainService,
				cfg.GitHub.TokenKeychainKey,
				"GITHUB_TOKEN",
			)
			return writeData(out, map[string]any{
				"configured":       readErr == nil && token != "",
				"keychain_service": cfg.GitHub.TokenKeychainService,
				"account":          cfg.GitHub.TokenKeychainKey,
				"error":            errorString(readErr),
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Delete the configured GitHub token from Keychain",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			if err := secretstore.Delete(
				cmd.Context(),
				cfg.GitHub.TokenKeychainService,
				cfg.GitHub.TokenKeychainKey,
			); err != nil {
				return err
			}
			return writeData(out, map[string]any{"deleted": true})
		},
	})
	return cmd
}

func newGitHubClient(cfg config.Config, token string) (*internalgithub.Client, error) {
	return internalgithub.NewClient(internalgithub.ClientConfig{
		BaseURL: cfg.GitHub.APIBaseURL,
		Token:   token,
		Limits: internalgithub.Limits{
			MaxFiles:       cfg.GitHub.MaxFiles,
			MaxPatchBytes:  cfg.GitHub.MaxPatchBytes,
			MaxAnnotations: cfg.GitHub.MaxAnnotations,
			MaxReviews:     cfg.GitHub.MaxReviews,
		},
	})
}

func loadGitHubNotifyConfig(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return config.Config{}, err
	}
	workspaceRoot := firstNonEmpty(os.Getenv("GITHUB_WORKSPACE"), "/github/workspace")
	if !filepath.IsAbs(workspaceRoot) {
		return config.Config{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"GITHUB_WORKSPACE must be absolute",
		)
	}
	cfg = config.Default()
	cfg.Lark.AppID = strings.TrimSpace(os.Getenv("LARK_AGENT_APP_ID"))
	cfg.Lark.BaseURL = strings.TrimSpace(os.Getenv("LARK_AGENT_LARK_BASE_URL"))
	if cfg.Lark.BaseURL == "" {
		return config.Config{}, errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"LARK_AGENT_LARK_BASE_URL is required in GitHub Actions",
		).WithField("lark.base_url")
	}
	cfg.Owner.OpenID = "github-action-sender"
	cfg.Owner.Name = "GitHub Action"
	cfg.Workspace.Root = workspaceRoot
	cfg.GitHub.Enabled = true
	cfg.GitHub.APIBaseURL = firstNonEmpty(os.Getenv("GITHUB_API_URL"), cfg.GitHub.APIBaseURL)
	cfg.GitHub.AllowedRepositories = []string{strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY"))}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func repositoryAllowed(repository string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(repository), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func failedGitHubJobs(jobs []internalgithub.JobSummary) []internalgithub.JobSummary {
	var failed []internalgithub.JobSummary
	for _, job := range jobs {
		switch job.Conclusion {
		case "failure", "cancelled", "timed_out", "action_required", "startup_failure":
			failed = append(failed, job)
		}
	}
	return failed
}

func mergeAuthLoginInput(existing serviceim.Credentials, input authLoginInput) (serviceim.Credentials, error) {
	appSecret := strings.TrimSpace(input.AppSecret)
	if appSecret == "" {
		appSecret = strings.TrimSpace(existing.AppSecret)
	}
	if appSecret == "" {
		return serviceim.Credentials{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "app_secret is required")
	}
	userToken := strings.TrimSpace(input.UserAccessToken)
	if userToken == "" {
		userToken = strings.TrimSpace(existing.UserAccessToken)
	}
	refreshToken := strings.TrimSpace(input.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(existing.RefreshToken)
	}
	return serviceim.Credentials{
		AppSecret:       appSecret,
		UserAccessToken: userToken,
		RefreshToken:    refreshToken,
	}, nil
}

func newInitCommand(out io.Writer, configPath *string) *cobra.Command {
	var workspaceRoot, appID, owner, ownerName, preferredLanguage, fallbackLanguage string
	cmd := &cobra.Command{
		Use:   "init --workspace <absolute-dir>",
		Short: "Initialize lark-agent config",
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := workspace.NewScope(workspaceRoot)
			if err != nil {
				return err
			}
			cfg := config.Default()
			cfg.Lark.AppID = appID
			cfg.Owner.OpenID = owner
			cfg.Owner.Name = strings.TrimSpace(ownerName)
			cfg.Owner.PreferredLanguage = agentlocale.Language(preferredLanguage)
			cfg.Owner.FallbackLanguage = agentlocale.Language(fallbackLanguage)
			cfg.Workspace.Root = scope.ConfiguredRoot()
			path := resolveConfigPath(*configPath)
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			return writeData(out, map[string]any{
				"config":    path,
				"workspace": scope.Snapshot(),
				"mode":      cfg.Policy.Mode,
			})
		},
	}
	cmd.Flags().StringVar(&workspaceRoot, "workspace", "", "absolute workspace root; required")
	cmd.Flags().StringVar(&appID, "app-id", "", "Lark app_id used by the public Go SDK")
	cmd.Flags().StringVar(&owner, "owner-open-id", "", "owner open_id")
	cmd.Flags().StringVar(&ownerName, "owner-name", "", "owner display name used in delegated assistant replies")
	cmd.Flags().StringVar(
		&preferredLanguage,
		"preferred-language",
		string(agentlocale.LanguageAuto),
		"outward language: auto, zh-CN, or en-US",
	)
	cmd.Flags().StringVar(
		&fallbackLanguage,
		"fallback-language",
		string(agentlocale.LanguageChinese),
		"fallback outward language: zh-CN or en-US",
	)
	_ = cmd.MarkFlagRequired("owner-name")
	return cmd
}

func newConfigCommand(out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect agent configuration"}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate agent configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"valid": true, "mode": cfg.Policy.Mode})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show agent configuration without secrets",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			return writeData(out, cfg)
		},
	})
	return cmd
}

func newWorkspaceCommand(out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "Manage the workspace boundary"}
	var workspaceRoot, probe string
	validateCmd := &cobra.Command{
		Use:   "validate --workspace <absolute-dir>",
		Short: "Validate a workspace root and optional probe path",
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := workspace.NewScope(workspaceRoot)
			if err != nil {
				return err
			}
			data := map[string]any{"workspace": scope.Snapshot()}
			if probe != "" {
				resolved, err := scope.ResolveReadPath(probe)
				if err != nil {
					return err
				}
				data["probe"] = map[string]any{"relative": probe, "resolved": resolved}
			}
			return writeData(out, data)
		},
	}
	validateCmd.Flags().StringVar(&workspaceRoot, "workspace", "", "absolute workspace root")
	validateCmd.Flags().StringVar(&probe, "probe", "", "workspace-relative path to test")
	cmd.AddCommand(validateCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show configured workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			scope, err := workspace.NewScopeWithExcludes(cfg.Workspace.Root, cfg.Workspace.Excludes)
			if err != nil {
				return err
			}
			return writeData(out, scope.Snapshot())
		},
	})
	return cmd
}

func newModelCommand(in io.Reader, out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "model", Short: "Manage model credentials"}
	cmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Read an OpenAI-compatible API key from stdin",
		RunE: func(cmd *cobra.Command, args []string) error {
			if in == nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "stdin is required").WithParam("stdin")
			}
			// The first version validates the command surface without storing
			// secrets in files. Keychain persistence is wired in a later step.
			return writeData(out, map[string]any{"stored": false, "hint": "keychain storage not configured in this build"})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Verify native tools and tool-result protocol against the configured provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			apiKey := os.Getenv("OPENAI_API_KEY")
			modelName := firstNonEmpty(os.Getenv("OPENAI_MODEL"), cfg.Model.Name)
			baseURL := firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), cfg.Model.BaseURL)
			if apiKey == "" {
				return errs.NewConfigError(errs.SubtypeNotConfigured, "OPENAI_API_KEY is required for model doctor").
					WithHint("set the daemon's OpenAI-compatible API key in its private environment")
			}
			if modelName == "" {
				return errs.NewConfigError(errs.SubtypeNotConfigured, "model.name or OPENAI_MODEL is required for model doctor")
			}
			result, err := agentruntime.DoctorNativeTools(cmd.Context(), &agentruntime.OpenAICompatibleModel{
				APIKey:  apiKey,
				BaseURL: baseURL,
				Model:   modelName,
				Timeout: cfg.Model.Timeout,
			})
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{
				"provider": cfg.Model.Provider,
				"model":    modelName,
				"tools":    result,
			})
		},
	})
	return cmd
}

func newDaemonCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run or manage the daemon",
		Long: "Run or manage the standalone daemon. Safe interrupted work is " +
			"automatically re-evaluated after a ready restart; result-uncertain " +
			"external actions are terminalized and never replayed. " +
			"安全工作自动续跑，结果不确定的外部动作绝不重放。",
	}
	var once, live, dryRun, includePrivate bool
	var chatQuery string
	var pollInterval time.Duration
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run daemon in the foreground",
		Long: "Run the daemon in the foreground. Non-owner requests are read-only and confined to " +
			"same-chat plus configured-workspace evidence; environment reconnaissance and paths outside " +
			"the workspace are refused. Allowed group owner mentions and all inbound human private messages " +
			"use a durable three-minute semantic owner-answer window before reply work.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			if pollInterval <= 0 {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "--poll-interval must be positive").WithParam("--poll-interval")
			}
			store, err := storage.Open(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // foreground daemon shutdown
			store.ConfigureScheduler(cfg.Scheduler.DuplicateWindow, cfg.Goal.MaxActive)
			agentRouter := newAgentRouter(cfg, store)
			options, realtimeSource, liveMessenger, liveInfo, err := buildLiveOptions(
				cmd.Context(),
				cfg,
				store,
				agentRouter,
				live,
				dryRun,
				chatQuery,
				includePrivate,
			)
			if err != nil {
				return err
			}
			options = append(options, app.WithWorkLeases(map[domain.WorkKind]time.Duration{
				domain.WorkKindFastPath:       cfg.Scheduler.FastPathLease,
				domain.WorkKindOwnerControl:   cfg.Scheduler.FastPathLease,
				domain.WorkKindSimpleQuestion: cfg.Scheduler.SimpleLease,
				domain.WorkKindDirectMention:  cfg.Scheduler.SimpleLease,
				domain.WorkKindCodingQuestion: cfg.Scheduler.CodingQuestionLease,
				domain.WorkKindCodingGoal:     cfg.Scheduler.CodingGoalLease,
			}), app.WithCodingGoalMaxTurns(cfg.Goal.MaxInvestigationTurns))
			daemonApp := app.NewDaemon(store, agentRouter, options...)
			var lifecycleController *lifecycle.Controller
			if live && !dryRun && liveMessenger != nil && !once {
				lifecycleController = lifecycle.NewDurableController(
					liveMessenger,
					store,
					lifecycle.Options{
						Language:  string(resolveConfiguredLanguage(cfg.Owner)),
						OwnerName: cfg.Owner.Name,
					},
				)
			}
			var pollResult any
			if live {
				result, err := daemonApp.PollOnce(cmd.Context())
				if err != nil {
					return err
				}
				pollResult = result
			}
			readySession, err := store.MarkCurrentSessionReady(cmd.Context())
			if err != nil {
				return err
			}
			recoveryReport, err := store.ConvergeInterruptedWork(cmd.Context())
			if err != nil {
				return err
			}
			if liveMessenger != nil {
				if err := flushOwnerResolutionNotifications(
					cmd.Context(),
					store,
					liveMessenger,
					cfg.Owner,
					cmd.ErrOrStderr(),
				); err != nil {
					return err
				}
				for _, notice := range recoveryReport.Notices {
					if err := notifyRecoveryConvergence(
						cmd.Context(),
						store,
						liveMessenger,
						notice,
						cfg.Owner,
					); err != nil {
						writeError(cmd.ErrOrStderr(), err)
					}
				}
			}
			if lifecycleController != nil {
				_, remainingUncertain, err := store.RecoverySummary(cmd.Context())
				if err != nil {
					return err
				}
				summary := lifecycle.Summary{
					Resumed:      recoveryReport.Resumed,
					WaitingOwner: recoveryReport.WaitingOwner,
					Terminalized: recoveryReport.Terminalized,
					Uncertain:    recoveryReport.Uncertain + remainingUncertain,
				}
				if err := lifecycleController.NotifyOnline(
					cmd.Context(),
					readySession.ID,
					summary,
				); err != nil {
					return err
				}
				defer notifyLifecycleOffline(
					store,
					lifecycleController,
					readySession.ID,
					cmd.ErrOrStderr(),
				)
			}
			var workerCtx context.Context
			var workerGroup sync.WaitGroup
			if !once {
				var cancelWorkers context.CancelFunc
				workerCtx, cancelWorkers = context.WithCancel(cmd.Context())
				defer func() {
					cancelWorkers()
					workerGroup.Wait()
				}()
				if realtimeSource != nil {
					workerGroup.Add(1)
					go func() {
						defer workerGroup.Done()
						realtime.Supervise(workerCtx, realtimeSource, time.Second, 30*time.Second, func(err error) {
							writeError(cmd.ErrOrStderr(), err)
						})
					}()
				}
			}
			if once {
				first, err := daemonApp.RunOnce(cmd.Context())
				if err != nil {
					return err
				}
				return writeData(out, map[string]any{
					"ready":     true,
					"mode":      cfg.Policy.Mode,
					"workspace": cfg.Workspace.Root,
					"live":      liveInfo,
					"poll":      pollResult,
					"run":       first,
				})
			}
			interactiveOptions := append([]app.Option{}, options...)
			interactiveOptions = append(interactiveOptions,
				app.WithWorker("interactive-1"),
				app.WithSchedulerLane(domain.SchedulerLaneInteractive),
			)
			startSchedulerWorker(
				&workerGroup,
				workerCtx,
				app.NewDaemon(store, agentRouter, interactiveOptions...),
				cmd.ErrOrStderr(),
			)
			for i := 0; i < cfg.Scheduler.ForegroundWorkers-1; i++ {
				workerOptions := append([]app.Option{}, options...)
				workerOptions = append(workerOptions,
					app.WithWorker(fmt.Sprintf("foreground-%d", i+1)),
					app.WithSchedulerLane(domain.SchedulerLaneForeground),
				)
				startSchedulerWorker(
					&workerGroup,
					workerCtx,
					app.NewDaemon(store, agentRouter, workerOptions...),
					cmd.ErrOrStderr(),
				)
			}
			for i := 0; i < cfg.Scheduler.BackgroundWorkers; i++ {
				workerOptions := append([]app.Option{}, options...)
				workerOptions = append(workerOptions,
					app.WithWorker(fmt.Sprintf("background-%d", i+1)),
					app.WithSchedulerLane(domain.SchedulerLaneBackground),
				)
				startSchedulerWorker(
					&workerGroup,
					workerCtx,
					app.NewDaemon(store, agentRouter, workerOptions...),
					cmd.ErrOrStderr(),
				)
			}
			if err := writeData(out, map[string]any{
				"ready":      true,
				"mode":       cfg.Policy.Mode,
				"workspace":  cfg.Workspace.Root,
				"live":       liveInfo,
				"first_poll": pollResult,
				"first_run":  map[string]any{"processed": false, "reason": "workers_started_after_recovery"},
			}); err != nil {
				return err
			}
			pollTicker := time.NewTicker(pollInterval)
			defer pollTicker.Stop()
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-pollTicker.C:
					if _, err := daemonApp.PollOnce(cmd.Context()); err != nil {
						writeError(cmd.ErrOrStderr(), err)
					}
					if liveMessenger != nil {
						if err := flushOwnerResolutionNotifications(
							cmd.Context(),
							store,
							liveMessenger,
							cfg.Owner,
							cmd.ErrOrStderr(),
						); err != nil {
							writeError(cmd.ErrOrStderr(), err)
						}
					}
				}
			}
		},
	}
	runCmd.Flags().BoolVar(&once, "once", false, "process at most one queued item and exit")
	runCmd.Flags().BoolVar(&live, "live", false, "consume owner bot events in real time and poll user-visible Lark conversations as fallback")
	runCmd.Flags().BoolVar(&dryRun, "dry-run", false, "run live intake and model decisions without sending replies")
	runCmd.Flags().StringVar(&chatQuery, "chat-query", "", "chat keyword used to mark configured and validation groups")
	runCmd.Flags().BoolVar(&includePrivate, "include-private", true, "include private chats in live user-visible polling")
	runCmd.Flags().DurationVar(&pollInterval, "poll-interval", 30*time.Second, "live polling interval")
	cmd.AddCommand(runCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Render macOS launchd service configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			program, err := vfs.Executable()
			if err != nil {
				program = "lark-agent"
			}
			path := resolveConfigPath(*configPath)
			return writeData(out, map[string]any{
				"label": daemon.Label,
				"plist": daemon.LaunchdPlist(daemon.LaunchdConfig{
					Label:        daemon.Label,
					Program:      program,
					ConfigPath:   path,
					StatePath:    resolveStatePath(*statePath),
					Live:         true,
					ChatQuery:    "Test Group",
					PollInterval: "10s",
				}),
				"hint": "write this plist to ~/Library/LaunchAgents/" + daemon.Label + ".plist and load it with launchctl after reviewing it",
			})
		},
	})
	cmd.AddCommand(newDaemonInstallAppCommand(out, configPath, statePath))
	cmd.AddCommand(newDaemonStatusCommand(out))
	cmd.AddCommand(newDaemonStartCommand(out))
	cmd.AddCommand(newDaemonStopCommand(out))
	cmd.AddCommand(newDaemonRestartCommand(out))
	cmd.AddCommand(newDaemonUninstallCommand(out))
	return cmd
}

func lifecycleRecoverySummary(
	ctx context.Context,
	store *storage.Store,
) (lifecycle.Summary, error) {
	interrupted, uncertain, err := store.RecoverySummary(ctx)
	if err != nil {
		return lifecycle.Summary{}, err
	}
	return lifecycle.Summary{
		Paused:    interrupted,
		Uncertain: uncertain,
	}, nil
}

func notifyRecoveryConvergence(
	ctx context.Context,
	store *storage.Store,
	messenger agenttools.Messenger,
	notice storage.RecoveryConvergenceNotice,
	owner config.OwnerConfig,
) error {
	item, err := store.GetWorkItem(ctx, notice.WorkItemID)
	if err != nil {
		return err
	}
	language := agentlocale.Resolve(
		owner.PreferredLanguage,
		owner.FallbackLanguage,
		item.Event.Content,
	)
	text := recoveryConvergenceText(notice, owner, language)
	return sendOwnerResolutionText(ctx, store, messenger, notice.WorkItemID, text)
}

func sendOwnerResolutionText(
	ctx context.Context,
	store *storage.Store,
	messenger agenttools.Messenger,
	workItemID int64,
	text string,
) error {
	actionID, key, send, err := store.BeginOwnerResolutionNotification(
		ctx,
		workItemID,
		text,
	)
	if err != nil || !send {
		return err
	}
	return executeOwnerResolutionSend(ctx, store, messenger, actionID, key, text)
}

func sendOwnerResolutionRequirement(
	ctx context.Context,
	store *storage.Store,
	messenger agenttools.Messenger,
	requirement storage.RequiredOwnerResolutionNotification,
	text string,
) error {
	actionID, key, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		ctx,
		requirement.ID,
		requirement.WorkItemID,
		text,
	)
	if err != nil || !send {
		return err
	}
	return executeOwnerResolutionSend(ctx, store, messenger, actionID, key, text)
}

func retryOwnerResolutionText(
	ctx context.Context,
	store *storage.Store,
	messenger agenttools.Messenger,
	actionID int64,
) error {
	notice, send, err := store.RetryOwnerResolutionNotification(ctx, actionID)
	if err != nil || !send {
		return err
	}
	return executeOwnerResolutionSend(
		ctx,
		store,
		messenger,
		notice.ID,
		notice.IdempotencyKey,
		notice.Text,
	)
}

func executeOwnerResolutionSend(
	ctx context.Context,
	store *storage.Store,
	messenger agenttools.Messenger,
	actionID int64,
	key string,
	text string,
) error {
	sendErr := (agenttools.NotifyOwnerTool{Messenger: messenger}).Execute(ctx, agenttools.NotifyRequest{
		Text:           text,
		IdempotencyKey: key,
	})
	errorText := ""
	if sendErr != nil {
		errorText = sendErr.Error()
	}
	if completeErr := store.CompleteOwnerResolutionNotification(
		context.WithoutCancel(ctx),
		actionID,
		errorText,
	); completeErr != nil {
		if sendErr != nil {
			return errs.NewInternalError(
				errs.SubtypeStorage,
				"send and record recovery resolution notice",
			).WithCause(errors.Join(sendErr, completeErr))
		}
		return completeErr
	}
	return sendErr
}

func flushOwnerResolutionNotifications(
	ctx context.Context,
	store *storage.Store,
	messenger agenttools.Messenger,
	owner config.OwnerConfig,
	errOut io.Writer,
) error {
	if err := store.ConvergeOwnerResolutionRequirements(ctx); err != nil {
		return err
	}
	pending, err := store.ListPendingOwnerResolutionNotifications(ctx)
	if err != nil {
		return err
	}
	for _, notice := range pending {
		if err := retryOwnerResolutionText(
			ctx,
			store,
			messenger,
			notice.ID,
		); err != nil {
			writeError(errOut, err)
		}
	}
	required, err := store.ListRequiredOwnerResolutionNotifications(ctx)
	if err != nil {
		return err
	}
	handler := liveTerminalFailureHandler{
		store:     store,
		messenger: messenger,
		owner:     owner,
	}
	for _, requirement := range required {
		item, err := store.GetWorkItem(ctx, requirement.WorkItemID)
		if err != nil {
			writeError(errOut, err)
			continue
		}
		if err := handler.handleTerminalFailureRequirement(
			ctx,
			item,
			errors.New(requirement.Reason),
			requirement,
		); err != nil {
			writeError(errOut, err)
		}
	}
	return nil
}

func recoveryConvergenceText(
	notice storage.RecoveryConvergenceNotice,
	owner config.OwnerConfig,
	language agentlocale.Language,
) string {
	name := strings.TrimSpace(owner.Name)
	if name == "" {
		if language == agentlocale.LanguageEnglish {
			name = "Owner"
		} else {
			name = "负责人"
		}
	}
	if language == agentlocale.LanguageEnglish {
		if notice.Kind == "approval_required" {
			command := "`lark-agent approval list`"
			if notice.ActionID > 0 {
				command = fmt.Sprintf("`lark-agent approval approve %d`", notice.ActionID)
			}
			return fmt.Sprintf(
				"%s, work #%d for message %s is waiting for your exact approval. The original draft was preserved. Review it with `lark-agent queue inspect --work-id %d`, then run %s if it is correct.",
				name,
				notice.WorkItemID,
				notice.MessageID,
				notice.WorkItemID,
				command,
			)
		}
		return fmt.Sprintf(
			"%s, work #%d for message %s was terminalized because an external action was interrupted with an uncertain result. It was not replayed. Reconcile it with `lark-agent queue inspect --work-id %d`.",
			name,
			notice.WorkItemID,
			notice.MessageID,
			notice.WorkItemID,
		)
	}
	if notice.Kind == "approval_required" {
		command := "`lark-agent approval list`"
		if notice.ActionID > 0 {
			command = fmt.Sprintf("`lark-agent approval approve %d`", notice.ActionID)
		}
		return fmt.Sprintf(
			"%s，工作 #%d（消息 %s）仍在等待你的明确批准，原草稿已保留。先执行 `lark-agent queue inspect --work-id %d` 核对；确认无误后执行 %s。",
			name,
			notice.WorkItemID,
			notice.MessageID,
			notice.WorkItemID,
			command,
		)
	}
	return fmt.Sprintf(
		"%s，工作 #%d（消息 %s）已收口：中断时有外部动作正在执行，结果无法确认，因此没有重放。请执行 `lark-agent queue inspect --work-id %d` 核对外部结果。",
		name,
		notice.WorkItemID,
		notice.MessageID,
		notice.WorkItemID,
	)
}

func notifyLifecycleOffline(
	store *storage.Store,
	controller *lifecycle.Controller,
	sessionID string,
	errOut io.Writer,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := store.PauseCurrentSessionWork(ctx, "intentional daemon shutdown"); err != nil {
		writeError(errOut, err)
	}
	summary, err := lifecycleRecoverySummary(ctx, store)
	if err != nil {
		writeError(errOut, err)
	} else if err := controller.NotifyOffline(ctx, sessionID, summary); err != nil {
		writeError(errOut, err)
	}
	if _, err := store.StopCurrentSession(ctx, "intentional daemon shutdown"); err != nil {
		writeError(errOut, err)
	}
}

func newDaemonInstallAppCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	var write, load, live bool
	var program, chatQuery string
	var pollInterval time.Duration
	cmd := &cobra.Command{
		Use:   "install-app",
		Short: "Install the user-level macOS LaunchAgent",
		RunE: func(cmd *cobra.Command, args []string) error {
			if pollInterval <= 0 {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "--poll-interval must be positive").WithParam("--poll-interval")
			}
			if program == "" {
				exe, err := vfs.Executable()
				if err != nil {
					return errs.NewInternalError(errs.SubtypeFileIO, "resolve executable path").WithCause(err)
				}
				program = exe
			}
			controller, err := daemon.NewController()
			if err != nil {
				return err
			}
			req := daemon.InstallRequest{
				Program:      program,
				ConfigPath:   resolveConfigPath(*configPath),
				StatePath:    resolveStatePath(*statePath),
				Live:         live,
				ChatQuery:    chatQuery,
				PollInterval: pollInterval.String(),
				Load:         load,
				Environment:  launchdEnvironment(),
			}
			if !write {
				paths := daemon.UserPaths(controller.HomeDir)
				return writeData(out, map[string]any{
					"action":    "install-app",
					"written":   false,
					"load":      load,
					"paths":     paths,
					"arguments": daemon.LaunchdProgramArguments(daemon.LaunchdConfig{Program: req.Program, ConfigPath: req.ConfigPath, StatePath: req.StatePath, Live: req.Live, ChatQuery: req.ChatQuery, PollInterval: req.PollInterval}),
					"hint":      "pass --write to write the LaunchAgent plist; pass --load to start it",
				})
			}
			status, err := controller.Install(cmd.Context(), req)
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": "install-app", "written": true, "loaded": load, "status": status})
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "write the LaunchAgent plist")
	cmd.Flags().BoolVar(&load, "load", false, "load and start the LaunchAgent after writing")
	cmd.Flags().StringVar(&program, "program", "", "lark-agent binary path (default: current executable)")
	cmd.Flags().BoolVar(&live, "live", true, "run daemon with live Lark polling")
	cmd.Flags().StringVar(&chatQuery, "chat-query", "Test Group", "chat keyword used to mark configured and validation groups")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 10*time.Second, "daemon live polling interval")
	return cmd
}

func launchdEnvironment() map[string]string {
	return nil
}

func newDaemonStatusCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show macOS LaunchAgent status",
		RunE: func(cmd *cobra.Command, args []string) error {
			controller, err := daemon.NewController()
			if err != nil {
				return err
			}
			status, err := controller.Status(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, status)
		},
	}
}

func newDaemonStartCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the macOS LaunchAgent",
		RunE: func(cmd *cobra.Command, args []string) error {
			controller, err := daemon.NewController()
			if err != nil {
				return err
			}
			status, err := controller.Start(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": "start", "status": status})
		},
	}
}

func newDaemonStopCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the macOS LaunchAgent",
		RunE: func(cmd *cobra.Command, args []string) error {
			controller, err := daemon.NewController()
			if err != nil {
				return err
			}
			status, err := controller.Stop(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": "stop", "status": status})
		},
	}
}

func newDaemonRestartCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the macOS LaunchAgent",
		RunE: func(cmd *cobra.Command, args []string) error {
			controller, err := daemon.NewController()
			if err != nil {
				return err
			}
			_, _ = controller.Stop(cmd.Context())
			status, err := controller.Start(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": "restart", "status": status})
		},
	}
}

func newDaemonUninstallCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Unload and remove the user-level LaunchAgent",
		RunE: func(cmd *cobra.Command, args []string) error {
			controller, err := daemon.NewController()
			if err != nil {
				return err
			}
			status, err := controller.Uninstall(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": "uninstall", "status": status})
		},
	}
}

func buildLiveOptions(
	ctx context.Context,
	cfg config.Config,
	store *storage.Store,
	agentRouter *router.Router,
	live, dryRun bool,
	chatQuery string,
	includePrivate bool,
) ([]app.Option, realtime.Runner, agenttools.Messenger, map[string]any, error) {
	info := map[string]any{
		"enabled":                 live,
		"dry_run":                 dryRun,
		"model_configured":        false,
		"chat_query":              chatQuery,
		"assistant_reply_scope":   cfg.Assistant.ReplyScope,
		"reply_scope":             cfg.Policy.ReplyScope,
		"include_private":         includePrivate,
		"realtime_owner_requests": false,
		"realtime_requests":       false,
	}
	if !live {
		return nil, nil, nil, info, nil
	}
	if err := validateLiveReplyScopes(cfg.Assistant.ReplyScope, cfg.Policy.ReplyScope, chatQuery); err != nil {
		return nil, nil, nil, info, err
	}
	if os.Getenv("LARK_AGENT_OFFLINE_LIVE_TEST") == "1" {
		info["offline_live_test"] = true
		return nil, nil, nil, info, nil
	}
	store.ConfigureRecovery(cfg.Agent.MaxRetries)
	credentials, err := serviceim.LoadCredentials(ctx, credentialRefs(cfg))
	if err != nil {
		return nil, nil, nil, info, err
	}
	apiClient, err := serviceim.NewClient(serviceim.ClientConfig{
		AppID:           cfg.Lark.AppID,
		AppSecret:       credentials.AppSecret,
		UserAccessToken: credentials.UserAccessToken,
		RefreshToken:    credentials.RefreshToken,
		UserTokenStore:  serviceim.NewKeychainUserTokenStore(credentialRefs(cfg)),
		BaseURL:         cfg.Lark.BaseURL,
		Timeout:         30 * time.Second,
	})
	if err != nil {
		return nil, nil, nil, info, err
	}
	imSvc := serviceim.NewService(apiClient, cfg.Owner.OpenID)
	var realtimeSource realtime.Runner
	var configuredAssistantChatIDs []string
	if cfg.Assistant.ReplyScope == domain.ReplyScopeConfiguredGroups {
		configuredAssistantChatIDs, err = discoverConfiguredAssistantChats(ctx, imSvc, chatQuery)
		if err != nil {
			return nil, nil, nil, info, err
		}
		info["configured_assistant_chat_ids"] = configuredAssistantChatIDs
	}
	if cfg.Assistant.OwnerDirect.Enabled || len(cfg.Assistant.OpenIDs) > 0 || len(cfg.Assistant.Names) > 0 {
		consumer := realtime.NewLarkConsumer(apiClient, realtime.LarkConsumerConfig{
			AppID:     cfg.Lark.AppID,
			AppSecret: credentials.AppSecret,
			BaseURL:   cfg.Lark.BaseURL,
		})
		realtimeSource = realtime.NewSource(consumer, store, realtime.Config{
			OwnerOpenID:         cfg.Owner.OpenID,
			AssistantOpenIDs:    cfg.Assistant.OpenIDs,
			AssistantNames:      cfg.Assistant.Names,
			AssistantReplyScope: cfg.Assistant.ReplyScope,
			ConfiguredChatIDs:   configuredAssistantChatIDs,
			Classify:            agentRouter.Route,
		})
		info["realtime_owner_requests"] = true
		info["realtime_requests"] = true
	}
	scope, err := workspace.NewScopeWithExcludes(cfg.Workspace.Root, cfg.Workspace.Excludes)
	if err != nil {
		return nil, nil, nil, info, err
	}
	ruleSet, err := rules.Load(scope)
	if err != nil {
		return nil, nil, nil, info, err
	}
	userContextEnabled := credentials.UserAccessToken != ""
	var githubClient *internalgithub.Client
	if cfg.GitHub.Enabled {
		token, tokenErr := secretstore.Read(
			ctx,
			cfg.GitHub.TokenKeychainService,
			cfg.GitHub.TokenKeychainKey,
			"GITHUB_TOKEN",
		)
		if tokenErr == nil {
			githubClient, err = newGitHubClient(cfg, token)
			if err != nil {
				return nil, nil, nil, info, err
			}
			info["github_context"] = true
		} else {
			info["github_context"] = false
			info["github_token"] = "missing"
		}
	} else {
		info["github_context"] = false
	}
	builder := &conversationBuilder{
		svc:                 imSvc,
		store:               store,
		currentAppID:        cfg.Lark.AppID,
		referenceSigningKey: credentials.AppSecret,
		allowedRepositories: append([]string(nil), cfg.GitHub.AllowedRepositories...),
		githubEnabled:       cfg.GitHub.Enabled,
		base: agentcontext.Builder{
			Scope:  scope,
			Rules:  ruleSet,
			Memory: memory.NewStore(),
			User: agentcontext.UserProfile{
				OpenID:            cfg.Owner.OpenID,
				Name:              cfg.Owner.Name,
				PreferredLanguage: string(cfg.Owner.PreferredLanguage),
				FallbackLanguage:  string(cfg.Owner.FallbackLanguage),
			},
		},
		includeLarkContext: userContextEnabled,
	}
	options := []app.Option{
		app.WithContextBuilder(builder),
		app.WithControlHandler(control.New(store, control.Config{
			OwnerName: cfg.Owner.Name,
			Language:  string(resolveConfiguredLanguage(cfg.Owner)),
			Version:   buildVersion(),
		})),
	}
	info["user_context"] = userContextEnabled
	if userContextEnabled {
		livePoller := newConfiguredLivePoller(
			imSvc,
			store,
			agentRouter,
			cfg,
			chatQuery,
			configuredAssistantChatIDs,
			includePrivate,
		)
		options = append(options, app.WithPoller(livePoller))
		info["user_polling"] = true
	} else {
		info["user_polling"] = false
		info["user_token"] = "missing"
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), cfg.Model.BaseURL)
	model := firstNonEmpty(os.Getenv("OPENAI_MODEL"), cfg.Model.Name)
	var delegatedResolver app.DelegatedReplyResolver
	if apiKey != "" && model != "" {
		modelFingerprint := model + "@" + baseURL
		configFingerprint, err := agentConfigFingerprint(cfg)
		if err != nil {
			return nil, nil, nil, info, err
		}
		definitions := append([]agenttools.Definition{}, agenttools.CodeIndexDefinitions(scope, nil)...)
		definitions = append(definitions, agenttools.WorkspaceDefinitions(scope)...)
		if userContextEnabled {
			definitions = append(definitions, agenttools.LarkContextDefinitions(larkToolContext{svc: imSvc})...)
		}
		if githubClient != nil {
			definitions = append(definitions, agenttools.GitHubContextDefinition(githubClient))
		}
		definitions = append(definitions,
			agenttools.ShellDefinition(scope, agenttools.ShellOptions{
				ApprovalRequired:     cfg.Agent.ShellApproval,
				Approvals:            store,
				MaxTimeout:           cfg.Agent.ShellTimeout,
				MaxOutputBytes:       cfg.Agent.MaxToolOutput,
				AllowUnboundedSearch: !cfg.ToolPolicy.DenyUnboundedShellSearch,
			}),
			agentruntime.SubmitInvestigationPlanDefinition(),
			agentruntime.SubmitDecisionDefinition(),
		)
		registry, err := agenttools.NewRegistry(definitions...)
		if err != nil {
			return nil, nil, nil, info, err
		}
		modelAdapter := &agentruntime.OpenAICompatibleModel{
			APIKey:  apiKey,
			BaseURL: baseURL,
			Model:   model,
			Timeout: cfg.Model.Timeout,
		}
		loopModelAdapter := modelAdapter
		if visionModel := strings.TrimSpace(cfg.Agent.VisionModel); visionModel != "" {
			loopModelAdapter = &agentruntime.OpenAICompatibleModel{
				APIKey:  apiKey,
				BaseURL: baseURL,
				Model:   visionModel,
				Timeout: cfg.Model.Timeout,
			}
			info["vision_model"] = visionModel
		}
		if userContextEnabled {
			delegatedResolver = &liveDelegatedReplyResolver{
				contexts:             imSvc,
				store:                store,
				matcher:              replymatch.New(modelAdapter, cfg.Owner.OpenID),
				ownerWait:            cfg.Policy.OwnerWait,
				visionEnabled:        strings.TrimSpace(cfg.Agent.VisionModel) != "",
				maxContextImages:     cfg.Agent.MaxContextImages,
				maxContextImageBytes: cfg.Agent.MaxContextImageBytes,
				maxContextImageTotal: cfg.Agent.MaxContextImageTotalBytes,
			}
			options = append(options, app.WithDelegatedReplyResolver(
				delegatedResolver,
				cfg.Policy.OwnerReplyConfidenceMin,
				cfg.Policy.OwnerReplyRetry,
			))
		}
		options = append(options, app.WithDecider(agentruntime.LoopDecisionAgent{Loop: agentruntime.AgentLoop{
			Model:             loopModelAdapter,
			Tools:             registry,
			MaxTurns:          cfg.Agent.MaxTurns,
			MaxToolBytes:      cfg.Agent.MaxToolOutput,
			MaxTotalBytes:     cfg.Agent.MaxTotalToolOutput,
			MaxContextBytes:   cfg.Agent.MaxContextBytes,
			ContextCompaction: cfg.Agent.ContextCompaction,
			MaxElapsed:        cfg.Agent.LoopTimeout,
			MaxRepeatedCalls:  cfg.Agent.MaxRepeatedCalls,
			MaxToolCalls:      cfg.ToolPolicy.CodingMaxToolCalls,
			MaxNoProgress:     cfg.ToolPolicy.MaxNoProgress,
			SimpleMaxTurns:    cfg.FastPath.SimpleMaxTurns,
			CodingMaxTurns:    cfg.FastPath.CodingMaxTurns,
			GoalMaxTurns:      cfg.Goal.MaxInvestigationTurns,
			Recorder:          store,
			ModelFingerprint:  loopModelAdapter.Model + "@" + baseURL,
			ConfigFingerprint: configFingerprint,
		}}))
		info["model_configured"] = true
		info["model"] = model
		info["agent_tools"] = len(registry.Infos())
		info["runtime_fingerprint"] = modelFingerprint + ":" + configFingerprint
	}
	if userContextEnabled && delegatedResolver == nil {
		delegatedResolver = unavailableDelegatedReplyResolver{}
		options = append(options, app.WithDelegatedReplyResolver(
			delegatedResolver,
			cfg.Policy.OwnerReplyConfidenceMin,
			cfg.Policy.OwnerReplyRetry,
		))
	}
	info["semantic_delegated_replies"] = delegatedResolver != nil
	if !dryRun {
		threadState := &liveThreadState{
			resolver:      delegatedResolver,
			confidenceMin: cfg.Policy.OwnerReplyConfidenceMin,
			resolutions:   make(map[string]replymatch.Resolution),
		}
		gate := policy.NewReplyGate(policy.Config{
			Mode:                cfg.Policy.Mode,
			OwnerOpenID:         cfg.Owner.OpenID,
			ReplyScope:          cfg.Policy.ReplyScope,
			AssistantReplyScope: cfg.Assistant.ReplyScope,
			ReplyConfidenceMin:  cfg.Policy.ReplyConfidenceMin,
			BlockChats:          cfg.Policy.BlockChats,
			BlockUsers:          cfg.Policy.BlockUsers,
		}, threadState)
		options = append(options,
			app.WithReplyHandler(reply.NewController(gate, imSvc, store)),
			app.WithDecisionPresenter(agentlocale.DelegatedPresenter{
				OwnerOpenID: cfg.Owner.OpenID,
				OwnerName:   cfg.Owner.Name,
				Preferred:   cfg.Owner.PreferredLanguage,
				Fallback:    cfg.Owner.FallbackLanguage,
			}),
			app.WithNotificationHandler(liveOwnerNotifier{
				messenger: imSvc,
				ownerName: cfg.Owner.Name,
				preferred: cfg.Owner.PreferredLanguage,
				fallback:  cfg.Owner.FallbackLanguage,
			}),
			app.WithTerminalFailureHandler(liveTerminalFailureHandler{
				store:     store,
				messenger: imSvc,
				owner:     cfg.Owner,
			}),
			app.WithOwnerActivityHandler(feedback.NewController(imSvc, store)),
		)
		if cfg.Policy.InvestigationProgress == "enabled" {
			options = append(options, app.WithInvestigationProgressHandler(
				investigation.New(store, imSvc, investigation.Config{
					OwnerName: cfg.Owner.Name,
					Language:  string(resolveConfiguredLanguage(cfg.Owner)),
				}),
			))
			info["investigation_progress"] = true
		} else {
			info["investigation_progress"] = false
		}
	}
	return options, realtimeSource, imSvc, info, nil
}

func validateLiveReplyScope(scope domain.ReplyScope, chatQuery string) error {
	if scope == domain.ReplyScopeConfiguredGroups && strings.TrimSpace(chatQuery) == "" {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"policy.reply_scope configured_groups requires --chat-query",
		).WithField("policy.reply_scope").
			WithHint("set policy.reply_scope to all_groups or provide --chat-query")
	}
	return nil
}

func validateLiveReplyScopes(assistantScope, delegatedScope domain.ReplyScope, chatQuery string) error {
	if assistantScope == domain.ReplyScopeConfiguredGroups && strings.TrimSpace(chatQuery) == "" {
		return errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"assistant.reply_scope configured_groups requires --chat-query",
		).WithField("assistant.reply_scope").
			WithHint("set assistant.reply_scope to all_groups or provide --chat-query")
	}
	return validateLiveReplyScope(delegatedScope, chatQuery)
}

func discoverConfiguredAssistantChats(
	ctx context.Context,
	imSvc *serviceim.Service,
	chatQuery string,
) ([]string, error) {
	var chatIDs []string
	seen := map[string]bool{}
	pageToken := ""
	for {
		result, err := imSvc.SearchChats(ctx, serviceim.SearchChatsRequest{
			Query:     strings.TrimSpace(chatQuery),
			PageSize:  100,
			PageToken: pageToken,
			As:        serviceim.IdentityBot,
		})
		if err != nil {
			return nil, err
		}
		for _, chat := range result.Items {
			if chat.ChatID != "" && !seen[chat.ChatID] {
				seen[chat.ChatID] = true
				chatIDs = append(chatIDs, chat.ChatID)
			}
		}
		if !result.HasMore || result.PageToken == "" {
			break
		}
		pageToken = result.PageToken
	}
	if len(chatIDs) == 0 {
		return nil, errs.NewConfigError(
			errs.SubtypeInvalidConfig,
			"assistant.reply_scope configured_groups query matched no bot-visible group",
		).WithField("assistant.reply_scope").
			WithHint("adjust --chat-query or set assistant.reply_scope to all_groups")
	}
	return chatIDs, nil
}

func newConfiguredLivePoller(
	im poll.IMClient,
	store poll.Store,
	agentRouter *router.Router,
	cfg config.Config,
	chatQuery string,
	configuredAssistantChatIDs []string,
	includePrivate bool,
) *poll.Poller {
	return poll.New(im, store, poll.Config{
		OwnerOpenID:                cfg.Owner.OpenID,
		ChatQuery:                  chatQuery,
		AssistantOpenIDs:           cfg.Assistant.OpenIDs,
		AssistantNames:             cfg.Assistant.Names,
		ConfiguredAssistantChatIDs: configuredAssistantChatIDs,
		IncludePrivate:             includePrivate,
		PageSize:                   20,
		IndexLookback:              cfg.Scheduler.PollIndexLookback,
		OwnerWait:                  cfg.Policy.OwnerWait,
		Classify:                   agentRouter.Route,
	})
}

func newAgentRouter(cfg config.Config, store *storage.Store) *router.Router {
	queueText := func() string {
		english := resolveConfiguredLanguage(cfg.Owner) == agentlocale.LanguageEnglish
		action, err := store.ListOwnerTasks(context.Background(), domain.OwnerTaskQuery{
			View: domain.OwnerTaskViewAction, Page: 1, PageSize: 1,
		})
		if err != nil {
			if english {
				return "The task summary is temporarily unavailable."
			}
			return "任务摘要暂时不可用，请稍后重试或发送 `/doctor` 检查。"
		}
		running, err := store.ListOwnerTasks(context.Background(), domain.OwnerTaskQuery{
			View: domain.OwnerTaskViewRunning, Page: 1, PageSize: 1,
		})
		if err != nil {
			if english {
				return "The task summary is temporarily unavailable."
			}
			return "任务摘要暂时不可用，请稍后重试或发送 `/doctor` 检查。"
		}
		if english {
			return fmt.Sprintf(
				"Needs your action: %d. In progress or waiting automatically: %d. Use `/tasks` for details.",
				action.Total,
				running.Total,
			)
		}
		return fmt.Sprintf(
			"需要你处理 %d 条；正在执行或自动等待 %d 条。发送 `/tasks` 查看详情。",
			action.Total,
			running.Total,
		)
	}
	language := string(resolveConfiguredLanguage(cfg.Owner))
	return router.New(router.Config{
		OwnerOpenID:         cfg.Owner.OpenID,
		AssistantOpenIDs:    cfg.Assistant.OpenIDs,
		AssistantNames:      cfg.Assistant.Names,
		AssistantReplyScope: cfg.Assistant.ReplyScope,
		OwnerDirect:         cfg.Assistant.OwnerDirect.Enabled,
		Mode:                cfg.Policy.Mode,
		ReplyScope:          cfg.Policy.ReplyScope,
		PrivateReplyScope:   cfg.Policy.PrivateReplyScope,
		AllowChats:          cfg.Policy.AllowChats,
		BlockChats:          cfg.Policy.BlockChats,
		BlockUsers:          cfg.Policy.BlockUsers,
		Sensitivity:         cfg.Policy.Sensitivity,
		DisableFastPath:     !cfg.FastPath.Enabled,
		DisableCodingGoal:   !cfg.Goal.Enabled,
		StatusText:          func() string { return queueText() },
		DoctorText:          func() string { return queueText() },
		QueueSummaryText:    queueText,
		HelpText:            control.HelpText(language, ""),
		Language:            language,
	})
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || strings.TrimSpace(info.Main.Version) == "" {
		return "development"
	}
	return info.Main.Version
}

func runSchedulerWorker(ctx context.Context, daemonApp *app.Daemon, errOut io.Writer) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := daemonApp.RunOnce(ctx); err != nil && ctx.Err() == nil {
			writeError(errOut, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func startSchedulerWorker(
	group *sync.WaitGroup,
	ctx context.Context,
	daemonApp *app.Daemon,
	errOut io.Writer,
) {
	group.Add(1)
	go func() {
		defer group.Done()
		runSchedulerWorker(ctx, daemonApp, errOut)
	}()
}

type agentOperatingContract struct {
	SystemPrompt              string
	SubmitDecisionName        string
	SubmitDecisionDescription string
	SubmitDecisionSchema      string
}

func currentAgentOperatingContract() (agentOperatingContract, error) {
	definition := agentruntime.SubmitDecisionDefinition()
	if definition.Info == nil || definition.Info.ParamsOneOf == nil {
		return agentOperatingContract{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"submit_decision operating contract is incomplete",
		)
	}
	decisionSchema, err := definition.Info.ToJSONSchema()
	if err != nil {
		return agentOperatingContract{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"encode submit_decision operating contract",
		).WithCause(err)
	}
	schemaJSON, err := json.Marshal(decisionSchema)
	if err != nil {
		return agentOperatingContract{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"marshal submit_decision operating contract",
		).WithCause(err)
	}
	return agentOperatingContract{
		SystemPrompt:              agentcontext.AgentSystemPrompt(),
		SubmitDecisionName:        definition.Info.Name,
		SubmitDecisionDescription: definition.Info.Desc,
		SubmitDecisionSchema:      string(schemaJSON),
	}, nil
}

func agentConfigFingerprint(cfg config.Config) (string, error) {
	contract, err := currentAgentOperatingContract()
	if err != nil {
		return "", err
	}
	return agentConfigFingerprintForContract(cfg, contract), nil
}

func agentConfigFingerprintForContract(cfg config.Config, contract agentOperatingContract) string {
	data, _ := json.Marshal(struct {
		Agent             config.AgentConfig
		Policy            config.PolicyConfig
		GitHub            config.GitHubConfig
		Workspace         config.WorkspaceConfig
		OperatingContract agentOperatingContract
	}{
		Agent:             cfg.Agent,
		Policy:            cfg.Policy,
		GitHub:            cfg.GitHub,
		Workspace:         cfg.Workspace,
		OperatingContract: contract,
	})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

type conversationBuilder struct {
	svc                 *serviceim.Service
	store               *storage.Store
	currentAppID        string
	referenceSigningKey string
	allowedRepositories []string
	githubEnabled       bool
	includeLarkContext  bool
	base                agentcontext.Builder
}

type semanticReplyContextReader interface {
	GetSemanticReplyContext(
		context.Context,
		serviceim.SemanticReplyContextRequest,
	) (serviceim.SemanticReplyContext, error)
}

type semanticContextImageHydrator interface {
	HydrateContextImages(
		context.Context,
		[]serviceim.Message,
		serviceim.ImageHydrationLimits,
	) []serviceim.Message
}

type semanticReplyStore interface {
	ListPendingDelegatedWork(string) ([]domain.WorkItem, error)
	RecordOwnerReplyResolution(int64, replymatch.Resolution) error
}

type semanticReplyMatcher interface {
	Resolve(context.Context, replymatch.Request) (replymatch.Resolution, error)
}

type unavailableDelegatedReplyResolver struct{}

func (unavailableDelegatedReplyResolver) Resolve(
	context.Context,
	domain.WorkItem,
) (replymatch.Resolution, error) {
	return replymatch.Resolution{}, errs.NewInternalError(
		errs.SubtypeFailedPrecondition,
		"semantic delegated reply model is unavailable",
	)
}

type liveDelegatedReplyResolver struct {
	contexts             semanticReplyContextReader
	store                semanticReplyStore
	matcher              semanticReplyMatcher
	maxMessages          int
	ownerWait            time.Duration
	visionEnabled        bool
	maxContextImages     int
	maxContextImageBytes int64
	maxContextImageTotal int64
}

func (r liveDelegatedReplyResolver) Resolve(
	ctx context.Context,
	item domain.WorkItem,
) (replymatch.Resolution, error) {
	ownerWait := r.ownerWait
	if ownerWait <= 0 {
		ownerWait = 3 * time.Minute
	}
	pending, err := r.store.ListPendingDelegatedWork(item.Event.ChatID)
	if err != nil {
		return replymatch.Resolution{}, err
	}
	since := item.Event.CreatedAt
	for _, candidate := range pending {
		if candidate.Event.CreatedAt.IsZero() {
			continue
		}
		if since.IsZero() || candidate.Event.CreatedAt.Before(since) {
			since = candidate.Event.CreatedAt
		}
	}
	if !since.IsZero() {
		since = since.Add(-ownerWait)
	}
	maxMessages := r.maxMessages
	if maxMessages <= 0 {
		maxMessages = 100
	}
	larkContext, err := r.contexts.GetSemanticReplyContext(
		ctx,
		serviceim.SemanticReplyContextRequest{
			ChatID:          item.Event.ChatID,
			TargetMessageID: item.Event.MessageID,
			Since:           since,
			MaxMessages:     maxMessages,
		},
	)
	if err != nil {
		return replymatch.Resolution{}, err
	}
	if larkContext.Withdrawn {
		resolution := replymatch.Resolution{
			TargetMessageID: item.Event.MessageID,
			Result:          replymatch.ResultWithdrawn,
			Confidence:      1,
			Reason:          "target message was withdrawn",
			ContextCutoff:   larkContext.ContextCutoff,
		}
		if err := r.store.RecordOwnerReplyResolution(item.ID, resolution); err != nil {
			return replymatch.Resolution{}, err
		}
		return resolution, nil
	}
	if r.visionEnabled {
		if hydrator, ok := r.contexts.(semanticContextImageHydrator); ok {
			larkContext.Messages = hydrator.HydrateContextImages(
				ctx,
				larkContext.Messages,
				serviceim.ImageHydrationLimits{
					MaxImages:     r.maxContextImages,
					MaxImageBytes: r.maxContextImageBytes,
					MaxTotalBytes: r.maxContextImageTotal,
					As:            serviceim.IdentityUser,
				},
			)
		}
	} else {
		for messageIndex := range larkContext.Messages {
			for attachmentIndex := range larkContext.Messages[messageIndex].Attachments {
				attachment := &larkContext.Messages[messageIndex].Attachments[attachmentIndex]
				if attachment.Type == "image" && !attachment.Readable {
					attachment.UnreadableReason = "vision_model_not_configured"
				}
			}
		}
	}

	messages := normalizeToolMessages(larkContext.Messages)
	latest := make(map[string]domain.NormalizedEvent, len(messages))
	for _, message := range messages {
		latest[message.MessageID] = message
	}
	target := item
	if event, ok := latest[item.Event.MessageID]; ok {
		target.Event = event
	}
	for index := range pending {
		if event, ok := latest[pending[index].Event.MessageID]; ok {
			pending[index].Event = event
		}
	}
	if !target.Event.UpdatedAt.IsZero() &&
		target.Event.UpdatedAt.After(target.Event.CreatedAt) {
		deadline := target.Event.UpdatedAt.Add(ownerWait)
		cutoff := larkContext.ContextCutoff
		if cutoff.IsZero() {
			cutoff = time.Now().UTC()
		}
		if deadline.After(cutoff) {
			resolution := replymatch.Resolution{
				TargetMessageID: item.Event.MessageID,
				Result:          replymatch.ResultAmbiguous,
				Reason:          "target was edited inside the owner wait window",
				ContextCutoff:   cutoff,
				RetryAfter:      deadline.Sub(cutoff),
			}
			if err := r.store.RecordOwnerReplyResolution(item.ID, resolution); err != nil {
				return replymatch.Resolution{}, err
			}
			return resolution, nil
		}
	}
	if larkContext.Incomplete {
		resolution := replymatch.Resolution{
			TargetMessageID: item.Event.MessageID,
			Result:          replymatch.ResultAmbiguous,
			Reason:          "same-chat context is incomplete",
			ContextCutoff:   larkContext.ContextCutoff,
		}
		if err := r.store.RecordOwnerReplyResolution(item.ID, resolution); err != nil {
			return replymatch.Resolution{}, err
		}
		return resolution, nil
	}
	resolution, err := r.matcher.Resolve(ctx, replymatch.Request{
		Target:        target,
		Pending:       pending,
		Messages:      messages,
		ContextCutoff: larkContext.ContextCutoff,
	})
	if err != nil {
		resolution = replymatch.Resolution{
			TargetMessageID: item.Event.MessageID,
			Result:          replymatch.ResultAmbiguous,
			Reason:          "semantic owner-reply evaluation failed",
			ContextCutoff:   larkContext.ContextCutoff,
		}
	}
	if resolution.ContextCutoff.IsZero() {
		resolution.ContextCutoff = larkContext.ContextCutoff
	}
	resolution.ContextMessages = append(
		[]domain.NormalizedEvent(nil),
		messages...,
	)
	contextJSON, digestErr := json.Marshal(messages)
	if digestErr != nil {
		return replymatch.Resolution{}, errs.NewInternalError(
			errs.SubtypeUnknown,
			"encode delegated context digest",
		).WithCause(digestErr)
	}
	contextDigest := sha256.Sum256(contextJSON)
	resolution.ContextDigest = fmt.Sprintf("sha256:%x", contextDigest[:])
	if err := r.store.RecordOwnerReplyResolution(item.ID, resolution); err != nil {
		return replymatch.Resolution{}, err
	}
	return resolution, nil
}

type larkToolContext struct {
	svc *serviceim.Service
}

type liveOwnerNotifier struct {
	messenger agenttools.Messenger
	ownerName string
	preferred agentlocale.Language
	fallback  agentlocale.Language
}

type liveTerminalFailureHandler struct {
	store     *storage.Store
	messenger agenttools.Messenger
	owner     config.OwnerConfig
}

func (h liveTerminalFailureHandler) HandleTerminalFailure(
	ctx context.Context,
	item domain.WorkItem,
	runErr error,
) error {
	required, err := h.store.ListRequiredOwnerResolutionNotifications(ctx)
	if err != nil {
		return err
	}
	for _, requirement := range required {
		if requirement.WorkItemID == item.ID {
			return h.handleTerminalFailureRequirement(ctx, item, runErr, requirement)
		}
	}
	current, err := h.store.GetWorkItem(ctx, item.ID)
	if err != nil {
		return err
	}
	if current.Status != domain.StatusDeadLetter {
		return nil
	}
	return sendOwnerResolutionText(
		ctx,
		h.store,
		h.messenger,
		current.ID,
		h.terminalFailureText(current, runErr),
	)
}

func (h liveTerminalFailureHandler) handleTerminalFailureRequirement(
	ctx context.Context,
	item domain.WorkItem,
	runErr error,
	requirement storage.RequiredOwnerResolutionNotification,
) error {
	return sendOwnerResolutionRequirement(
		ctx,
		h.store,
		h.messenger,
		requirement,
		h.terminalFailureText(item, runErr),
	)
}

func (h liveTerminalFailureHandler) terminalFailureText(
	item domain.WorkItem,
	runErr error,
) string {
	language := agentlocale.Resolve(
		h.owner.PreferredLanguage,
		h.owner.FallbackLanguage,
		item.Event.Content,
	)
	name := strings.TrimSpace(h.owner.Name)
	if name == "" {
		if language == agentlocale.LanguageEnglish {
			name = "Owner"
		} else {
			name = "负责人"
		}
	}
	reason := agentlocale.LocalizedReason(language, runErr.Error())
	var text string
	if language == agentlocale.LanguageEnglish {
		text = fmt.Sprintf(
			"%s, work #%d for message %s has stopped after bounded retries: %s. Completed evidence and failure history were preserved. In the intelligent-assistant private chat, send `/task %d` to inspect it; after reviewing side effects, send `/task resume %d confirm` only if it should run again.",
			name,
			item.ID,
			item.Event.MessageID,
			reason,
			item.ID,
			item.ID,
		)
	} else {
		text = fmt.Sprintf(
			"%s，工作 #%d（消息 %s）在有界重试后已停止：%s。已完成的证据和失败记录均已保留。请在智能助手私聊发送 `/task %d` 查看；核对外部动作后，确需重做时再发送 `/task resume %d confirm`。",
			name,
			item.ID,
			item.Event.MessageID,
			reason,
			item.ID,
			item.ID,
		)
	}
	return text
}

func (n liveOwnerNotifier) HandleNotification(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	idempotencyKey string,
) error {
	language := agentlocale.Language(decision.Language)
	if language != agentlocale.LanguageChinese && language != agentlocale.LanguageEnglish {
		language = agentlocale.Resolve(
			n.preferred,
			n.fallback,
			item.Event.Content,
			decision.ReplyText,
		)
	}
	return (agenttools.NotifyOwnerTool{Messenger: n.messenger}).Execute(ctx, agenttools.NotifyRequest{
		Text:           ownerNotificationText(item, decision, n.ownerName, string(language)),
		IdempotencyKey: idempotencyKey,
	})
}

func (n liveOwnerNotifier) HandleApprovalNotification(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	action domain.Action,
) error {
	if action.ID <= 0 || action.Status != domain.ActionAwaitingApproval {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"reply approval notification requires a persisted awaiting approval action",
		)
	}
	language := agentlocale.Language(decision.Language)
	if language != agentlocale.LanguageChinese && language != agentlocale.LanguageEnglish {
		language = agentlocale.Resolve(
			n.preferred,
			n.fallback,
			item.Event.Content,
			decision.ReplyText,
		)
	}
	return (agenttools.NotifyOwnerTool{Messenger: n.messenger}).Execute(ctx, agenttools.NotifyRequest{
		Text: approvalNotificationText(
			item,
			decision,
			action,
			n.ownerName,
			string(language),
		),
		IdempotencyKey: fmt.Sprintf("owner-approval-notice:%d", action.ID),
	})
}

func ownerNotificationText(item domain.WorkItem, decision domain.Decision, ownerName, language string) string {
	resolved := agentlocale.Language(language)
	if resolved != agentlocale.LanguageEnglish {
		resolved = agentlocale.LanguageChinese
	}
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		if resolved == agentlocale.LanguageEnglish {
			ownerName = "Owner"
		} else {
			ownerName = "负责人"
		}
	}
	if decision.Kind == domain.DecisionReply {
		ownerAction := strings.TrimSpace(decision.OwnerAction)
		if ownerAction == "" || agentlocale.ValidateProse(ownerAction, resolved) != nil {
			if resolved == agentlocale.LanguageEnglish {
				ownerAction = "Please review the assistant reply and decide whether follow-up is needed."
			} else {
				ownerAction = "请查看智能助手的回复，并确认是否还需要后续处理"
			}
		}
		if resolved == agentlocale.LanguageEnglish {
			return fmt.Sprintf(
				"%s, the intelligent assistant received a delegated request for message %s and is preparing this reply:\n\n%s\n\nYour next action: %s",
				ownerName,
				item.Event.MessageID,
				decision.ReplyText,
				ownerAction,
			)
		}
		return fmt.Sprintf(
			"%s，智能助手已收到消息 %s 的代回复请求，正在准备以下答复：\n\n%s\n\n仍需你处理：%s。",
			ownerName,
			item.Event.MessageID,
			decision.ReplyText,
			ownerAction,
		)
	}
	reason := agentlocale.LocalizedReason(resolved, decision.Reason)
	if resolved == agentlocale.LanguageEnglish {
		return fmt.Sprintf(
			"%s, message %s needs your attention: %s Inspect with `lark-agent queue inspect --message-id %s`.",
			ownerName,
			item.Event.MessageID,
			reason,
			item.Event.MessageID,
		)
	}
	return fmt.Sprintf(
		"%s，消息 %s 需要你关注：%s。可执行 `lark-agent queue inspect --message-id %s` 查看已完成的检查和下一步。",
		ownerName,
		item.Event.MessageID,
		reason,
		item.Event.MessageID,
	)
}

func approvalNotificationText(
	item domain.WorkItem,
	decision domain.Decision,
	action domain.Action,
	ownerName,
	language string,
) string {
	resolved := agentlocale.Language(language)
	if resolved != agentlocale.LanguageEnglish {
		resolved = agentlocale.LanguageChinese
	}
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		if resolved == agentlocale.LanguageEnglish {
			ownerName = "Owner"
		} else {
			ownerName = "负责人"
		}
	}
	ownerAction := strings.TrimSpace(decision.OwnerAction)
	if ownerAction == "" || agentlocale.ValidateProse(ownerAction, resolved) != nil {
		if resolved == agentlocale.LanguageEnglish {
			ownerAction = "Review the exact draft and decide whether it may be sent."
		} else {
			ownerAction = "查看完整草稿，并决定是否允许发送"
		}
	}
	delegated := isDelegatedApprovalRelevance(decision.Relevance)
	if resolved == agentlocale.LanguageEnglish {
		draftRole := "assistant reply draft"
		if delegated {
			draftRole = "delegated reply draft"
		}
		return fmt.Sprintf(
			"%s, the %s for message %s is waiting for your approval (#%d) and has not been sent:\n\n%s\n\nYour next action: %s\n\nApprove: `/approval approve %d confirm`\nReject: `/approval reject %d <reason>`",
			ownerName,
			draftRole,
			item.Event.MessageID,
			action.ID,
			decision.ReplyText,
			ownerAction,
			action.ID,
			action.ID,
		)
	}
	draftRole := "助手答复草稿"
	if delegated {
		draftRole = "代回复草稿"
	}
	return fmt.Sprintf(
		"%s，消息 %s 的%s正在等待你确认（审批 #%d），尚未发送：\n\n%s\n\n仍需你处理：%s\n\n批准：`/approval approve %d confirm`\n拒绝：`/approval reject %d <原因>`",
		ownerName,
		item.Event.MessageID,
		draftRole,
		action.ID,
		decision.ReplyText,
		ownerAction,
		action.ID,
		action.ID,
	)
}

func isDelegatedApprovalRelevance(relevance domain.Relevance) bool {
	return relevance == domain.RelevanceDirectMention ||
		relevance == domain.RelevancePrivateMessage
}

func (p larkToolContext) RecentMessages(
	ctx context.Context,
	request agenttools.LarkContextRequest,
) (agenttools.LarkContextResult, error) {
	messageContext, err := p.svc.GetMessageContext(ctx, serviceim.MessageContextRequest{
		Mode:      request.Mode,
		ChatID:    request.ChatID,
		MessageID: request.MessageID,
		Limit:     request.Limit,
	})
	if err != nil {
		return agenttools.LarkContextResult{}, err
	}
	return agenttools.LarkContextResult{
		Messages:  normalizeToolMessages(messageContext.Messages),
		Selection: messageContext.Selection,
	}, nil
}

func (p larkToolContext) SearchMessages(ctx context.Context, query string, chatIDs []string, limit int) ([]domain.NormalizedEvent, error) {
	result, err := p.svc.SearchMessages(ctx, serviceim.SearchMessagesRequest{
		Query:    query,
		ChatIDs:  chatIDs,
		PageSize: limit,
		ChatType: "all",
	})
	if err != nil {
		return nil, err
	}
	return normalizeToolMessages(result.Items), nil
}

func normalizeToolMessages(messages []serviceim.Message) []domain.NormalizedEvent {
	events := make([]domain.NormalizedEvent, 0, len(messages))
	for _, message := range messages {
		sum := sha256.Sum256([]byte(message.MessageID + "\x00" + message.Content))
		attachments := make([]domain.Attachment, 0, len(message.Attachments))
		for _, attachment := range message.Attachments {
			normalized := domain.Attachment{
				Type:             attachment.Type,
				Key:              attachment.Key,
				MediaType:        attachment.MediaType,
				Readable:         attachment.Readable,
				UnreadableReason: attachment.UnreadableReason,
			}
			if len(attachment.Data) > 0 && attachment.MediaType != "" {
				normalized.DataURL = "data:" + attachment.MediaType + ";base64," +
					base64.StdEncoding.EncodeToString(attachment.Data)
			}
			attachments = append(attachments, normalized)
		}
		events = append(events, domain.NormalizedEvent{
			Source:           domain.SourcePoll,
			MessageID:        message.MessageID,
			ChatID:           message.ChatID,
			ChatType:         message.ChatType,
			RootMessageID:    message.RootMessageID,
			ReplyToMessageID: message.ReplyToMessageID,
			ThreadID:         message.ThreadID,
			SenderID:         message.SenderOpenID,
			SenderName:       message.SenderDisplayName,
			SenderType:       message.SenderType,
			Content:          message.Content,
			Attachments:      attachments,
			Mentions:         message.Mentions,
			CreatedAt:        normalizeServiceMessageTime(message.CreateTime),
			UpdatedAt:        normalizeServiceMessageTime(message.UpdateTime),
			RawDigest:        fmt.Sprintf("sha256:%x", sum[:]),
		})
	}
	return events
}

func normalizeServiceMessageTime(raw string) time.Time {
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed
	}
	if millis, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(millis).UTC()
	}
	return time.Time{}
}

func (b *conversationBuilder) Build(item domain.WorkItem) (agentcontext.Bundle, error) {
	builder := b.base
	if len(item.ResolvedContext) > 0 {
		builder.Conversation = append(
			builder.Conversation,
			item.ResolvedContext...,
		)
		builder.ContextSelection = domain.ContextSelection{
			Mode:            domain.ContextModeAdjacent,
			AnchorMessageID: item.Event.MessageID,
			Reason:          "shared_delegated_context_snapshot",
		}
		for _, event := range item.ResolvedContext {
			if event.MessageID == item.Event.MessageID {
				item.Event = event
				break
			}
		}
	} else if b.includeLarkContext && b.svc != nil && item.Event.ChatID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var messages []serviceim.Message
		var selection domain.ContextSelection
		var err error
		if item.WorkKind == domain.WorkKindDirectMention {
			var semanticContext serviceim.SemanticReplyContext
			semanticContext, err = b.svc.GetSemanticReplyContext(
				ctx,
				serviceim.SemanticReplyContextRequest{
					ChatID:          item.Event.ChatID,
					TargetMessageID: item.Event.MessageID,
					Since:           item.Event.CreatedAt,
					MaxMessages:     100,
				},
			)
			messages = semanticContext.Messages
			selection = domain.ContextSelection{
				Mode:            domain.ContextModeAdjacent,
				AnchorMessageID: item.Event.MessageID,
				Incomplete:      semanticContext.Incomplete || semanticContext.Withdrawn,
				Reason:          "delegated_post_target_window",
			}
		} else {
			var messageContext serviceim.MessageContext
			messageContext, err = b.svc.GetMessageContext(ctx, serviceim.MessageContextRequest{
				ChatID:           item.Event.ChatID,
				MessageID:        item.Event.MessageID,
				RootMessageID:    item.Event.RootMessageID,
				ReplyToMessageID: item.Event.ReplyToMessageID,
				ThreadID:         item.Event.ThreadID,
				CreatedAt:        item.Event.CreatedAt,
				Limit:            30,
			})
			messages = messageContext.Messages
			selection = messageContext.Selection
		}
		if err != nil {
			builder.ContextSelection = domain.ContextSelection{
				Mode:             domain.ContextModeAdjacent,
				AnchorMessageID:  item.Event.MessageID,
				RootMessageID:    item.Event.RootMessageID,
				ReplyToMessageID: item.Event.ReplyToMessageID,
				Incomplete:       true,
				Reason:           "lark_context_unavailable",
			}
		} else {
			normalized := normalizeToolMessages(messages)
			builder.Conversation = append(builder.Conversation, normalized...)
			builder.ContextSelection = selection
			if item.WorkKind == domain.WorkKindDirectMention {
				for _, event := range normalized {
					if event.MessageID == item.Event.MessageID {
						item.Event = event
						break
					}
				}
			}
		}
	}
	if err := b.resolveGitHubReference(&builder, item.Event); err != nil {
		return agentcontext.Bundle{}, err
	}
	resolveBuilderUser(&builder, item.Event)
	return builder.Build(item)
}

func (b *conversationBuilder) resolveGitHubReference(
	builder *agentcontext.Builder,
	event domain.NormalizedEvent,
) error {
	if !b.githubEnabled {
		return nil
	}
	verified, ok, err := agentcontext.ResolveGitHubReference(
		event,
		builder.Conversation,
		b.currentAppID,
		b.allowedRepositories,
		b.referenceSigningKey,
	)
	if err != nil {
		return err
	}
	if !ok {
		return b.applyStoredGitHubReference(builder, event)
	}
	if b.store != nil {
		verified, err = b.store.UpsertExternalReference(context.Background(), verified)
		if err != nil {
			return err
		}
	}
	ref := verified.Reference
	builder.GitHubReference = &ref
	return nil
}

func resolveBuilderUser(builder *agentcontext.Builder, event domain.NormalizedEvent) {
	if strings.TrimSpace(builder.User.Name) == "" {
		for _, mention := range event.Mentions {
			if mention.OpenID == builder.User.OpenID && strings.TrimSpace(mention.Name) != "" {
				builder.User.Name = strings.TrimSpace(mention.Name)
				break
			}
		}
	}
	samples := make([]string, 0, len(builder.Conversation)+1)
	samples = append(samples, event.Content)
	for _, message := range builder.Conversation {
		samples = append(samples, message.Content)
	}
	builder.User.Language = string(agentlocale.Resolve(
		agentlocale.Language(builder.User.PreferredLanguage),
		agentlocale.Language(builder.User.FallbackLanguage),
		samples...,
	))
}

func (b *conversationBuilder) applyStoredGitHubReference(
	builder *agentcontext.Builder,
	event domain.NormalizedEvent,
) error {
	if !b.githubEnabled || b.store == nil {
		return nil
	}
	var selected *domain.GitHubReference
	for _, messageID := range []string{event.ReplyToMessageID, event.RootMessageID} {
		if messageID == "" {
			continue
		}
		stored, ok, err := b.store.GetExternalReference(context.Background(), "github", messageID)
		if err != nil {
			return err
		}
		if !ok || stored.ChatID != event.ChatID || stored.SenderAppID != b.currentAppID ||
			!repositoryAllowed(stored.Reference.Repository, b.allowedRepositories) {
			continue
		}
		if selected != nil && *selected != stored.Reference {
			return errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"conflicting stored GitHub references in reply chain",
			)
		}
		ref := stored.Reference
		selected = &ref
	}
	builder.GitHubReference = selected
	return nil
}

type liveThreadState struct {
	resolver      app.DelegatedReplyResolver
	confidenceMin float64
	mu            sync.Mutex
	resolutions   map[string]replymatch.Resolution
}

func (s *liveThreadState) OwnerAlreadyReplied(
	ctx context.Context,
	item domain.WorkItem,
) (bool, error) {
	if s == nil || s.resolver == nil || item.WorkKind != domain.WorkKindDirectMention {
		return false, nil
	}
	key := semanticThreadStateKey(item)
	s.mu.Lock()
	resolution, ok := s.resolutions[key]
	if ok {
		delete(s.resolutions, key)
	}
	s.mu.Unlock()
	if !ok {
		var err error
		resolution, err = s.resolve(ctx, item)
		if err != nil {
			return false, err
		}
	}
	return resolution.Result == replymatch.ResultAnswered ||
		resolution.Result == replymatch.ResultNoReplyNeeded, nil
}

func (s *liveThreadState) MessageWithdrawn(
	ctx context.Context,
	item domain.WorkItem,
) (bool, error) {
	if s == nil || s.resolver == nil || item.WorkKind != domain.WorkKindDirectMention {
		return false, nil
	}
	resolution, err := s.resolve(ctx, item)
	if err != nil {
		return false, err
	}
	if resolution.Result == replymatch.ResultWithdrawn {
		return true, nil
	}
	key := semanticThreadStateKey(item)
	s.mu.Lock()
	s.resolutions[key] = resolution
	s.mu.Unlock()
	return false, nil
}

func (s *liveThreadState) resolve(
	ctx context.Context,
	item domain.WorkItem,
) (replymatch.Resolution, error) {
	resolution, err := s.resolver.Resolve(ctx, item)
	if err != nil {
		return replymatch.Resolution{}, err
	}
	if resolution.Confidence < s.confidenceMin ||
		resolution.Result == replymatch.ResultAmbiguous {
		return replymatch.Resolution{}, errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"final semantic owner-reply state is ambiguous",
		)
	}
	return resolution, nil
}

func semanticThreadStateKey(item domain.WorkItem) string {
	if item.ID != 0 {
		return strconv.FormatInt(item.ID, 10)
	}
	return item.DedupKey
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func resolveConfiguredLanguage(owner config.OwnerConfig) agentlocale.Language {
	if owner.PreferredLanguage == agentlocale.LanguageChinese ||
		owner.PreferredLanguage == agentlocale.LanguageEnglish {
		return owner.PreferredLanguage
	}
	if owner.FallbackLanguage == agentlocale.LanguageEnglish {
		return agentlocale.LanguageEnglish
	}
	return agentlocale.LanguageChinese
}

func newModeCommand(out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mode auto|approval|paused",
		Short: "Switch agent mode",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := domain.ParseMode(args[0])
			if err != nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid mode: %s", args[0]).
					WithParam("mode").
					WithHint("valid modes: auto, approval, paused").
					WithCause(err)
			}
			path := resolveConfigPath(*configPath)
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			cfg.Policy.Mode = mode
			if err := config.Save(path, cfg); err != nil {
				return err
			}
			return writeData(out, map[string]any{"mode": mode})
		},
	}
	return cmd
}

func newQueueCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Inspect or repair the durable queue",
		Long: "Inspect or explicitly repair the durable queue. Ready startup " +
			"automatically re-evaluates safe interrupted work, while uncertain " +
			"external actions remain terminal and are never replayed.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List queued work items",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // diagnostic command
			items, err := store.ListWorkItems()
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"items": items})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "summary",
		Short: "Summarize queue lanes and stale work",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // diagnostic command
			summary, err := store.QueueSummary()
			if err != nil {
				return err
			}
			return writeData(out, summary)
		},
	})
	cmd.AddCommand(newQueueInspectCommand(out, statePath))
	cmd.AddCommand(newQueueResumeCommand(out, statePath))
	cmd.AddCommand(newQueueBackfillCommand(out, configPath, statePath))
	cmd.AddCommand(newQueueRetryCommand(out, statePath))
	cmd.AddCommand(newQueueExportCommand(out, statePath))
	cmd.AddCommand(newQueueCancelCommand(out, statePath))
	cmd.AddCommand(newQueueTasksCommand(out, statePath))
	cmd.AddCommand(newQueueAcknowledgeCommand(out, statePath))
	cmd.AddCommand(newQueueReconcileCommand(out, statePath))
	return cmd
}

func newQueueTasksCommand(out io.Writer, statePath *string) *cobra.Command {
	var view string
	var page int
	var pageSize int
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List bounded owner-action task views",
		Long: "List the same bounded action, running, recent, or all task view " +
			"used by the owner-private Lark control commands.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // diagnostic command
			result, err := store.ListOwnerTasks(cmd.Context(), domain.OwnerTaskQuery{
				View:     domain.OwnerTaskView(view),
				Page:     page,
				PageSize: pageSize,
			})
			if err != nil {
				return err
			}
			return writeData(out, result)
		},
	}
	cmd.Flags().StringVar(
		&view,
		"view",
		string(domain.OwnerTaskViewAction),
		"task view: action, running, recent, or all",
	)
	cmd.Flags().IntVar(&page, "page", 1, "one-based result page")
	cmd.Flags().IntVar(&pageSize, "page-size", 10, "result count per page (maximum 20)")
	return cmd
}

func newQueueAcknowledgeCommand(out io.Writer, statePath *string) *cobra.Command {
	var workItemID int64
	var reason string
	cmd := &cobra.Command{
		Use:   "acknowledge",
		Short: "Acknowledge and close one interrupted or stopped task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workItemID <= 0 {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue acknowledge requires --work-id",
				).WithParam("--work-id")
			}
			if strings.TrimSpace(reason) == "" {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue acknowledge requires --reason",
				).WithParam("--reason")
			}
			return executeQueueOwnerMutation(
				cmd.Context(),
				out,
				statePath,
				domain.OwnerControlCommand{
					Name:       domain.OwnerControlTaskAcknowledge,
					WorkItemID: workItemID,
					Reason:     reason,
				},
			)
		},
	}
	cmd.Flags().Int64Var(&workItemID, "work-id", 0, "exact durable work item ID")
	cmd.Flags().StringVar(&reason, "reason", "", "auditable acknowledgement note")
	return cmd
}

func newQueueReconcileCommand(out io.Writer, statePath *string) *cobra.Command {
	var workItemID int64
	var result string
	var reason string
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Record the verified result of one uncertain external action",
		Long: "Record completed, not-completed, or unknown after manual " +
			"verification. The uncertain action is never replayed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workItemID <= 0 {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue reconcile requires --work-id",
				).WithParam("--work-id")
			}
			if strings.TrimSpace(reason) == "" {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue reconcile requires --reason",
				).WithParam("--reason")
			}
			disposition := domain.OwnerResolutionDisposition(
				strings.ReplaceAll(strings.TrimSpace(result), "-", "_"),
			)
			switch disposition {
			case domain.OwnerResolutionCompleted,
				domain.OwnerResolutionNotCompleted,
				domain.OwnerResolutionUnknown:
			default:
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue reconcile --result must be completed, not-completed, or unknown",
				).WithParam("--result")
			}
			return executeQueueOwnerMutation(
				cmd.Context(),
				out,
				statePath,
				domain.OwnerControlCommand{
					Name:        domain.OwnerControlTaskReconcile,
					WorkItemID:  workItemID,
					Disposition: disposition,
					Reason:      reason,
				},
			)
		},
	}
	cmd.Flags().Int64Var(&workItemID, "work-id", 0, "exact durable work item ID")
	cmd.Flags().StringVar(
		&result,
		"result",
		"",
		"verified result: completed, not-completed, or unknown",
	)
	cmd.Flags().StringVar(&reason, "reason", "", "auditable verification note")
	return cmd
}

func executeQueueOwnerMutation(
	ctx context.Context,
	out io.Writer,
	statePath *string,
	command domain.OwnerControlCommand,
) error {
	store, err := storage.OpenInspection(resolveStatePath(*statePath))
	if err != nil {
		return err
	}
	defer store.Close() //nolint:errcheck // diagnostic command
	commandID := fmt.Sprintf(
		"cli:%s:%d:%d",
		command.Name,
		command.WorkItemID,
		time.Now().UTC().UnixNano(),
	)
	result, err := store.ExecuteOwnerMutation(ctx, commandID, command)
	if err != nil {
		return err
	}
	return writeData(out, result)
}

func newQueueCancelCommand(out io.Writer, statePath *string) *cobra.Command {
	var workItemIDs []int64
	var messageIDs []string
	var allInterrupted bool
	var keepWorkItemIDs []int64
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Durably cancel audited queue work",
		Long: "Durably cancel exact work/message IDs, or all interrupted work except " +
			"explicitly kept IDs. The command requires an audit reason, never deletes " +
			"history, never sends a Lark message, and fails atomically for running or " +
			"result-uncertain actions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue cancel requires --reason",
				).WithParam("--reason")
			}
			hasExact := len(workItemIDs) > 0 || len(messageIDs) > 0
			if allInterrupted == hasExact {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"queue cancel requires either exact ids or --all-interrupted",
				)
			}
			if !allInterrupted && len(keepWorkItemIDs) > 0 {
				return errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"--keep-work-id requires --all-interrupted",
				).WithParam("--keep-work-id")
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // bounded queue repair command
			result, err := store.CancelWork(cmd.Context(), domain.CancelWorkRequest{
				WorkItemIDs:     workItemIDs,
				MessageIDs:      messageIDs,
				AllInterrupted:  allInterrupted,
				KeepWorkItemIDs: keepWorkItemIDs,
				Reason:          reason,
			})
			if err != nil {
				return err
			}
			return writeData(out, result)
		},
	}
	cmd.Flags().Int64SliceVar(&workItemIDs, "work-id", nil, "exact durable work item id; repeat for multiple items")
	cmd.Flags().StringSliceVar(&messageIDs, "message-id", nil, "exact Lark message id; repeat for multiple messages")
	cmd.Flags().BoolVar(&allInterrupted, "all-interrupted", false, "cancel every interrupted item except kept work ids")
	cmd.Flags().Int64SliceVar(&keepWorkItemIDs, "keep-work-id", nil, "interrupted work id to keep; requires --all-interrupted")
	cmd.Flags().StringVar(&reason, "reason", "", "required operator audit reason")
	return cmd
}

func newQueueInspectCommand(out io.Writer, statePath *string) *cobra.Command {
	var workItemID int64
	var messageID string
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect one exact message or durable work item",
		Long: "Inspect the intake receipt, latest durable stage, action, and " +
			"interruption state for one exact message. Inspection never replays work.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // bounded diagnostic command
			inspection, err := store.InspectWork(cmd.Context(), domain.WorkInspectionQuery{
				WorkItemID: workItemID,
				MessageID:  strings.TrimSpace(messageID),
			})
			if err != nil {
				return err
			}
			return writeData(out, inspection)
		},
	}
	cmd.Flags().Int64Var(&workItemID, "work-id", 0, "exact durable work item id")
	cmd.Flags().StringVar(&messageID, "message-id", "", "exact Lark message id")
	return cmd
}

func newQueueResumeCommand(out io.Writer, statePath *string) *cobra.Command {
	var workItemID int64
	var messageID string
	var forceTerminal bool
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Explicitly resume one reviewed paused, terminal, or offline message",
		Long: "Explicitly admit one exact offline message or interrupted work item. " +
			"Safe cross-restart work is already re-evaluated automatically. " +
			"Result-uncertain external actions are never replayed automatically. " +
			"Completed, ignored, cancelled, or dead-letter work additionally requires --force-terminal. " +
			"结果不确定的外部动作不会自动重放；仅在核对外部结果后，才可显式恢复终态工作。",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // bounded queue repair command
			inspection, err := store.ResumeWork(cmd.Context(), domain.ResumeWorkRequest{
				WorkItemID:    workItemID,
				MessageID:     strings.TrimSpace(messageID),
				ForceTerminal: forceTerminal,
			})
			if err != nil {
				return err
			}
			return writeData(out, inspection)
		},
	}
	cmd.Flags().Int64Var(&workItemID, "work-id", 0, "exact durable work item id")
	cmd.Flags().StringVar(&messageID, "message-id", "", "exact Lark message id")
	cmd.Flags().BoolVar(&forceTerminal, "force-terminal", false, "allow explicit replay of terminal work")
	return cmd
}

func newQueueBackfillCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	var chatQuery string
	var chatIDs []string
	var since string
	var until string
	var includePrivate bool
	var pageSize int
	cmd := &cobra.Command{
		Use:   "backfill --chat-query QUERY --since TIME [--until TIME]",
		Short: "Explicitly backfill missed @Owner messages",
		Long: "Explicitly search a bounded Lark time range for messages that @Owner, " +
			"then record matching messages into the durable queue. This recovers messages " +
			"that were never captured while user-token polling was unavailable. It never " +
			"advances the normal poll cursor and never scans history without --since.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(since) == "" {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "queue backfill requires --since").
					WithParam("--since")
			}
			if strings.TrimSpace(chatQuery) == "" && len(chatIDs) == 0 {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "queue backfill requires --chat-query or --chat-id").
					WithParam("--chat-query")
			}
			now := time.Now()
			start, err := parseBackfillTime(since, now)
			if err != nil {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid --since: %s", since).
					WithParam("--since").
					WithCause(err)
			}
			end := now.UTC()
			if strings.TrimSpace(until) != "" {
				end, err = parseBackfillTime(until, now)
				if err != nil {
					return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid --until: %s", until).
						WithParam("--until").
						WithCause(err)
				}
			}
			if pageSize <= 0 {
				pageSize = 20
			}
			if pageSize > 50 {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "--page-size must be at most 50").
					WithParam("--page-size")
			}
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			credentials, err := serviceim.LoadCredentials(cmd.Context(), credentialRefs(cfg))
			if err != nil {
				return err
			}
			if strings.TrimSpace(credentials.UserAccessToken) == "" {
				return errs.NewConfigError(errs.SubtypeNotConfigured, "lark user access token is not configured").
					WithHint("run `lark-agent auth login` before queue backfill")
			}
			client, err := serviceim.NewClient(serviceim.ClientConfig{
				AppID:           cfg.Lark.AppID,
				AppSecret:       credentials.AppSecret,
				UserAccessToken: credentials.UserAccessToken,
				RefreshToken:    credentials.RefreshToken,
				UserTokenStore:  serviceim.NewKeychainUserTokenStore(credentialRefs(cfg)),
				BaseURL:         cfg.Lark.BaseURL,
				Timeout:         30 * time.Second,
			})
			if err != nil {
				return err
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // bounded queue repair command
			agentRouter := newAgentRouter(cfg, store)
			poller := newConfiguredLivePoller(
				serviceim.NewService(client, cfg.Owner.OpenID),
				store,
				agentRouter,
				cfg,
				chatQuery,
				nil,
				includePrivate,
			)
			result, err := poller.Backfill(cmd.Context(), poll.BackfillRequest{
				ChatQuery: chatQuery,
				ChatIDs:   chatIDs,
				Start:     start,
				End:       end,
				PageSize:  pageSize,
			})
			if err != nil {
				return err
			}
			return writeData(out, result)
		},
	}
	cmd.Flags().StringVar(&chatQuery, "chat-query", "", "visible chat search keyword, for example Example Group")
	cmd.Flags().StringArrayVar(&chatIDs, "chat-id", nil, "exact Lark chat id; may be passed more than once")
	cmd.Flags().StringVar(&since, "since", "", "required start time, RFC3339 or lookback duration such as 8h")
	cmd.Flags().StringVar(&until, "until", "", "optional end time, RFC3339 or lookback duration; defaults to now")
	cmd.Flags().BoolVar(&includePrivate, "include-private", false, "also allow private chats when searching")
	cmd.Flags().IntVar(&pageSize, "page-size", 20, "Lark message search page size, max 50")
	return cmd
}

func parseBackfillTime(raw string, now time.Time) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("time value is required")
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration < 0 {
			return time.Time{}, fmt.Errorf("duration must be positive")
		}
		return now.Add(-duration).UTC(), nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func newQueueExportCommand(out io.Writer, statePath *string) *cobra.Command {
	var runID string
	cmd := &cobra.Command{
		Use:   "export --run-id RUN_ID",
		Short: "Export one agent run replay transcript",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(runID) == "" {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "queue export requires --run-id").WithParam("--run-id")
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // diagnostic export command
			jsonl, err := store.ExportAgentRunTranscript(runID)
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"run_id": runID, "format": "jsonl", "transcript": jsonl})
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "agent run id to export")
	return cmd
}

func newQueueRetryCommand(out io.Writer, statePath *string) *cobra.Command {
	var rawIDs []string
	var all bool
	cmd := &cobra.Command{
		Use:   "retry",
		Short: "Retry current-session transient failures",
		Long: "Retry only ordinary retry_wait work owned by the active session. " +
			"Prior-session, processing, interrupted, terminal, or uncertain-action work must use queue inspect and queue resume.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(rawIDs) == 0 && !all {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "queue retry requires --id or --all").
					WithParam("--id").
					WithHint("Use --id <work-item-id> to retry one eligible item, or --all for all eligible active-session retry_wait items.")
			}
			if len(rawIDs) > 0 && all {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "queue retry cannot combine --id and --all").
					WithParam("--all")
			}
			ids := make([]int64, 0, len(rawIDs))
			for _, raw := range rawIDs {
				id, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || id <= 0 {
					return errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid work item id: %s", raw).
						WithParam("--id").
						WithCause(err)
				}
				ids = append(ids, id)
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // diagnostic repair command
			changed, err := store.RetryWorkItems(ids)
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": "retry", "changed": changed})
		},
	}
	cmd.Flags().StringArrayVar(&rawIDs, "id", nil, "eligible active-session retry_wait item; may be passed more than once")
	cmd.Flags().BoolVar(&all, "all", false, "retry all eligible active-session retry_wait items")
	return cmd
}

func newApprovalCommand(out io.Writer, statePath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "approval", Short: "Inspect and decide pending side effects"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List approval and action records",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			actions, err := store.ListActionAttempts()
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"actions": actions})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Summarize action records by status",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			actions, err := store.ListActionAttempts()
			if err != nil {
				return err
			}
			counts := map[domain.ActionStatus]int{}
			for _, action := range actions {
				counts[action.Status]++
			}
			return writeData(out, map[string]any{"counts": counts, "total": len(actions)})
		},
	})
	cmd.AddCommand(newApprovalShowCommand(out, statePath))
	cmd.AddCommand(newApprovalDecisionCommand(out, statePath, true))
	cmd.AddCommand(newApprovalDecisionCommand(out, statePath, false))
	return cmd
}

func newSubscriptionCommand(out io.Writer, statePath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "subscription", Short: "Manage document and Base monitoring subscriptions"}
	cmd.AddCommand(&cobra.Command{
		Use:   "add URL",
		Short: "Add a Wiki, document, or Base resource subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := serviceim.ParseResourceURL(args[0])
			if err != nil {
				return err
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			modes := []string{"document_comment", "cloud_docs_notice"}
			if ref.ResourceType == serviceim.ResourceTypeBase {
				modes = []string{"base_record", "base_field", "cloud_docs_notice"}
			}
			sub, err := store.UpsertResourceSubscription(cmd.Context(), domain.ResourceSubscription{
				OriginalURL:   ref.OriginalURL,
				ResourceType:  string(ref.ResourceType),
				FileToken:     ref.FileToken,
				AppToken:      ref.AppToken,
				WikiNodeToken: ref.WikiNodeToken,
				TableID:       ref.TableID,
				ViewID:        ref.ViewID,
				MonitorModes:  modes,
				Status:        domain.ResourceSubscriptionPending,
			})
			if err != nil {
				return err
			}
			return writeData(out, sub)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured resource subscriptions",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			subs, err := store.ListResourceSubscriptions(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"subscriptions": subs})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect URL",
		Short: "Inspect one resource subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			sub, err := store.GetResourceSubscription(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeData(out, sub)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove URL",
		Short: "Mark one resource subscription removed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			sub, err := store.RemoveResourceSubscription(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeData(out, sub)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "sync",
		Short: "Report local resource subscription sync state",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			subs, err := store.ListResourceSubscriptions(cmd.Context())
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{
				"subscriptions": subs,
				"remote_scope":  "base subscriptions are app/file scoped; table filtering is local and view is context only",
			})
		},
	})
	return cmd
}

func newApprovalShowCommand(out io.Writer, statePath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show ID",
		Short: "Show one pending or completed action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseApprovalID(args[0])
			if err != nil {
				return err
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			action, err := store.GetActionAttempt(id)
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": action})
		},
	}
}

func newApprovalDecisionCommand(out io.Writer, statePath *string, approve bool) *cobra.Command {
	name := "reject"
	if approve {
		name = "approve"
	}
	return &cobra.Command{
		Use:   name + " ID",
		Short: name + " one pending exact action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseApprovalID(args[0])
			if err != nil {
				return err
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck
			if err := store.DecideAction(id, approve); err != nil {
				return err
			}
			return writeData(out, map[string]any{"action": name, "id": id})
		},
	}
}

func parseApprovalID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errs.NewValidationError(errs.SubtypeInvalidArgument, "approval action ID must be a positive integer").
			WithParam("ID").
			WithCause(err)
	}
	if id <= 0 {
		return 0, errs.NewValidationError(errs.SubtypeInvalidArgument, "approval action ID must be a positive integer").
			WithParam("ID")
	}
	return id, nil
}

func newMemoryCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Inspect or delete explicit memory"}
	for _, name := range []string{"list", "delete"} {
		n := name
		cmd.AddCommand(&cobra.Command{
			Use:   n,
			Short: n + " memory",
			RunE: func(cmd *cobra.Command, args []string) error {
				return writeData(out, map[string]any{"action": n, "items": []any{}})
			},
		})
	}
	return cmd
}

func newRulesCommand(out io.Writer, configPath *string) *cobra.Command {
	cmd := &cobra.Command{Use: "rules", Short: "Explain loaded workspace rules"}
	cmd.AddCommand(&cobra.Command{
		Use:   "explain",
		Short: "List rule files inside the workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			scope, err := workspace.NewScopeWithExcludes(cfg.Workspace.Root, cfg.Workspace.Excludes)
			if err != nil {
				return err
			}
			refs, err := scope.WalkRules()
			if err != nil {
				return err
			}
			return writeData(out, map[string]any{"rules": refs})
		},
	})
	return cmd
}

func newDoctorCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	var larkOnly bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Lark SDK and local agent readiness",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			larkStatus, err := checkLarkSDK(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if larkOnly {
				return writeData(out, map[string]any{"ok": true, "lark": larkStatus})
			}
			scope, err := workspace.NewScopeWithExcludes(cfg.Workspace.Root, cfg.Workspace.Excludes)
			if err != nil {
				return err
			}
			store, err := storage.OpenInspection(resolveStatePath(*statePath))
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck // diagnostic command
			queueSummary, err := store.QueueSummary()
			if err != nil {
				return err
			}
			githubToken, githubTokenErr := secretstore.Read(
				cmd.Context(),
				cfg.GitHub.TokenKeychainService,
				cfg.GitHub.TokenKeychainKey,
				"GITHUB_TOKEN",
			)
			return writeData(out, map[string]any{
				"ok":        true,
				"lark":      larkStatus,
				"workspace": scope.Snapshot(),
				"mode":      cfg.Policy.Mode,
				"github": map[string]any{
					"enabled":              cfg.GitHub.Enabled,
					"read_only":            true,
					"single_lark_listener": true,
					"api_base_url":         cfg.GitHub.APIBaseURL,
					"allowed_repositories": cfg.GitHub.AllowedRepositories,
					"token_configured":     githubTokenErr == nil && githubToken != "",
					"token_error":          errorString(githubTokenErr),
				},
				"reply_scopes": map[string]any{
					"assistant_mentions": cfg.Assistant.ReplyScope,
					"owner_mentions":     cfg.Policy.ReplyScope,
					"private_messages":   cfg.Policy.PrivateReplyScope,
				},
				"delegated_reply": map[string]any{
					"owner_wait":            cfg.Policy.OwnerWait.String(),
					"owner_reply_threshold": cfg.Policy.OwnerReplyConfidenceMin,
					"ambiguous_retry":       cfg.Policy.OwnerReplyRetry.String(),
				},
				"reply_scope": cfg.Policy.ReplyScope,
				"scheduler": map[string]any{
					"fast_path_enabled":     cfg.FastPath.Enabled,
					"foreground_workers":    cfg.Scheduler.ForegroundWorkers,
					"background_workers":    cfg.Scheduler.BackgroundWorkers,
					"duplicate_window":      cfg.Scheduler.DuplicateWindow.String(),
					"fast_path_lease":       cfg.Scheduler.FastPathLease.String(),
					"coding_question_lease": cfg.Scheduler.CodingQuestionLease.String(),
				},
				"queue": queueSummary,
				"assistant": map[string]any{
					"owner_direct_enabled": cfg.Assistant.OwnerDirect.Enabled,
					"names":                cfg.Assistant.Names,
					"open_ids_configured":  len(cfg.Assistant.OpenIDs),
				},
				"coding": map[string]any{
					"enabled":             cfg.Coding.Enabled,
					"code_index":          "fallback",
					"max_evidence_files":  cfg.Coding.MaxEvidenceFiles,
					"require_source_refs": cfg.Coding.RequireSourceRefs,
					"plan_tool":           "submit_investigation_plan",
					"verify_gate":         true,
					"transcript_export":   "queue export --run-id",
				},
			})
		},
	}
	cmd.Flags().BoolVar(&larkOnly, "lark-only", false, "check only Lark SDK credentials and permissions")
	return cmd
}

func checkLarkSDK(ctx context.Context, cfg config.Config) (map[string]any, error) {
	credentials, err := serviceim.LoadCredentials(ctx, credentialRefs(cfg))
	if err != nil {
		return nil, err
	}
	client, err := serviceim.NewClient(serviceim.ClientConfig{
		AppID:           cfg.Lark.AppID,
		AppSecret:       credentials.AppSecret,
		UserAccessToken: credentials.UserAccessToken,
		RefreshToken:    credentials.RefreshToken,
		UserTokenStore:  serviceim.NewKeychainUserTokenStore(credentialRefs(cfg)),
		BaseURL:         cfg.Lark.BaseURL,
		Timeout:         15 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	status := map[string]any{
		"boundary":          "official_go_sdk",
		"app_id_configured": cfg.Lark.AppID != "",
		"keychain_service":  cfg.Lark.KeychainService,
	}
	if credentials.UserAccessToken != "" {
		status["user_token"] = "configured"
	} else {
		status["user_token"] = "missing"
	}
	if cfg.Assistant.OwnerDirect.Enabled && os.Getenv("LARK_AGENT_REMOTE_DOCTOR") == "1" {
		if _, err := serviceim.CheckPublishedApp(ctx, client, cfg.Lark.AppID); err != nil {
			return nil, err
		}
		status["realtime"] = "sdk_websocket_ready"
	} else if cfg.Assistant.OwnerDirect.Enabled {
		status["realtime"] = "sdk_websocket_not_checked"
	}
	return status, nil
}

func credentialRefs(cfg config.Config) serviceim.CredentialRefs {
	return serviceim.CredentialRefs{
		Service:             cfg.Lark.KeychainService,
		AppSecretAccount:    cfg.Lark.AppSecretKeychainKey,
		UserTokenAccount:    cfg.Lark.UserTokenKeychainKey,
		RefreshTokenAccount: cfg.Lark.RefreshTokenKeychainKey,
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func resolveConfigPath(path string) string {
	if path != "" {
		return path
	}
	home, err := vfs.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config.DefaultPaths(home).ConfigDir, "config.yaml")
}

func resolveStatePath(path string) string {
	if path != "" {
		return path
	}
	home, err := vfs.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config.DefaultPaths(home).StateDir, "state.db")
}

func writeData(w io.Writer, data any) error {
	return json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
}

func writeError(w io.Writer, err error) {
	typed, ok := errs.UnwrapTypedError(err)
	if !ok {
		typed = errs.NewInternalError(errs.SubtypeUnknown, "%s", err).WithCause(err)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": typed})
}

func exitCodeOf(err error) int {
	problem, ok := errs.ProblemOf(err)
	if !ok {
		return 1
	}
	switch problem.Category {
	case errs.CategoryValidation, errs.CategoryConfig, errs.CategoryAuthorization:
		return 2
	default:
		return 1
	}
}

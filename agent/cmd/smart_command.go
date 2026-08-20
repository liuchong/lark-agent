package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/liuchong/lark-agent/agent/config"
	"github.com/liuchong/lark-agent/agent/smartcmd"
)

func newRunCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	var promptFile, message, rulesFile, allowedActions, chatID, outputLanguage string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run one smart command and exit",
		Long: "Start the agent main loop as a smart command: no Lark WebSocket, HTTP-only Lark when an allowlisted tool needs it, then exit.\n\n" +
			"Provide --prompt-file and/or --message. --allowed-actions is a comma-separated write allowlist. " +
			"--dry-run keeps read tools and denies every write.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			result, err := smartcmd.Run(cmd.Context(), smartcmd.Options{
				Config:         cfg,
				StatePath:      *statePath,
				GitHub:         false,
				PromptFile:     promptFile,
				Message:        message,
				RulesFile:      rulesFile,
				AllowedActions: allowedActions,
				ChatID:         firstNonEmpty(chatID, os.Getenv("LARK_CHAT_ID")),
				DryRun:         dryRun,
				WorkspaceRoot:  firstNonEmpty(os.Getenv("GITHUB_WORKSPACE"), cfg.Workspace.Root),
				OutputLanguage: outputLanguage,
			})
			if err != nil {
				return err
			}
			return writeData(out, result)
		},
	}
	bindSmartCommandFlags(cmd, &promptFile, &message, &rulesFile, &allowedActions, &chatID, &outputLanguage, &dryRun)
	return cmd
}

func newGitHubRunCommand(out io.Writer, configPath, statePath *string) *cobra.Command {
	var promptFile, message, rulesFile, allowedActions, chatID, outputLanguage string
	var eventPath, eventName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Load built-in GitHub support and run one smart command",
		Long: "Read GITHUB_EVENT_PATH and GITHUB_EVENT_NAME, bind GitHub HTTP to the parsed event, then start a smart command. " +
			"This command never starts a Lark WebSocket.\n\n" +
			"Provide --prompt-file and/or --message. --allowed-actions is a comma-separated write allowlist. " +
			"--dry-run keeps read tools and denies every write.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadGitHubNotifyConfig(resolveConfigPath(*configPath))
			if err != nil {
				return err
			}
			result, err := smartcmd.Run(cmd.Context(), smartcmd.Options{
				Config:         cfg,
				StatePath:      *statePath,
				GitHub:         true,
				PromptFile:     promptFile,
				Message:        message,
				RulesFile:      rulesFile,
				AllowedActions: allowedActions,
				ChatID:         firstNonEmpty(chatID, os.Getenv("LARK_CHAT_ID")),
				DryRun:         dryRun,
				EventPath:      eventPath,
				EventName:      eventName,
				WorkspaceRoot:  firstNonEmpty(os.Getenv("GITHUB_WORKSPACE"), cfg.Workspace.Root),
				OutputLanguage: outputLanguage,
			})
			if err != nil {
				return err
			}
			return writeData(out, result)
		},
	}
	bindSmartCommandFlags(cmd, &promptFile, &message, &rulesFile, &allowedActions, &chatID, &outputLanguage, &dryRun)
	cmd.Flags().StringVar(&eventPath, "event-path", "", "typed GitHub event JSON path (default: GITHUB_EVENT_PATH)")
	cmd.Flags().StringVar(&eventName, "event-name", "", "GitHub event name (default: GITHUB_EVENT_NAME)")
	return cmd
}

func bindSmartCommandFlags(
	cmd *cobra.Command,
	promptFile, message, rulesFile, allowedActions, chatID, outputLanguage *string,
	dryRun *bool,
) {
	cmd.Flags().StringVar(promptFile, "prompt-file", "", "workspace-relative prompt file")
	cmd.Flags().StringVar(message, "message", "", "inline user message")
	cmd.Flags().StringVar(rulesFile, "rules-file", "", "workspace-relative extra rules file")
	cmd.Flags().StringVar(allowedActions, "allowed-actions", "", "comma-separated write tools")
	cmd.Flags().StringVar(chatID, "chat-id", "", "exact destination Lark chat ID (default: LARK_CHAT_ID)")
	cmd.Flags().StringVar(outputLanguage, "output-language", "",
		"outward language auto|zh-CN|en-US (default: LARK_AGENT_OUTPUT_LANGUAGE, then output.language)")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "deny every write tool")
}

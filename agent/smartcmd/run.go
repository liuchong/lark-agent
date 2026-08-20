package smartcmd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"

	"github.com/liuchong/lark-agent/agent/config"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	"github.com/liuchong/lark-agent/agent/storage"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
	"github.com/liuchong/lark-agent/internal/secretstore"
)

const smartCommandIdentity = "smart-command"

type Options struct {
	Config         config.Config
	StatePath      string
	GitHub         bool
	PromptFile     string
	Message        string
	RulesFile      string
	AllowedActions string
	ChatID         string
	DryRun         bool
	EventPath      string
	EventName      string
	WorkspaceRoot  string
	OutputLanguage string

	Model             einomodel.BaseChatModel
	TerminalFinalizer einomodel.BaseChatModel
	GitHubClient      *internalgithub.Client
	LarkService       *serviceim.Service
	AppSecret         string
}

type Result struct {
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
	Reference      any               `json:"reference"`
}

func Run(ctx context.Context, opts Options) (Result, error) {
	result := Result{
		Mode:           "run",
		DryRun:         opts.DryRun,
		AllowedActions: []string{},
		Outputs:        map[string]string{},
		Reference:      map[string]any{},
	}
	language, err := resolveOutputLanguage(opts)
	if err != nil {
		return Result{}, err
	}
	result.OutputLanguage = string(language)

	workspaceRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = firstNonEmpty(os.Getenv("GITHUB_WORKSPACE"), opts.Config.Workspace.Root)
	}
	if workspaceRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Result{}, err
		}
		workspaceRoot = cwd
	}

	if opts.GitHub && !opts.Config.GitHub.Enabled {
		return Result{}, errs.NewConfigError(
			errs.SubtypeFailedPrecondition,
			"github bridge is disabled",
		).WithField("github.enabled")
	}

	model := opts.Model
	if model == nil {
		adapter, err := resolveRoleModel(ctx, opts.Config, opts.Config.Model.Roles.Agent)
		if err != nil {
			return Result{}, err
		}
		model = adapter
	}
	finalizer := opts.TerminalFinalizer
	if finalizer == nil {
		adapter, err := resolveRoleModel(ctx, opts.Config, opts.Config.Model.Roles.Finalizer)
		if err != nil {
			return Result{}, err
		}
		finalizer = adapter
	}

	cliActions, err := internalgithub.ParseAllowedActions(opts.AllowedActions)
	if err != nil {
		return Result{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "%s", err.Error())
	}

	var (
		snapshot  internalgithub.Snapshot
		mention   internalgithub.Mention
		eventName string
	)
	if opts.GitHub {
		path := firstNonEmpty(opts.EventPath, os.Getenv("GITHUB_EVENT_PATH"))
		eventName = firstNonEmpty(opts.EventName, os.Getenv("GITHUB_EVENT_NAME"))
		if path == "" || eventName == "" {
			return Result{}, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"GITHUB_EVENT_PATH and GITHUB_EVENT_NAME are required",
			)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Result{}, errs.NewInternalError(errs.SubtypeFileIO, "read GitHub event path").WithCause(err)
		}
		snapshot, err = internalgithub.ParseEvent(eventName, data)
		if err != nil {
			return Result{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "%s", err.Error())
		}
		if snapshot.Reference.Kind == internalgithub.ReferenceWorkflowDispatch &&
			snapshot.Reference.PullRequestNumber == 0 {
			if runID, parseErr := strconv.ParseInt(strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")), 10, 64); parseErr == nil && runID > 0 {
				snapshot.Reference.WorkflowRunID = runID
			}
		}
		if !repositoryAllowed(snapshot.Reference.Repository, opts.Config.GitHub.AllowedRepositories) {
			return Result{}, errs.NewPermissionError(
				errs.SubtypeFailedPrecondition,
				"github repository is not allowed: %s",
				snapshot.Reference.Repository,
			)
		}
		result.EventName = eventName
		result.Repository = snapshot.Reference.Repository
		result.Reference = snapshot.Reference
		if eventName == "issue_comment" || eventName == "pull_request_review_comment" {
			mention = internalgithub.ParseMention(snapshot.CommentBody)
			if mention.FlagError != "" {
				return Result{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "%s", mention.FlagError)
			}
			if !mention.Matched {
				result.Skipped = true
				result.AllowedActions = []string{}
				return result, nil
			}
			result.Command = mention.Command
			opts.DryRun = opts.DryRun || mention.DryRun
			result.DryRun = opts.DryRun
		}
	}

	hasPR := snapshot.Reference.PullRequestNumber > 0
	effective := internalgithub.EffectiveAllowlist(cliActions, mention.Command, hasPR, result.DryRun)
	result.AllowedActions = append([]string{}, effective...)

	if mention.UnknownCommand {
		help := agentlocale.UnknownSlashCommandHelp(language, mention.Command)
		if internalgithub.AllowlistContains(effective, internalgithub.ActionPostGitHubComment) {
			commentID, err := postHelpComment(ctx, opts, snapshot.Reference, help)
			if err != nil {
				return Result{}, err
			}
			result.CommentID = commentID
			return result, nil
		}
		return Result{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "%s", help)
	}
	if (mention.Command == "review" || mention.Command == "check") && !hasPR {
		if internalgithub.AllowlistContains(effective, internalgithub.ActionPostGitHubComment) {
			commentID, err := postHelpComment(
				ctx,
				opts,
				snapshot.Reference,
				agentlocale.NonPullRequestCommandHelp(language),
			)
			if err != nil {
				return Result{}, err
			}
			result.CommentID = commentID
			return result, nil
		}
		return result, nil
	}

	if internalgithub.AllowlistContains(effective, internalgithub.ActionSendLarkMessage) && !result.DryRun {
		if strings.TrimSpace(opts.ChatID) == "" {
			return Result{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "--chat-id is required").
				WithParam("--chat-id")
		}
	}

	userMessage, err := composePrompt(workspaceRoot, opts, mention)
	if err != nil {
		return Result{}, err
	}

	statePath := strings.TrimSpace(opts.StatePath)
	cleanupState := false
	if statePath == "" {
		file, err := os.CreateTemp("", "lark-agent-smart-command-*.db")
		if err != nil {
			return Result{}, errs.NewInternalError(errs.SubtypeStorage, "create smart command state").WithCause(err)
		}
		statePath = file.Name()
		_ = file.Close()
		cleanupState = true
	}
	store, err := storage.Open(statePath)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		_ = store.Close()
		if cleanupState {
			_ = os.Remove(statePath)
		}
	}()

	githubClient := opts.GitHubClient
	if opts.GitHub && githubClient == nil {
		token, tokenErr := secretstore.Read(
			ctx,
			opts.Config.GitHub.TokenKeychainService,
			opts.Config.GitHub.TokenKeychainKey,
			"GITHUB_TOKEN",
		)
		if tokenErr == nil && token != "" {
			githubClient, err = internalgithub.NewClient(internalgithub.ClientConfig{
				BaseURL: opts.Config.GitHub.APIBaseURL,
				Token:   token,
				Limits: internalgithub.Limits{
					MaxFiles:       opts.Config.GitHub.MaxFiles,
					MaxPatchBytes:  opts.Config.GitHub.MaxPatchBytes,
					MaxAnnotations: opts.Config.GitHub.MaxAnnotations,
					MaxReviews:     opts.Config.GitHub.MaxReviews,
				},
			})
			if err != nil {
				return Result{}, err
			}
		}
	}

	needCheck := mention.Command == "review" || mention.Command == "check" ||
		internalgithub.AllowlistContains(effective, internalgithub.ActionUpsertGitHubCheck)
	if opts.GitHub && snapshot.Reference.PullRequestNumber > 0 && snapshot.Reference.HeadSHA == "" {
		if githubClient == nil {
			if needCheck {
				return Result{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "pull request head_sha is required")
			}
			result.Partial = true
		} else {
			filled, fillErr := githubClient.GetPull(ctx, snapshot.Reference)
			if fillErr != nil {
				if needCheck {
					return Result{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "pull request head_sha is required").WithCause(fillErr)
				}
				result.Partial = true
			} else {
				snapshot.Reference = filled
				result.Reference = filled
			}
		}
	}

	gate := &agenttools.WriteGate{
		Allow:          allowMap(effective),
		Secrets:        secretValues(),
		Language:       language,
		JobOutputPath:  os.Getenv("GITHUB_OUTPUT"),
		ChatID:         strings.TrimSpace(opts.ChatID),
		AppSecret:      opts.AppSecret,
		Outputs:        map[string]string{},
		EncodeMarker:   internalgithub.EncodeReferenceMarker,
		IdempotencyKey: internalgithub.StableSmartCommandKey,
	}
	if opts.GitHub {
		ref := snapshot.Reference
		gate.Reference = &ref
	}

	larkService := opts.LarkService
	needLark := strings.TrimSpace(opts.ChatID) != "" ||
		internalgithub.AllowlistContains(effective, internalgithub.ActionSendLarkMessage)
	if needLark && larkService == nil {
		credentials, credErr := serviceim.LoadCredentials(ctx, credentialRefs(opts.Config))
		if credErr != nil {
			return Result{}, credErr
		}
		opts.AppSecret = credentials.AppSecret
		gate.AppSecret = credentials.AppSecret
		larkClient, clientErr := serviceim.NewClient(serviceim.ClientConfig{
			AppID:     opts.Config.Lark.AppID,
			AppSecret: credentials.AppSecret,
			BaseURL:   opts.Config.Lark.BaseURL,
			Timeout:   30 * time.Second,
		})
		if clientErr != nil {
			return Result{}, clientErr
		}
		larkService = serviceim.NewService(larkClient, opts.Config.Owner.OpenID)
	}

	definitions, err := catalog(opts, githubClient, larkService, gate, workspaceRoot)
	if err != nil {
		return Result{}, err
	}
	registry, err := agenttools.NewRegistry(definitions...)
	if err != nil {
		return Result{}, err
	}

	var githubRef *domain.GitHubReference
	var summary *agentcontext.GitHubEventSummary
	if opts.GitHub {
		ref := snapshot.Reference
		githubRef = &ref
		summary = &agentcontext.GitHubEventSummary{
			EventName:         eventName,
			Action:            snapshot.Action,
			Repository:        ref.Repository,
			Kind:              string(ref.Kind),
			IssueNumber:       ref.IssueNumber,
			PullRequestNumber: ref.PullRequestNumber,
			CommentID:         ref.CommentID,
			HeadSHA:           ref.HeadSHA,
			BeforeSHA:         ref.BeforeSHA,
			Ref:               ref.Ref,
			TagName:           ref.TagName,
			HTMLURL:           ref.HTMLURL,
			Title:             snapshot.Title,
			Command:           mention.Command,
			ExtraPrompt:       mention.ExtraPrompt,
			DryRun:            result.DryRun,
			AllowedActions:    append([]string{}, effective...),
		}
	}

	tools := make([]agentcontext.ToolSpec, 0, len(registry.Infos()))
	for _, info := range registry.Infos() {
		tools = append(tools, agentcontext.ToolSpec{Name: info.Name, Description: info.Desc, Available: true})
	}
	bundle := agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: smartCommandIdentity,
			ChatID:    strings.TrimSpace(opts.ChatID),
			SenderID:  smartCommandIdentity,
			Content:   userMessage,
		},
		WorkKind:           domain.WorkKindSmartCommand,
		MaxTurns:           20,
		OutputLanguage:     string(language),
		User:               agentcontext.UserProfile{OpenID: smartCommandIdentity, Name: smartCommandIdentity},
		Environment:        agentcontext.EnvironmentSnapshot{WorkspaceRoot: workspaceRoot, Tools: tools},
		GitHubReference:    githubRef,
		GitHubEventSummary: summary,
	}

	loop := agentruntime.AgentLoop{
		Model:             model,
		TerminalFinalizer: finalizer,
		Tools:             registry,
		MaxTurns:          20,
		MaxToolCalls:      12,
		MaxElapsed:        8 * time.Minute,
		SystemPrompt:      agentcontext.SmartCommandSystemPrompt(),
	}
	decision, _, loopErr := loop.Decide(ctx, bundle)
	if loopErr != nil {
		return Result{}, loopErr
	}
	if decision.Kind != domain.DecisionRecord {
		return Result{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "smart command must finish with decision=record")
	}
	result.Skipped = result.Skipped || decision.Skipped
	if decision.ReplyOutcome == domain.ReplyOutcomePartial || decision.ReplyOutcome == domain.ReplyOutcomeClarification {
		result.Partial = true
	}
	result.Partial = result.Partial || gate.Partial
	result.CommentID = gate.CommentID
	result.CheckID = gate.CheckID
	result.MessageID = gate.MessageID
	result.Title = gate.Title
	if len(gate.Outputs) > 0 {
		result.Outputs = gate.Outputs
	}
	if opts.GitHub {
		result.Reference = snapshot.Reference
	}
	return result, nil
}

func postHelpComment(ctx context.Context, opts Options, ref internalgithub.Reference, body string) (string, error) {
	client := opts.GitHubClient
	if client == nil {
		token, err := secretstore.Read(
			ctx,
			opts.Config.GitHub.TokenKeychainService,
			opts.Config.GitHub.TokenKeychainKey,
			"GITHUB_TOKEN",
		)
		if err != nil {
			return "", err
		}
		var errClient error
		client, errClient = internalgithub.NewClient(internalgithub.ClientConfig{
			BaseURL: opts.Config.GitHub.APIBaseURL,
			Token:   token,
			Limits: internalgithub.Limits{
				MaxFiles:       opts.Config.GitHub.MaxFiles,
				MaxPatchBytes:  opts.Config.GitHub.MaxPatchBytes,
				MaxAnnotations: opts.Config.GitHub.MaxAnnotations,
				MaxReviews:     opts.Config.GitHub.MaxReviews,
			},
		})
		if errClient != nil {
			return "", errClient
		}
	}
	posted, err := client.PostIssueComment(ctx, ref, body)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(posted.ID, 10), nil
}

func catalog(
	opts Options,
	githubClient *internalgithub.Client,
	larkService *serviceim.Service,
	gate *agenttools.WriteGate,
	workspaceRoot string,
) ([]agenttools.Definition, error) {
	definitions := []agenttools.Definition{agentruntime.SubmitDecisionDefinition()}
	if opts.GitHub && githubClient != nil {
		definitions = append(definitions,
			agenttools.GitHubContextDefinition(githubClient),
			agenttools.GitHubFileDefinition(githubClient),
			agenttools.GitHubCompareDefinition(githubClient),
			agenttools.PostGitHubCommentDefinition(githubClient, gate),
			agenttools.UpdateGitHubIssueTitleDefinition(githubClient, gate),
			agenttools.UpsertGitHubCheckDefinition(githubClient, gate),
		)
	}
	if !opts.GitHub {
		scope, err := workspace.NewScope(workspaceRoot)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, agenttools.WorkspaceReadDefinitions(scope)...)
	}
	if strings.TrimSpace(opts.ChatID) != "" && larkService != nil {
		definitions = append(definitions, agenttools.BoundLarkContextDefinitions(larkToolContext{svc: larkService}, opts.ChatID)...)
	}
	if larkService != nil {
		definitions = append(definitions, agenttools.SendLarkMessageDefinition(larkBotSender{svc: larkService}, gate))
	}
	definitions = append(definitions, agenttools.WriteJobOutputDefinition(gate))
	return definitions, nil
}

func composePrompt(root string, opts Options, mention internalgithub.Mention) (string, error) {
	var parts []string
	message := strings.TrimSpace(opts.Message)
	if strings.TrimSpace(opts.PromptFile) == "" && message == "" {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "prompt file or --message is required")
	}
	if strings.TrimSpace(opts.PromptFile) != "" {
		text, err := readWorkspaceFile(root, opts.PromptFile, "prompt")
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	if strings.TrimSpace(opts.RulesFile) != "" {
		text, err := readWorkspaceFile(root, opts.RulesFile, "rules")
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	if contract := slashContractFile(mention.Command); contract != "" {
		text, err := readWorkspaceFile(root, contract, "prompt")
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	if message != "" {
		parts = append(parts, message)
	}
	if strings.TrimSpace(mention.ExtraPrompt) != "" {
		parts = append(parts, mention.ExtraPrompt)
	}
	if len(parts) == 0 {
		return "Complete this smart command.", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

func slashContractFile(command string) string {
	switch command {
	case "review":
		return ".github/lark-agent/prompts/review.md"
	case "title":
		return ".github/lark-agent/prompts/title-rules.md"
	case "check":
		return ".github/lark-agent/prompts/merge-check.md"
	default:
		return ""
	}
}

func readWorkspaceFile(root, rel, kind string) (string, error) {
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "%s path escapes the workspace", kind)
	}
	cleaned := filepath.Clean(filepath.Join(root, rel))
	relative, err := filepath.Rel(root, cleaned)
	if err != nil || strings.HasPrefix(relative, "..") {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "%s path escapes the workspace", kind)
	}
	data, err := os.ReadFile(cleaned)
	if err != nil {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "%s file is missing", kind).WithCause(err)
	}
	return string(data), nil
}

// resolveOutputLanguage fixes one concrete outward language before any model or
// HTTP call. Flag beats environment beats configuration; prompt, rules, and
// message text are instructions and never language samples.
func resolveOutputLanguage(opts Options) (agentlocale.Language, error) {
	requested := firstNonEmpty(opts.OutputLanguage, os.Getenv("LARK_AGENT_OUTPUT_LANGUAGE"))
	if strings.TrimSpace(requested) == "" {
		return opts.Config.OutwardLanguage(), nil
	}
	language, err := agentlocale.ParsePreferred(requested)
	if err != nil {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported output language: %s",
			strings.TrimSpace(requested),
		).WithParam("--output-language")
	}
	if language == agentlocale.LanguageAuto {
		return opts.Config.OutwardLanguage(), nil
	}
	return language, nil
}

func resolveRoleModel(ctx context.Context, cfg config.Config, role string) (*agentruntime.OpenAICompatibleModel, error) {
	profileName := strings.TrimSpace(role)
	if profileName == "" {
		profileName = "primary"
	}
	profile, ok := cfg.Model.Profiles[profileName]
	if !ok {
		return nil, errs.NewConfigError(errs.SubtypeInvalidConfig, "model profile does not exist: %s", profileName)
	}
	envName := ""
	if profileName == "primary" {
		envName = "OPENAI_API_KEY"
	}
	apiKey, err := secretstore.Read(ctx, profile.KeychainService, profile.CredentialKeychainKey, envName)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "model credentials are required")
	}
	baseURL := profile.BaseURL
	modelName := profile.Name
	if profileName == "primary" {
		baseURL = firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), baseURL)
		modelName = firstNonEmpty(os.Getenv("OPENAI_MODEL"), modelName)
	}
	timeout := profile.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &agentruntime.OpenAICompatibleModel{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   modelName,
		Timeout: timeout,
	}, nil
}

func repositoryAllowed(repository string, allowed []string) bool {
	for _, item := range allowed {
		if item == repository {
			return true
		}
	}
	return false
}

func allowMap(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		out[name] = true
	}
	return out
}

func secretValues() []string {
	var out []string
	for _, name := range []string{"LARK_AGENT_APP_SECRET", "GITHUB_TOKEN", "OPENAI_API_KEY"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func credentialRefs(cfg config.Config) serviceim.CredentialRefs {
	return serviceim.CredentialRefs{
		Service:             cfg.Lark.KeychainService,
		AppSecretAccount:    cfg.Lark.AppSecretKeychainKey,
		UserTokenAccount:    cfg.Lark.UserTokenKeychainKey,
		RefreshTokenAccount: cfg.Lark.RefreshTokenKeychainKey,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type larkToolContext struct {
	svc *serviceim.Service
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
	events := make([]domain.NormalizedEvent, 0, len(messageContext.Messages))
	for _, message := range messageContext.Messages {
		events = append(events, domain.NormalizedEvent{
			MessageID: message.MessageID,
			ChatID:    message.ChatID,
			Content:   message.Content,
			SenderID:  message.SenderOpenID,
		})
	}
	return agenttools.LarkContextResult{Messages: events, Selection: messageContext.Selection}, nil
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
	events := make([]domain.NormalizedEvent, 0, len(result.Items))
	for _, message := range result.Items {
		events = append(events, domain.NormalizedEvent{
			MessageID: message.MessageID,
			ChatID:    message.ChatID,
			Content:   message.Content,
			SenderID:  message.SenderOpenID,
		})
	}
	return events, nil
}

type larkBotSender struct {
	svc *serviceim.Service
}

func (s larkBotSender) Send(ctx context.Context, chatID, messageType, contentJSON, idempotencyKey string) (string, error) {
	result, err := s.svc.SendMessageAsBot(ctx, serviceim.SendMessageRequest{
		ChatID:         chatID,
		MessageType:    messageType,
		Content:        contentJSON,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return "", err
	}
	return result.MessageID, nil
}

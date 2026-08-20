package tools

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/internal/apperr"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

type GitHubWriter interface {
	PostIssueComment(context.Context, internalgithub.Reference, string) (internalgithub.CommentResult, error)
	UpdateIssueTitle(context.Context, internalgithub.Reference, string) error
	UpsertCheck(context.Context, internalgithub.Reference, internalgithub.CheckUpsert) (internalgithub.CheckResult, error)
}

type BotMessageSender interface {
	Send(ctx context.Context, chatID, messageType, contentJSON, idempotencyKey string) (messageID string, err error)
}

// WriteGate tracks one-shot smart-command writes and secret redaction.
type WriteGate struct {
	mu sync.Mutex

	Allow          map[string]bool
	Secrets        []string
	Language       agentlocale.Language
	JobOutputPath  string
	ChatID         string
	AppSecret      string
	Reference      *internalgithub.Reference
	EncodeMarker   func(internalgithub.Reference, string) (string, error)
	IdempotencyKey func(string, internalgithub.Reference) string

	CommentPosted bool
	TitleUpdated  bool
	LarkSent      bool
	OutputWritten bool
	Partial       bool
	CommentID     string
	CheckID       string
	MessageID     string
	Title         string
	Outputs       map[string]string
}

// Wrote reports whether any write tool succeeded, so a caller can describe a
// run that produced no outward effect without trusting the model to say so.
func (g *WriteGate) Wrote() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.CommentPosted || g.TitleUpdated || g.LarkSent || g.OutputWritten ||
		strings.TrimSpace(g.CheckID) != ""
}

func (g *WriteGate) allowed(name string) bool {
	if g == nil || g.Allow == nil {
		return false
	}
	return g.Allow[name]
}

func (g *WriteGate) rejectSecrets(body string) error {
	if g == nil {
		return nil
	}
	for _, secret := range g.Secrets {
		if secret != "" && strings.Contains(body, secret) {
			return errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"outbound body contains a secret",
			)
		}
	}
	return nil
}

// rejectLanguage keeps outward prose in the language the run resolved. Titles
// are repository artifacts, not outward prose, so callers do not pass them here.
func (g *WriteGate) rejectLanguage(text string) error {
	if g == nil {
		return nil
	}
	language, err := agentlocale.ParseConcrete(string(g.Language))
	if err != nil {
		return nil
	}
	if err := agentlocale.ValidateProse(text, language); err != nil {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"outward text must be written in %s; rewrite it in that language and call again",
			language,
		).WithCause(err)
	}
	return nil
}

func denyWrite(name string) error {
	return errs.NewValidationError(
		errs.SubtypeFailedPrecondition,
		"%s is not allowed",
		name,
	)
}

func PostGitHubCommentDefinition(writer GitHubWriter, gate *WriteGate) Definition {
	const name = internalgithub.ActionPostGitHubComment
	return Definition{
		Info: toolInfo(name, "Post one issue or pull-request comment on the verified GitHub object.", map[string]*schema.ParameterInfo{
			"body": {Type: schema.String, Required: true},
		}),
		Permission:              ToolPermissionAllow,
		Risk:                    ToolRiskMedium,
		SideEffect:              true,
		NonOwnerReadOnly:        true,
		RequiresGitHubReference: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Body string `json:"body"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if !gate.allowed(name) {
				return Execution{}, denyWrite(name)
			}
			body := strings.TrimSpace(args.Body)
			if body == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "comment body is required")
			}
			if len(body) > 65536 {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "comment body exceeds 65536 bytes")
			}
			if err := gate.rejectSecrets(body); err != nil {
				return Execution{}, err
			}
			if err := gate.rejectLanguage(body); err != nil {
				return Execution{}, err
			}
			scope, ok := invocationScope(ctx)
			if !ok || scope.GitHubReference == nil {
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "verified GitHub reference is required")
			}
			gate.mu.Lock()
			if gate.CommentPosted {
				gate.mu.Unlock()
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "github comment already posted")
			}
			gate.mu.Unlock()
			result, err := writer.PostIssueComment(ctx, *scope.GitHubReference, body)
			if err != nil {
				return Execution{}, err
			}
			gate.mu.Lock()
			gate.CommentPosted = true
			gate.CommentID = formatInt64(result.ID)
			gate.mu.Unlock()
			return jsonExecution(map[string]any{"id": result.ID}, nil, nil)
		},
	}
}

func UpdateGitHubIssueTitleDefinition(writer GitHubWriter, gate *WriteGate) Definition {
	const name = internalgithub.ActionUpdateGitHubIssueTitle
	return Definition{
		Info: toolInfo(name, "Replace the verified issue or pull-request title.", map[string]*schema.ParameterInfo{
			"title": {Type: schema.String, Required: true},
		}),
		Permission:              ToolPermissionAllow,
		Risk:                    ToolRiskMedium,
		SideEffect:              true,
		NonOwnerReadOnly:        true,
		RequiresGitHubReference: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Title string `json:"title"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if !gate.allowed(name) {
				return Execution{}, denyWrite(name)
			}
			title := strings.TrimSpace(args.Title)
			if title == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "title is required")
			}
			if utf8.RuneCountInString(title) > 256 {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "title exceeds 256 characters")
			}
			if err := gate.rejectSecrets(title); err != nil {
				return Execution{}, err
			}
			scope, ok := invocationScope(ctx)
			if !ok || scope.GitHubReference == nil {
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "verified GitHub reference is required")
			}
			gate.mu.Lock()
			if gate.TitleUpdated {
				gate.mu.Unlock()
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "github title already updated")
			}
			gate.mu.Unlock()
			if err := writer.UpdateIssueTitle(ctx, *scope.GitHubReference, title); err != nil {
				return Execution{}, err
			}
			gate.mu.Lock()
			gate.TitleUpdated = true
			gate.Title = title
			gate.mu.Unlock()
			return jsonExecution(map[string]string{"title": title}, nil, nil)
		},
	}
}

func UpsertGitHubCheckDefinition(writer GitHubWriter, gate *WriteGate) Definition {
	const name = internalgithub.ActionUpsertGitHubCheck
	return Definition{
		Info: toolInfo(name, "Create or update the lark-agent-gate check on the verified pull-request head SHA.", map[string]*schema.ParameterInfo{
			"conclusion": {Type: schema.String, Required: true, Enum: []string{"success", "failure", "neutral"}},
			"title":      {Type: schema.String, Required: true},
			"summary":    {Type: schema.String, Required: true},
			"text":       {Type: schema.String},
		}),
		Permission:              ToolPermissionAllow,
		Risk:                    ToolRiskMedium,
		SideEffect:              true,
		NonOwnerReadOnly:        true,
		RequiresGitHubReference: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Conclusion string `json:"conclusion"`
				Title      string `json:"title"`
				Summary    string `json:"summary"`
				Text       string `json:"text"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if !gate.allowed(name) {
				return Execution{}, denyWrite(name)
			}
			switch args.Conclusion {
			case "success", "failure", "neutral":
			default:
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid check conclusion")
			}
			if utf8.RuneCountInString(args.Title) > 255 {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "check title exceeds 255 characters")
			}
			if utf8.RuneCountInString(args.Summary) > 65535 || utf8.RuneCountInString(args.Text) > 65535 {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "check summary or text exceeds 65535 characters")
			}
			if err := gate.rejectSecrets(args.Title + args.Summary + args.Text); err != nil {
				return Execution{}, err
			}
			if err := gate.rejectLanguage(args.Summary + "\n" + args.Text); err != nil {
				return Execution{}, err
			}
			scope, ok := invocationScope(ctx)
			if !ok || scope.GitHubReference == nil {
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "verified GitHub reference is required")
			}
			if scope.GitHubReference.HeadSHA == "" || scope.GitHubReference.PullRequestNumber <= 0 {
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "check upsert requires a pull request head_sha")
			}
			result, err := writer.UpsertCheck(ctx, *scope.GitHubReference, internalgithub.CheckUpsert{
				Conclusion: args.Conclusion,
				Title:      args.Title,
				Summary:    args.Summary,
				Text:       args.Text,
			})
			if err != nil {
				return Execution{}, err
			}
			gate.mu.Lock()
			gate.CheckID = formatInt64(result.ID)
			gate.mu.Unlock()
			return jsonExecution(map[string]any{"id": result.ID, "name": internalgithub.CheckRunName}, nil, nil)
		},
	}
}

func SendLarkMessageDefinition(sender BotMessageSender, gate *WriteGate) Definition {
	const name = internalgithub.ActionSendLarkMessage
	return Definition{
		Info: toolInfo(name, "Send one plain-text message to the configured Lark chat. The runtime appends a GitHub reference footer. Markdown link syntax is displayed literally in the chat, so write bare URLs.", map[string]*schema.ParameterInfo{
			"text": {Type: schema.String, Required: true},
		}),
		Permission:       ToolPermissionAllow,
		Risk:             ToolRiskMedium,
		SideEffect:       true,
		NonOwnerReadOnly: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if !gate.allowed(name) {
				return Execution{}, denyWrite(name)
			}
			text := strings.TrimSpace(args.Text)
			if text == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "text is required")
			}
			const maxLarkTextRunes = 4000
			if utf8.RuneCountInString(text) > maxLarkTextRunes {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "text exceeds 4000 runes")
			}
			if err := gate.rejectSecrets(text); err != nil {
				return Execution{}, err
			}
			if err := gate.rejectLanguage(text); err != nil {
				return Execution{}, err
			}
			if strings.TrimSpace(gate.ChatID) == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "--chat-id is required")
			}
			gate.mu.Lock()
			if gate.LarkSent {
				gate.mu.Unlock()
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "lark message already sent")
			}
			gate.mu.Unlock()
			if gate.Reference != nil && gate.EncodeMarker != nil && strings.TrimSpace(gate.AppSecret) != "" {
				marker, err := gate.EncodeMarker(*gate.Reference, gate.AppSecret)
				if err != nil {
					return Execution{}, err
				}
				text = appendMarkerWithinLimit(text, marker, maxLarkTextRunes)
			}
			if err := gate.rejectSecrets(text); err != nil {
				return Execution{}, err
			}
			content, err := json.Marshal(map[string]string{"text": text})
			if err != nil {
				return Execution{}, err
			}
			key := "ghs-smart-command"
			if gate.IdempotencyKey != nil && gate.Reference != nil {
				key = gate.IdempotencyKey(gate.ChatID, *gate.Reference)
			}
			messageID, err := sender.Send(ctx, gate.ChatID, "text", string(content), key)
			if err != nil {
				return Execution{}, err
			}
			gate.mu.Lock()
			gate.LarkSent = true
			gate.MessageID = messageID
			gate.mu.Unlock()
			return jsonExecution(map[string]string{"message_id": messageID, "idempotency_key": key}, nil, nil)
		},
	}
}

func WriteJobOutputDefinition(gate *WriteGate) Definition {
	const name = internalgithub.ActionWriteJobOutput
	return Definition{
		Info: toolInfo(name, "Write one GitHub Actions job output. The only supported name is changelog.", map[string]*schema.ParameterInfo{
			"name":  {Type: schema.String, Required: true},
			"value": {Type: schema.String, Required: true},
		}),
		Permission:       ToolPermissionAllow,
		Risk:             ToolRiskLow,
		SideEffect:       true,
		NonOwnerReadOnly: true,
		Execute: func(_ context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if !gate.allowed(name) {
				return Execution{}, denyWrite(name)
			}
			if args.Name != "changelog" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "write_job_output name must be changelog")
			}
			for _, line := range strings.Split(args.Value, "\n") {
				if line == "LARK_AGENT_EOF" {
					return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "changelog value contains LARK_AGENT_EOF")
				}
			}
			if err := gate.rejectSecrets(args.Value); err != nil {
				return Execution{}, err
			}
			if err := gate.rejectLanguage(args.Value); err != nil {
				return Execution{}, err
			}
			gate.mu.Lock()
			if gate.OutputWritten {
				gate.mu.Unlock()
				return Execution{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "job output already written")
			}
			gate.mu.Unlock()
			path := strings.TrimSpace(gate.JobOutputPath)
			if path == "" {
				file, err := os.CreateTemp("", "lark-agent-github-output-*")
				if err != nil {
					return Execution{}, err
				}
				path = file.Name()
				_ = file.Close()
				gate.JobOutputPath = path
			}
			block := "changelog<<LARK_AGENT_EOF\n" + args.Value + "\nLARK_AGENT_EOF\n"
			if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
				return Execution{}, errs.NewInternalError(errs.SubtypeFileIO, "write GitHub output file").WithCause(err)
			}
			gate.mu.Lock()
			gate.OutputWritten = true
			if gate.Outputs == nil {
				gate.Outputs = map[string]string{}
			}
			gate.Outputs["changelog"] = args.Value
			gate.mu.Unlock()
			return jsonExecution(map[string]string{"name": "changelog"}, nil, nil)
		},
	}
}

func appendMarkerWithinLimit(text, marker string, maxRunes int) string {
	text = strings.TrimSpace(text)
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return text
	}
	footerRunes := utf8.RuneCountInString("\n" + marker)
	if maxRunes <= footerRunes {
		return marker
	}
	if utf8.RuneCountInString(text)+footerRunes <= maxRunes {
		return text + "\n" + marker
	}
	ellipsis := "\n... [truncated]"
	allowed := maxRunes - footerRunes - utf8.RuneCountInString(ellipsis)
	if allowed <= 0 {
		return marker
	}
	return firstRunes(text, allowed) + ellipsis + "\n" + marker
}

func firstRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index := range text {
		if count == limit {
			return strings.TrimSpace(text[:index])
		}
		count++
	}
	return strings.TrimSpace(text)
}

func formatInt64(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

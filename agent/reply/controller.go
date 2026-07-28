// Package reply coordinates policy checks and reply tools.
package reply

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/policy"
	"github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Result records reply handling outcome.
type Result struct {
	Action domain.Action     `json:"action" yaml:"action"`
	Reply  tools.ReplyResult `json:"reply,omitempty" yaml:"reply,omitempty"`
}

// Controller applies policy gates and executes ready reply actions.
type Controller struct {
	gate      *policy.ReplyGate
	messenger tools.Messenger
	approvals ApprovalStore
}

// ApprovalStore persists and consumes one-time exact draft approvals.
type ApprovalStore interface {
	RequestReplyApproval(context.Context, string, string, string, string, domain.Relevance) (int64, error)
	ConsumeReplyApproval(context.Context, string, string, string, string, domain.Relevance) (int64, bool, error)
	CompleteReplyApproval(context.Context, int64, string, string) error
}

type replyActionStore interface {
	BeginReplyAction(context.Context, string, string) (int64, string, string, bool, error)
	CompleteReplyAction(context.Context, int64, string, string) error
}

type botReplyMessenger interface {
	ReplyAsBot(context.Context, tools.ReplyRequest) (tools.ReplyResult, error)
}

// NewController creates a reply controller.
func NewController(gate *policy.ReplyGate, messenger tools.Messenger, approvals ...ApprovalStore) *Controller {
	controller := &Controller{gate: gate, messenger: messenger}
	if len(approvals) > 0 {
		controller.approvals = approvals[0]
	}
	return controller
}

// Handle gates, sends, and notifies for a proposed reply.
func (c *Controller) Handle(ctx context.Context, item domain.WorkItem, decision domain.Decision) (Result, error) {
	action, err := c.gate.Prepare(ctx, item, decision)
	if err != nil {
		return Result{}, err
	}
	result := Result{Action: action}
	var approvalID int64
	if (action.Status == domain.ActionBlocked || action.Status == domain.ActionCancelled) &&
		c.approvals != nil &&
		decision.Mode == domain.ModeApproval {
		var approved bool
		approvalID, approved, err = c.approvals.ConsumeReplyApproval(
			ctx,
			item.DedupKey,
			decision.ReplyText,
			decision.Reason,
			decision.OwnerAction,
			decision.Relevance,
		)
		if err != nil {
			return result, err
		}
		if approved {
			if err := c.approvals.CompleteReplyApproval(
				ctx,
				approvalID,
				"",
				action.CancelReason,
			); err != nil {
				return result, err
			}
			result.Action.Idempotency = fmt.Sprintf("approval:%d", approvalID)
		}
	}
	if action.Status == domain.ActionAwaitingApproval && c.approvals != nil {
		var approved bool
		approvalID, approved, err = c.approvals.ConsumeReplyApproval(
			ctx,
			item.DedupKey,
			decision.ReplyText,
			decision.Reason,
			decision.OwnerAction,
			decision.Relevance,
		)
		if err != nil {
			return result, err
		}
		if !approved {
			approvalID, err = c.approvals.RequestReplyApproval(
				ctx,
				item.DedupKey,
				decision.ReplyText,
				decision.Reason,
				decision.OwnerAction,
				decision.Relevance,
			)
			if err != nil {
				return result, err
			}
			result.Action.Idempotency = fmt.Sprintf("approval:%d", approvalID)
			return result, nil
		}
		action.Status = domain.ActionReady
		action.Idempotency = item.DedupKey + ":reply"
		result.Action = action
	}
	if action.Status == domain.ActionReady && c.approvals != nil && approvalID == 0 {
		var approved bool
		approvalID, approved, err = c.approvals.ConsumeReplyApproval(
			ctx,
			item.DedupKey,
			decision.ReplyText,
			decision.Reason,
			decision.OwnerAction,
			decision.Relevance,
		)
		if err != nil {
			return result, err
		}
		if approved {
			action.Idempotency = item.DedupKey + ":reply"
			result.Action = action
		} else if decision.Mode == domain.ModeApproval {
			return result, errs.NewInternalError(
				errs.SubtypeFailedPrecondition,
				"persisted approved reply has no consumable exact approval action",
			)
		}
	}
	if action.Status != domain.ActionReady {
		return result, nil
	}
	text, err := renderReplyMentions(decision.ReplyText, item.Event.Mentions)
	if err != nil {
		return result, err
	}
	text = strings.TrimSpace(text)
	replyAsBot := shouldReplyAsBot(decision)
	if !replyAsBot {
		text = ensureRobotReplyPrefix(text)
	}
	if text == "" {
		return result, errs.NewValidationError(errs.SubtypeInvalidArgument, "reply decision requires non-empty reply_text")
	}
	var replyActionID int64
	if actions, ok := c.approvals.(replyActionStore); ok {
		var key, existingMessageID string
		var completed bool
		replyActionID, key, existingMessageID, completed, err = actions.BeginReplyAction(
			ctx, item.DedupKey, text,
		)
		if err != nil {
			return result, err
		}
		action.Idempotency = key
		result.Action = action
		if completed {
			result.Action.Status = domain.ActionCompleted
			result.Reply.MessageID = existingMessageID
			return result, nil
		}
	}
	request := tools.ReplyRequest{
		MessageID:      item.Event.MessageID,
		Text:           text,
		IdempotencyKey: action.Idempotency,
	}
	var replyResult tools.ReplyResult
	if replyAsBot {
		if request.MessageID == "" {
			return result, errs.NewValidationError(errs.SubtypeInvalidArgument, "message_id is required").WithParam("message_id")
		}
		botMessenger, ok := c.messenger.(botReplyMessenger)
		if !ok {
			return result, errs.NewInternalError(errs.SubtypeUnknown, "bot reply messenger is not configured")
		}
		replyResult, err = botMessenger.ReplyAsBot(ctx, request)
	} else {
		replyTool := tools.ReplyMessageTool{Messenger: c.messenger}
		replyResult, err = replyTool.Execute(ctx, request)
	}
	if err != nil {
		if actions, ok := c.approvals.(replyActionStore); ok && replyActionID != 0 {
			_ = actions.CompleteReplyAction(context.WithoutCancel(ctx), replyActionID, "", err.Error())
		}
		if approvalID != 0 {
			_ = c.approvals.CompleteReplyApproval(context.WithoutCancel(ctx), approvalID, "", err.Error())
		}
		return result, err
	}
	result.Reply = replyResult
	result.Action.Status = domain.ActionCompleted
	if actions, ok := c.approvals.(replyActionStore); ok && replyActionID != 0 {
		if err := actions.CompleteReplyAction(ctx, replyActionID, replyResult.MessageID, ""); err != nil {
			return result, err
		}
	}
	if approvalID != 0 {
		if err := c.approvals.CompleteReplyApproval(ctx, approvalID, replyResult.MessageID, ""); err != nil {
			return result, err
		}
	}
	return result, nil
}

func shouldReplyAsBot(decision domain.Decision) bool {
	return decision.Relevance == domain.RelevanceOwnerRequest ||
		decision.Relevance == domain.RelevanceAssistantRequest
}

var larkMentionPlaceholderPattern = regexp.MustCompile(`@_user_\d+`)

func renderReplyMentions(text string, mentions []domain.Mention) (string, error) {
	mentionByKey := make(map[string]domain.Mention, len(mentions))
	for _, mention := range mentions {
		if strings.TrimSpace(mention.Key) == "" {
			continue
		}
		mentionByKey[mention.Key] = mention
	}
	var unmapped string
	rendered := larkMentionPlaceholderPattern.ReplaceAllStringFunc(text, func(key string) string {
		mention, ok := mentionByKey[key]
		if !ok {
			unmapped = key
			return key
		}
		name := strings.TrimSpace(mention.Name)
		if name == "" {
			name = strings.TrimPrefix(key, "@")
		}
		openID := strings.TrimSpace(mention.OpenID)
		if openID == "" {
			return "@" + name
		}
		return fmt.Sprintf(`<at user_id="%s">%s</at>`, html.EscapeString(openID), html.EscapeString(name))
	})
	if unmapped != "" {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"reply_text contains unmapped Lark mention placeholder %s", unmapped,
		).WithParam("reply_text")
	}
	return rendered, nil
}

func ensureRobotReplyPrefix(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "🤖") {
		return text
	}
	return "🤖" + text
}

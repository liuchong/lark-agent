// Package tools defines narrow, strongly typed tools exposed to the agent.
package tools

import (
	"context"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

// Messenger is the narrow Lark IM surface available to tools.
type Messenger interface {
	ReplyAsUser(context.Context, ReplyRequest) (ReplyResult, error)
	NotifyOwner(context.Context, NotifyRequest) error
}

// ReplyRequest is the only allowed owner-reply shape in V1.
type ReplyRequest struct {
	MessageID      string `json:"message_id" yaml:"message_id"`
	Text           string `json:"text" yaml:"text"`
	IdempotencyKey string `json:"idempotency_key,omitempty" yaml:"idempotency_key,omitempty"`
}

// ReplyResult is the Lark result from a user-identity reply.
type ReplyResult struct {
	MessageID string `json:"message_id" yaml:"message_id"`
	ChatID    string `json:"chat_id" yaml:"chat_id"`
}

// NotifyRequest is a bot-owner notification.
type NotifyRequest struct {
	Text           string `json:"text" yaml:"text"`
	IdempotencyKey string `json:"idempotency_key,omitempty" yaml:"idempotency_key,omitempty"`
}

// ReplyMessageTool replies to one specific message as the owner.
type ReplyMessageTool struct {
	Messenger Messenger
}

// Execute validates and dispatches the reply.
func (t ReplyMessageTool) Execute(ctx context.Context, req ReplyRequest) (ReplyResult, error) {
	if req.MessageID == "" {
		return ReplyResult{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "message_id is required").WithParam("message_id")
	}
	if req.Text == "" {
		return ReplyResult{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "text is required").WithParam("text")
	}
	if t.Messenger == nil {
		return ReplyResult{}, errs.NewInternalError(errs.SubtypeUnknown, "messenger is not configured")
	}
	return t.Messenger.ReplyAsUser(ctx, req)
}

// NotifyOwnerTool sends a bot notification to the owner.
type NotifyOwnerTool struct {
	Messenger Messenger
}

// Execute validates and dispatches the notification.
func (t NotifyOwnerTool) Execute(ctx context.Context, req NotifyRequest) error {
	if req.Text == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "text is required").WithParam("text")
	}
	if t.Messenger == nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "messenger is not configured")
	}
	return t.Messenger.NotifyOwner(ctx, req)
}

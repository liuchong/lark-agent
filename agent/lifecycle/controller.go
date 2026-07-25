package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Summary is the owner-visible recovery state attached to lifecycle notices.
type Summary struct {
	Interrupted int `json:"interrupted" yaml:"interrupted"`
	Uncertain   int `json:"uncertain" yaml:"uncertain"`
}

// Controller sends lifecycle messages through the bot-owner private channel.
type Controller struct {
	messenger tools.Messenger
	recorder  Recorder
}

// Recorder durably fences lifecycle notification side effects.
type Recorder interface {
	BeginLifecycleNotification(
		context.Context,
		string,
		string,
	) (int64, domain.ActionStatus, bool, error)
	CompleteLifecycleNotification(context.Context, int64, string) error
}

// NewController creates a lifecycle notification controller.
func NewController(messenger tools.Messenger) *Controller {
	return &Controller{messenger: messenger}
}

// NewDurableController creates a lifecycle controller that records intent
// before sending and never automatically repeats an uncertain write.
func NewDurableController(messenger tools.Messenger, recorder Recorder) *Controller {
	return &Controller{messenger: messenger, recorder: recorder}
}

// NotifyOffline reports one intentional transition before service unload.
func (c *Controller) NotifyOffline(
	ctx context.Context,
	transitionID string,
	summary Summary,
) error {
	if strings.TrimSpace(transitionID) == "" {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"offline lifecycle transition id is required",
		).WithParam("transition_id")
	}
	return c.notify(ctx, tools.NotifyRequest{
		Text: fmt.Sprintf(
			"🤖 Agent 正在离线。已暂停 %d 条未完成工作，%d 个外部动作结果不确定；不会自动回放，重新上线后请先检查再恢复。",
			summary.Interrupted,
			summary.Uncertain,
		),
		IdempotencyKey: lifecycleIdempotencyKey("offline", transitionID),
	})
}

// NotifyOnline reports one successfully ready daemon process session.
func (c *Controller) NotifyOnline(
	ctx context.Context,
	sessionID string,
	summary Summary,
) error {
	if strings.TrimSpace(sessionID) == "" {
		return errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"online lifecycle session id is required",
		).WithParam("session_id")
	}
	return c.notify(ctx, tools.NotifyRequest{
		Text: fmt.Sprintf(
			"🤖 Agent 已上线。共有 %d 条中断工作、%d 个结果不确定的外部动作；不会自动回放，请检查后显式恢复。",
			summary.Interrupted,
			summary.Uncertain,
		),
		IdempotencyKey: lifecycleIdempotencyKey("online", sessionID),
	})
}

func lifecycleIdempotencyKey(transition, identity string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(identity)))
	return fmt.Sprintf("lifecycle:%s:%x", transition, digest[:16])
}

func (c *Controller) notify(ctx context.Context, request tools.NotifyRequest) error {
	if c.messenger == nil {
		return errs.NewInternalError(errs.SubtypeFailedPrecondition, "lifecycle messenger is not configured")
	}
	var actionID int64
	if c.recorder != nil {
		id, status, send, err := c.recorder.BeginLifecycleNotification(
			ctx,
			request.IdempotencyKey,
			request.Text,
		)
		if err != nil {
			return err
		}
		if !send {
			if status == domain.ActionCompleted {
				return nil
			}
			return errs.NewValidationError(
				errs.SubtypeFailedPrecondition,
				"lifecycle notification result is uncertain and will not be resent automatically",
			)
		}
		actionID = id
	}
	sendErr := (tools.NotifyOwnerTool{Messenger: c.messenger}).Execute(ctx, request)
	if c.recorder != nil {
		errText := ""
		if sendErr != nil {
			errText = sendErr.Error()
		}
		if recordErr := c.recorder.CompleteLifecycleNotification(ctx, actionID, errText); recordErr != nil {
			if sendErr != nil {
				return errs.NewInternalError(
					errs.SubtypeStorage,
					"send and record lifecycle notification",
				).WithCause(errors.Join(sendErr, recordErr))
			}
			return recordErr
		}
	}
	return sendErr
}

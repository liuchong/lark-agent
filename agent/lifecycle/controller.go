package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Summary is the owner-visible recovery state attached to lifecycle notices.
type Summary struct {
	Paused       int `json:"paused" yaml:"paused"`
	Resumed      int `json:"resumed" yaml:"resumed"`
	WaitingOwner int `json:"waiting_owner" yaml:"waiting_owner"`
	Terminalized int `json:"terminalized" yaml:"terminalized"`
	Uncertain    int `json:"uncertain" yaml:"uncertain"`
}

// Options controls deterministic lifecycle message presentation.
type Options struct {
	Language  string
	OwnerName string
}

// Controller sends lifecycle messages through the bot-owner private channel.
type Controller struct {
	messenger tools.Messenger
	recorder  Recorder
	options   Options
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
func NewController(messenger tools.Messenger, options ...Options) *Controller {
	return &Controller{messenger: messenger, options: firstOptions(options)}
}

// NewDurableController creates a lifecycle controller that records intent
// before sending and never automatically repeats an uncertain write.
func NewDurableController(messenger tools.Messenger, recorder Recorder, options ...Options) *Controller {
	return &Controller{messenger: messenger, recorder: recorder, options: firstOptions(options)}
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
		Text:           c.offlineText(summary),
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
		Text:           c.onlineText(summary),
		IdempotencyKey: lifecycleIdempotencyKey("online", sessionID),
	})
}

func firstOptions(options []Options) Options {
	if len(options) == 0 {
		return Options{Language: string(agentlocale.LanguageChinese)}
	}
	return options[0]
}

func (c *Controller) language() agentlocale.Language {
	if c.options.Language == string(agentlocale.LanguageEnglish) {
		return agentlocale.LanguageEnglish
	}
	return agentlocale.LanguageChinese
}

func (c *Controller) ownerName() string {
	if name := strings.TrimSpace(c.options.OwnerName); name != "" {
		return name
	}
	if c.language() == agentlocale.LanguageEnglish {
		return "Owner"
	}
	return "负责人"
}

func (c *Controller) offlineText(summary Summary) string {
	if c.language() == agentlocale.LanguageEnglish {
		return fmt.Sprintf(
			"🤖 Intelligent Assistant is going offline. %s, paused: %d, already resumed this session: %d, waiting for your action: %d, terminalized: %d, externally uncertain: %d. Uncertain external actions will not be replayed.",
			c.ownerName(),
			summary.Paused,
			summary.Resumed,
			summary.WaitingOwner,
			summary.Terminalized,
			summary.Uncertain,
		)
	}
	return fmt.Sprintf(
		"🤖 智能助手正在离线。%s，本次暂停 %d 条工作；本会话已续跑 %d 条，等待你处理 %d 条，已收口 %d 条，外部结果不确定 %d 条。不确定的外部动作不会重放。",
		c.ownerName(),
		summary.Paused,
		summary.Resumed,
		summary.WaitingOwner,
		summary.Terminalized,
		summary.Uncertain,
	)
}

func (c *Controller) onlineText(summary Summary) string {
	if c.language() == agentlocale.LanguageEnglish {
		return fmt.Sprintf(
			"🤖 Intelligent Assistant is online. %s, automatically resumed: %d, waiting for your action: %d, terminalized: %d, externally uncertain and not replayed: %d.",
			c.ownerName(),
			summary.Resumed,
			summary.WaitingOwner,
			summary.Terminalized,
			summary.Uncertain,
		)
	}
	return fmt.Sprintf(
		"🤖 智能助手已上线。%s，已自动续跑 %d 条，等待你处理 %d 条，已收口 %d 条，外部结果不确定且未重放 %d 条。",
		c.ownerName(),
		summary.Resumed,
		summary.WaitingOwner,
		summary.Terminalized,
		summary.Uncertain,
	)
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

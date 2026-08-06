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
		parts := englishSummaryParts(summary, true)
		if len(parts) == 0 {
			return fmt.Sprintf(
				"🤖 Intelligent Assistant is going offline. %s, there is no unfinished or unreconciled work.",
				c.ownerName(),
			)
		}
		return fmt.Sprintf(
			"🤖 Intelligent Assistant is going offline. %s, %s. Use `/tasks` to inspect actionable work.",
			c.ownerName(),
			strings.Join(parts, "; "),
		)
	}
	parts := chineseSummaryParts(summary, true)
	if len(parts) == 0 {
		return fmt.Sprintf(
			"🤖 智能助手正在离线。%s，当前没有未完成或待核对任务。",
			c.ownerName(),
		)
	}
	return fmt.Sprintf(
		"🤖 智能助手正在离线。%s，%s。发送 `/tasks` 查看需要处理的任务。",
		c.ownerName(),
		strings.Join(parts, "；"),
	)
}

func (c *Controller) onlineText(summary Summary) string {
	if c.language() == agentlocale.LanguageEnglish {
		parts := englishSummaryParts(summary, false)
		if len(parts) == 0 {
			return fmt.Sprintf(
				"🤖 Intelligent Assistant is online. %s, there is no work that needs your action. Use `/help` to view commands.",
				c.ownerName(),
			)
		}
		return fmt.Sprintf(
			"🤖 Intelligent Assistant is online. %s, %s. Use `/tasks` to inspect actionable work.",
			c.ownerName(),
			strings.Join(parts, "; "),
		)
	}
	parts := chineseSummaryParts(summary, false)
	if len(parts) == 0 {
		return fmt.Sprintf(
			"🤖 智能助手已上线。%s，当前没有需要你处理的任务。发送 `/help` 查看可用命令。",
			c.ownerName(),
		)
	}
	return fmt.Sprintf(
		"🤖 智能助手已上线。%s，%s。发送 `/tasks` 查看并处理。",
		c.ownerName(),
		strings.Join(parts, "；"),
	)
}

func chineseSummaryParts(summary Summary, includePaused bool) []string {
	var parts []string
	if includePaused && summary.Paused > 0 {
		parts = append(parts, fmt.Sprintf("本次暂停 %d 条工作", summary.Paused))
	}
	if summary.Resumed > 0 {
		parts = append(parts, fmt.Sprintf("已自动续跑 %d 条", summary.Resumed))
	}
	if summary.WaitingOwner > 0 {
		parts = append(parts, fmt.Sprintf("等待你处理 %d 条", summary.WaitingOwner))
	}
	if summary.Terminalized > 0 {
		parts = append(parts, fmt.Sprintf("已收口 %d 条", summary.Terminalized))
	}
	if summary.Uncertain > 0 {
		parts = append(parts, fmt.Sprintf(
			"外部结果不确定 %d 条，这些动作不会重放",
			summary.Uncertain,
		))
	}
	return parts
}

func englishSummaryParts(summary Summary, includePaused bool) []string {
	var parts []string
	if includePaused && summary.Paused > 0 {
		parts = append(parts, fmt.Sprintf("paused %d work items", summary.Paused))
	}
	if summary.Resumed > 0 {
		parts = append(parts, fmt.Sprintf("automatically resumed %d", summary.Resumed))
	}
	if summary.WaitingOwner > 0 {
		parts = append(parts, fmt.Sprintf("%d need your action", summary.WaitingOwner))
	}
	if summary.Terminalized > 0 {
		parts = append(parts, fmt.Sprintf("closed %d", summary.Terminalized))
	}
	if summary.Uncertain > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d external results are uncertain and will not be replayed",
			summary.Uncertain,
		))
	}
	return parts
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

// Package investigation manages durable staged replies for delegated work.
package investigation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/tools"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type Store interface {
	BeginDelegatedInvestigation(
		domain.DelegatedInvestigation,
	) (domain.DelegatedInvestigation, bool, error)
	GetDelegatedInvestigation(
		int64,
	) (domain.DelegatedInvestigation, bool, error)
	TransitionDelegatedInvestigation(
		int64,
		domain.DelegatedInvestigationStatus,
		domain.DelegatedInvestigationStatus,
		string,
	) error
	BeginInvestigationMessageAction(
		context.Context,
		int64,
		string,
		string,
	) (domain.Action, bool, string, error)
	CompleteInvestigationMessageAction(
		context.Context,
		int64,
		string,
		string,
	) error
	MarkDelegatedInvestigationFinalizing(int64) error
	CompleteDelegatedInvestigation(int64) error
}

type Messenger interface {
	tools.Messenger
	ReplyAsBot(context.Context, tools.ReplyRequest) (tools.ReplyResult, error)
}

type Config struct {
	OwnerName string
	Language  string
}

type Controller struct {
	store     Store
	messenger Messenger
	config    Config
}

func New(store Store, messenger Messenger, config Config) *Controller {
	return &Controller{store: store, messenger: messenger, config: config}
}

func (c *Controller) Begin(
	ctx context.Context,
	item domain.WorkItem,
	resolution replymatch.Resolution,
) error {
	if c == nil || c.store == nil || c.messenger == nil {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation controller is not configured",
		)
	}
	investigation, _, err := c.store.BeginDelegatedInvestigation(
		domain.DelegatedInvestigation{
			WorkItemID:    item.ID,
			TaskSummary:   strings.TrimSpace(resolution.TaskSummary),
			TaskClass:     resolution.TaskClass,
			ContextCutoff: resolution.ContextCutoff,
			ContextDigest: strings.TrimSpace(resolution.ContextDigest),
			ContextMessages: append(
				[]domain.NormalizedEvent(nil),
				resolution.ContextMessages...,
			),
			Status: domain.InvestigationPendingProgress,
		},
	)
	if err != nil {
		return err
	}
	if investigation.Status == domain.InvestigationBlocked {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation is blocked and requires owner inspection",
		)
	}
	if err := c.sendOwnerNotice(ctx, item, investigation); err != nil {
		return err
	}
	if err := c.sendProgress(ctx, item, investigation); err != nil {
		return err
	}
	if investigation.Status == domain.InvestigationPendingProgress {
		return c.store.TransitionDelegatedInvestigation(
			item.ID,
			domain.InvestigationPendingProgress,
			domain.InvestigationInvestigating,
			"",
		)
	}
	return nil
}

func (c *Controller) Finalizing(_ context.Context, item domain.WorkItem) error {
	if c == nil || c.store == nil {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation controller is not configured",
		)
	}
	return c.store.MarkDelegatedInvestigationFinalizing(item.ID)
}

func (c *Controller) Complete(_ context.Context, item domain.WorkItem) error {
	if c == nil || c.store == nil {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation controller is not configured",
		)
	}
	return c.store.CompleteDelegatedInvestigation(item.ID)
}

func (c *Controller) Block(
	_ context.Context,
	item domain.WorkItem,
	runErr error,
) error {
	if c == nil || c.store == nil {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation controller is not configured",
		)
	}
	investigation, ok, err := c.store.GetDelegatedInvestigation(item.ID)
	if err != nil || !ok {
		return err
	}
	switch investigation.Status {
	case domain.InvestigationBlocked, domain.InvestigationCompleted:
		return nil
	case domain.InvestigationPendingProgress,
		domain.InvestigationInvestigating,
		domain.InvestigationFinalizing:
		return c.store.TransitionDelegatedInvestigation(
			item.ID,
			investigation.Status,
			domain.InvestigationBlocked,
			errorText(runErr),
		)
	default:
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated investigation cannot block from %s",
			investigation.Status,
		)
	}
}

func (c *Controller) sendOwnerNotice(
	ctx context.Context,
	item domain.WorkItem,
	investigation domain.DelegatedInvestigation,
) error {
	text := c.ownerNoticeText(item, investigation)
	action, shouldSend, _, err := c.store.BeginInvestigationMessageAction(
		ctx,
		item.ID,
		"owner_notice",
		text,
	)
	if err != nil {
		return err
	}
	if action.Status == domain.ActionCompleted {
		return nil
	}
	if !shouldSend {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"investigation owner-notice result is uncertain and will not be replayed",
		)
	}
	err = c.messenger.NotifyOwner(ctx, tools.NotifyRequest{
		Text:           text,
		IdempotencyKey: publicMessageUUID(action.Idempotency),
	})
	if completeErr := c.store.CompleteInvestigationMessageAction(
		context.WithoutCancel(ctx),
		action.ID,
		"",
		errorText(err),
	); completeErr != nil {
		return completeErr
	}
	return err
}

func (c *Controller) sendProgress(
	ctx context.Context,
	item domain.WorkItem,
	investigation domain.DelegatedInvestigation,
) error {
	text := c.progressText(investigation)
	action, shouldSend, _, err := c.store.BeginInvestigationMessageAction(
		ctx,
		item.ID,
		"progress",
		text,
	)
	if err != nil {
		return err
	}
	if action.Status == domain.ActionCompleted {
		return nil
	}
	if !shouldSend {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"investigation progress result is uncertain and will not be replayed",
		)
	}
	result, sendErr := c.messenger.ReplyAsBot(ctx, tools.ReplyRequest{
		MessageID:      item.Event.MessageID,
		Text:           text,
		IdempotencyKey: publicMessageUUID(action.Idempotency),
	})
	if completeErr := c.store.CompleteInvestigationMessageAction(
		context.WithoutCancel(ctx),
		action.ID,
		result.MessageID,
		errorText(sendErr),
	); completeErr != nil {
		return completeErr
	}
	return sendErr
}

func publicMessageUUID(internalKey string) string {
	digest := sha256.Sum256([]byte(internalKey))
	return fmt.Sprintf("investigation:%x", digest[:16])
}

func (c *Controller) ownerNoticeText(
	item domain.WorkItem,
	investigation domain.DelegatedInvestigation,
) string {
	name := c.ownerName()
	if c.english() {
		return fmt.Sprintf(
			"%s, the intelligent assistant started a read-only investigation for message %s: %s. It has told the sender that the work is in progress and will post a bounded evidence-based conclusion in the original thread.",
			name,
			item.Event.MessageID,
			investigation.TaskSummary,
		)
	}
	return fmt.Sprintf(
		"%s，智能助手已开始只读调查消息 %s：%s。助手会在原会话给出有证据的结论；当前无需你确认。",
		name,
		item.Event.MessageID,
		investigation.TaskSummary,
	)
}

func (c *Controller) progressText(
	investigation domain.DelegatedInvestigation,
) string {
	name := c.ownerName()
	if c.english() {
		return fmt.Sprintf(
			"Intelligent assistant: I have started a bounded read-only investigation into \"%s\" and notified %s. I will post the evidence-based result in this thread when the durable investigation closes.",
			investigation.TaskSummary,
			name,
		)
	}
	return fmt.Sprintf(
		"智能助手：我已开始只读调查“%s”，会结合当前对话和可读项目证据核对；已通知%s。持久化调查收尾后，我会在本线程补充有依据的结论。",
		investigation.TaskSummary,
		name,
	)
}

func (c *Controller) ownerName() string {
	name := strings.TrimSpace(c.config.OwnerName)
	if name != "" {
		return name
	}
	if c.english() {
		return "the owner"
	}
	return "负责人"
}

func (c *Controller) english() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.config.Language)), "en")
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Package app wires the deterministic daemon loop.
package app

import (
	"context"
	"errors"
	"time"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/poll"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Queue is the durable queue dependency used by the daemon loop.
type Queue interface {
	ClaimNext(worker string) (domain.WorkItem, bool, error)
	Complete(id int64, decision domain.Decision) error
}

type retryMarker interface {
	MarkRetry(id int64, reason string) error
}

type delayedRetryMarker interface {
	MarkRetryAfter(id int64, reason string, minimumDelay time.Duration) error
}

type deadLetterMarker interface {
	MarkDeadLetter(id int64, reason string) error
}

type fencedDeadLetterMarker interface {
	MarkDeadLetterClaim(id int64, leaseToken, reason string) error
}

type waitingUserDeferrer interface {
	DeferWaitingUserClaim(id int64, leaseToken, reason string, delay time.Duration) error
}

type schedulingStore interface {
	UpdateWorkItemSchedulingClaim(id int64, leaseToken string, kind domain.WorkKind, priority int, lease time.Duration) error
	RefreshLease(id int64, leaseToken string) error
	ValidateLease(id int64, leaseToken string) error
}

type laneQueue interface {
	ClaimNextForLane(worker string, lane domain.SchedulerLane) (domain.WorkItem, bool, error)
}

type codingGoalStore interface {
	SaveCodingGoalClaim(domain.CodingGoal, string) error
	CodingGoalBudget(workItemID int64) (used int, maxTurns int, err error)
}

type fencedCompletion interface {
	CompleteClaim(id int64, leaseToken string, decision domain.Decision) error
}

type fencedRetryMarker interface {
	MarkRetryClaim(id int64, leaseToken, reason string, minimumDelay time.Duration) error
}

type approvedReplySource interface {
	ReadyApprovedReply(workItemID int64) (domain.Decision, bool, error)
}

type postReplyNotificationStore interface {
	BeginPostReplyNotification(context.Context, string, domain.Decision) (int64, string, bool, error)
	ReadyPostReplyNotification(int64) (int64, string, domain.Decision, bool, error)
	CompletePostReplyNotification(context.Context, int64, string) error
}

// ContextBuilder builds bounded model context for a work item.
type ContextBuilder interface {
	Build(domain.WorkItem) (agentcontext.Bundle, error)
}

// Decider turns bounded context into a structured agent decision.
type Decider interface {
	Decide(context.Context, agentcontext.Bundle) (domain.Decision, error)
}

// ReplyHandler executes policy-gated replies.
type ReplyHandler interface {
	Handle(context.Context, domain.WorkItem, domain.Decision) (reply.Result, error)
}

// NotificationHandler sends a policy-selected owner notification.
type NotificationHandler interface {
	HandleNotification(context.Context, domain.WorkItem, domain.Decision, string) error
}

// Poller ingests user-visible live messages.
type Poller interface {
	Poll(context.Context) (poll.Result, error)
}

// DelegatedReplyResolver decides whether the owner already answered one exact
// delegated target using bounded same-chat context.
type DelegatedReplyResolver interface {
	Resolve(context.Context, domain.WorkItem) (replymatch.Resolution, error)
}

// OwnerActivityHandler provides transient feedback for owner-initiated work.
type OwnerActivityHandler interface {
	Begin(context.Context, domain.WorkItem) (string, error)
	End(context.Context, domain.WorkItem, string) error
}

type ownerActivityRecoverer interface {
	Recover(context.Context) error
}

// Option customizes daemon wiring.
type Option func(*Daemon)

// Daemon processes queued work items.
type Daemon struct {
	queue                Queue
	router               *router.Router
	worker               string
	leaseMaxAge          time.Duration
	builder              ContextBuilder
	decider              Decider
	replier              ReplyHandler
	notifier             NotificationHandler
	activity             OwnerActivityHandler
	poller               Poller
	workLeases           map[domain.WorkKind]time.Duration
	lane                 domain.SchedulerLane
	goalMaxTurns         int
	replyResolver        DelegatedReplyResolver
	replyConfidenceMin   float64
	replyResolutionRetry time.Duration
}

// WithCodingGoalMaxTurns bounds durable background investigations.
func WithCodingGoalMaxTurns(maxTurns int) Option {
	return func(d *Daemon) {
		if maxTurns > 0 {
			d.goalMaxTurns = maxTurns
		}
	}
}

// WithDelegatedReplyResolver wires the semantic owner-answer check that runs
// before delegated work is admitted to the main reply model.
func WithDelegatedReplyResolver(
	resolver DelegatedReplyResolver,
	confidenceMin float64,
	retryDelay time.Duration,
) Option {
	return func(d *Daemon) {
		d.replyResolver = resolver
		if confidenceMin > 0 && confidenceMin <= 1 {
			d.replyConfidenceMin = confidenceMin
		}
		if retryDelay > 0 {
			d.replyResolutionRetry = retryDelay
		}
	}
}

// Result is one daemon processing outcome.
type Result struct {
	Processed bool            `json:"processed" yaml:"processed"`
	Decision  domain.Decision `json:"decision" yaml:"decision"`
}

// TickResult is one live daemon tick: optional intake plus one scheduler claim.
type TickResult struct {
	Poll      poll.Result `json:"poll" yaml:"poll"`
	PollError string      `json:"poll_error,omitempty" yaml:"poll_error,omitempty"`
	Run       Result      `json:"run" yaml:"run"`
}

// NewDaemon constructs a daemon loop.
func NewDaemon(queue Queue, r *router.Router, opts ...Option) *Daemon {
	d := &Daemon{
		queue: queue, router: r, worker: "lark-agent",
		leaseMaxAge: 5 * time.Minute, lane: domain.SchedulerLaneAny,
		replyConfidenceMin: 0.85, replyResolutionRetry: 30 * time.Second,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// WithWorker identifies a fenced scheduler worker.
func WithWorker(worker string) Option {
	return func(d *Daemon) {
		if worker != "" {
			d.worker = worker
		}
	}
}

// WithSchedulerLane restricts claims to foreground or background work.
func WithSchedulerLane(lane domain.SchedulerLane) Option {
	return func(d *Daemon) { d.lane = lane }
}

// WithContextBuilder wires bounded context into the daemon.
func WithContextBuilder(builder ContextBuilder) Option {
	return func(d *Daemon) { d.builder = builder }
}

// WithDecider wires structured model decisions into the daemon.
func WithDecider(decider Decider) Option {
	return func(d *Daemon) { d.decider = decider }
}

// WithReplyHandler wires policy-gated reply execution into the daemon.
func WithReplyHandler(handler ReplyHandler) Option {
	return func(d *Daemon) { d.replier = handler }
}

// WithNotificationHandler wires real owner notifications.
func WithNotificationHandler(handler NotificationHandler) Option {
	return func(d *Daemon) { d.notifier = handler }
}

// WithOwnerActivityHandler wires transient owner-request feedback.
func WithOwnerActivityHandler(handler OwnerActivityHandler) Option {
	return func(d *Daemon) { d.activity = handler }
}

// WithPoller wires live user-message intake into the daemon.
func WithPoller(poller Poller) Option {
	return func(d *Daemon) { d.poller = poller }
}

// WithWorkLeases configures kind-specific scheduler lease windows.
func WithWorkLeases(leases map[domain.WorkKind]time.Duration) Option {
	return func(d *Daemon) {
		d.workLeases = make(map[domain.WorkKind]time.Duration, len(leases))
		for kind, lease := range leases {
			if lease > 0 {
				d.workLeases[kind] = lease
			}
		}
	}
}

// PollOnce ingests one live polling cycle when a poller is configured.
func (d *Daemon) PollOnce(ctx context.Context) (poll.Result, error) {
	if d.poller == nil {
		return poll.Result{}, nil
	}
	return d.poller.Poll(ctx)
}

// RunTick performs one live tick. Poll errors are reported but do not starve
// already queued work.
func (d *Daemon) RunTick(ctx context.Context, live bool) (TickResult, error) {
	var result TickResult
	if live {
		pollResult, err := d.PollOnce(ctx)
		if err != nil {
			result.PollError = err.Error()
		} else {
			result.Poll = pollResult
		}
	}
	run, err := d.RunOnce(ctx)
	result.Run = run
	if err != nil {
		return result, err
	}
	return result, nil
}

// RunOnce claims and routes one work item. Long-running daemon run calls this
// repeatedly with sleeps and cancellation around it.
func (d *Daemon) RunOnce(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if recovery, ok := d.activity.(ownerActivityRecoverer); ok {
		_ = recovery.Recover(ctx)
	}
	var item domain.WorkItem
	var ok bool
	var err error
	if q, supported := d.queue.(laneQueue); supported && d.lane != domain.SchedulerLaneAny {
		item, ok, err = q.ClaimNextForLane(d.worker, d.lane)
	} else {
		item, ok, err = d.queue.ClaimNext(d.worker)
	}
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{Processed: false}, nil
	}
	if notifications, ok := d.queue.(postReplyNotificationStore); ok {
		_, _, pending, found, err := notifications.ReadyPostReplyNotification(item.ID)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if found {
			return d.finishPostReplyNotification(ctx, item, pending, notifications)
		}
	}
	if source, ok := d.queue.(approvedReplySource); ok {
		approved, found, err := source.ReadyApprovedReply(item.ID)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if found {
			return d.finishDecision(ctx, item, approved)
		}
	}
	decision, err := d.router.Route(ctx, item)
	if err != nil {
		d.markRetry(item, err)
		return Result{}, err
	}
	if scheduler, ok := d.queue.(schedulingStore); ok {
		lease := leaseForWorkKind(decision.WorkKind, d.leaseMaxAge, d.workLeases)
		if err := scheduler.UpdateWorkItemSchedulingClaim(
			item.ID, item.LeaseBy, decision.WorkKind, decision.Priority, lease,
		); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if err := scheduler.RefreshLease(item.ID, item.LeaseBy); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		item.WorkKind = decision.WorkKind
		item.Priority = decision.Priority
		runCtx, stopHeartbeat := d.startLeaseHeartbeat(ctx, scheduler, item.ID, item.LeaseBy, lease)
		defer stopHeartbeat()
		ctx = runCtx
	}
	if isDelegatedReply(decision.Relevance) && d.replyResolver != nil {
		resolution, resolveErr := d.replyResolver.Resolve(ctx, item)
		if resolveErr != nil {
			return d.deferDelegatedReply(
				item,
				decision,
				"owner_reply_resolution_failed",
				d.replyResolutionRetry,
			)
		}
		if resolution.Confidence < d.replyConfidenceMin ||
			resolution.Result == replymatch.ResultAmbiguous {
			delay := d.replyResolutionRetry
			if resolution.RetryAfter > delay {
				delay = resolution.RetryAfter
			}
			return d.deferDelegatedReply(item, decision, "owner_reply_ambiguous", delay)
		}
		switch resolution.Result {
		case replymatch.ResultAnswered:
			decision.Kind = domain.DecisionIgnore
			decision.Reason = "owner_semantically_replied"
			return d.finishDecision(ctx, item, decision)
		case replymatch.ResultNoReplyNeeded:
			decision.Kind = domain.DecisionIgnore
			decision.Reason = "delegated_reply_not_needed"
			return d.finishDecision(ctx, item, decision)
		case replymatch.ResultWithdrawn:
			decision.Kind = domain.DecisionIgnore
			decision.Reason = "message_withdrawn"
			return d.finishDecision(ctx, item, decision)
		case replymatch.ResultUnanswered:
			// Continue into the ordinary read-only delegated reply workflow.
		default:
			return d.deferDelegatedReply(
				item,
				decision,
				"owner_reply_resolution_invalid",
				d.replyResolutionRetry,
			)
		}
	}
	if isAssistantFacingRequest(decision.Relevance) && d.activity != nil {
		token, activityErr := d.activity.Begin(ctx, item)
		if activityErr == nil && token != "" {
			defer func() {
				_ = d.activity.End(context.WithoutCancel(ctx), item, token)
			}()
		}
	}
	goalTurnsRemaining := 0
	goalBudgetExhausted := false
	if item.WorkKind == domain.WorkKindCodingGoal {
		if goals, ok := d.queue.(codingGoalStore); ok {
			goal, err := domain.NewCodingGoal(domain.CodingGoalSpec{
				WorkItemID:            item.ID,
				OriginalMessageID:     item.Event.MessageID,
				Question:              item.Event.Content,
				CompletionConditions:  []string{"answer the owner's coding request with verified evidence"},
				BlockingConditions:    []string{"required code, access, or evidence is unavailable"},
				MaxInvestigationTurns: d.goalMaxTurns,
			})
			if err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			if err := goals.SaveCodingGoalClaim(goal, item.LeaseBy); err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			used, maxTurns, err := goals.CodingGoalBudget(item.ID)
			if err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			goalTurnsRemaining = maxTurns - used
			goalBudgetExhausted = goalTurnsRemaining <= 0
		}
	}
	if goalBudgetExhausted {
		decision.Kind = domain.DecisionReply
		decision.Reason = appendReason(decision.Reason, "coding_goal_turn_budget_exhausted")
		decision.ReplyText = "CodingGoal 调查预算已耗尽；请缩小问题范围，或明确批准继续调查。"
		decision.OwnerAction = "CodingGoal 调查预算已耗尽，需要缩小范围或明确批准继续。"
	} else if d.shouldAskModel(decision) {
		bundle, err := d.builder.Build(item)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if goalTurnsRemaining > 0 {
			bundle.MaxTurns = goalTurnsRemaining
		}
		modelDecision, err := d.decider.Decide(ctx, bundle)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		decision = inheritRouteFields(modelDecision, decision)
	}
	if decision.Kind == domain.DecisionNotify && isAssistantFacingRequest(decision.Relevance) {
		err := errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"assistant-facing request cannot finish as notify only; submit reply_text or request_approval",
		)
		d.markRetry(item, err)
		return Result{}, err
	}
	return d.finishDecision(ctx, item, decision)
}

func (d *Daemon) deferDelegatedReply(
	item domain.WorkItem,
	route domain.Decision,
	reason string,
	delay time.Duration,
) (Result, error) {
	deferrer, ok := d.queue.(waitingUserDeferrer)
	if !ok {
		err := errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"queue does not support deferred delegated replies",
		)
		d.markRetry(item, err)
		return Result{}, err
	}
	if err := deferrer.DeferWaitingUserClaim(
		item.ID,
		item.LeaseBy,
		reason,
		delay,
	); err != nil {
		return Result{}, err
	}
	return Result{
		Processed: true,
		Decision: domain.Decision{
			Kind:      domain.DecisionRecord,
			Relevance: route.Relevance,
			WorkKind:  route.WorkKind,
			Priority:  route.Priority,
			Reason:    reason,
		},
	}, nil
}

func isDelegatedReply(relevance domain.Relevance) bool {
	return relevance == domain.RelevanceDirectMention ||
		relevance == domain.RelevancePrivateMessage
}

func (d *Daemon) startLeaseHeartbeat(
	parent context.Context,
	store schedulingStore,
	workItemID int64,
	leaseToken string,
	lease time.Duration,
) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	interval := lease / 3
	if interval <= 0 || interval > 10*time.Second {
		interval = 10 * time.Second
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if err := store.RefreshLease(workItemID, leaseToken); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel()
		close(stop)
	}
}

func (d *Daemon) finishDecision(ctx context.Context, item domain.WorkItem, decision domain.Decision) (Result, error) {
	if err := d.ensureLease(ctx, item); err != nil {
		d.markRetry(item, err)
		return Result{}, err
	}
	if (decision.Kind == domain.DecisionReply || decision.Kind == domain.DecisionRequestApproval) && d.replier == nil {
		err := errs.NewInternalError(errs.SubtypeFailedPrecondition, "reply handler is not configured")
		d.markRetry(item, err)
		return Result{}, err
	}
	if decision.Kind == domain.DecisionReply || decision.Kind == domain.DecisionRequestApproval {
		if err := d.ensureLease(ctx, item); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		actionDecision := decision
		if decision.Kind == domain.DecisionRequestApproval {
			actionDecision.Kind = domain.DecisionReply
			actionDecision.Confidence = 0
		}
		replyResult, err := d.replier.Handle(ctx, item, actionDecision)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		switch replyResult.Action.Status {
		case domain.ActionCompleted:
			decision.Kind = domain.DecisionReply
		case domain.ActionAwaitingApproval:
			decision.Kind = domain.DecisionRequestApproval
			decision.Reason = appendReason(decision.Reason, replyResult.Action.CancelReason)
		case domain.ActionCancelled, domain.ActionBlocked:
			decision.Kind = domain.DecisionIgnore
			decision.Reason = appendReason(decision.Reason, replyResult.Action.CancelReason)
		default:
			return Result{}, errs.NewInternalError(errs.SubtypeUnknown, "unexpected reply action status: %s", replyResult.Action.Status)
		}
	}
	if decision.Kind == domain.DecisionReply &&
		!isAssistantFacingRequest(decision.Relevance) &&
		d.notifier != nil {
		if notifications, ok := d.queue.(postReplyNotificationStore); ok {
			return d.finishPostReplyNotification(ctx, item, decision, notifications)
		}
		if err := d.notifier.HandleNotification(ctx, item, decision, ""); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
	}
	if decision.Kind == domain.DecisionNotify && d.notifier != nil {
		if err := d.notifier.HandleNotification(ctx, item, decision, ""); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
	}
	if err := d.complete(item, decision); err != nil {
		return Result{}, err
	}
	return Result{Processed: true, Decision: decision}, nil
}

func (d *Daemon) ensureLease(ctx context.Context, item domain.WorkItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store, ok := d.queue.(schedulingStore); ok && item.LeaseBy != "" {
		return store.ValidateLease(item.ID, item.LeaseBy)
	}
	return nil
}

func (d *Daemon) finishPostReplyNotification(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	notifications postReplyNotificationStore,
) (Result, error) {
	actionID, key, completed, err := notifications.BeginPostReplyNotification(ctx, item.DedupKey, decision)
	if err != nil {
		d.markRetry(item, err)
		return Result{}, err
	}
	if !completed {
		if d.notifier == nil {
			err := errs.NewInternalError(errs.SubtypeUnknown, "post-reply owner notifier is not configured")
			_ = notifications.CompletePostReplyNotification(context.WithoutCancel(ctx), actionID, err.Error())
			d.markRetry(item, err)
			return Result{}, err
		}
		if err := d.ensureLease(ctx, item); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if err := d.notifier.HandleNotification(ctx, item, decision, key); err != nil {
			_ = notifications.CompletePostReplyNotification(context.WithoutCancel(ctx), actionID, err.Error())
			d.markRetry(item, err)
			return Result{}, err
		}
		if err := notifications.CompletePostReplyNotification(ctx, actionID, ""); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
	}
	if err := d.complete(item, decision); err != nil {
		return Result{}, err
	}
	return Result{Processed: true, Decision: decision}, nil
}

func (d *Daemon) complete(item domain.WorkItem, decision domain.Decision) error {
	if q, ok := d.queue.(fencedCompletion); ok && item.LeaseBy != "" {
		return q.CompleteClaim(item.ID, item.LeaseBy, decision)
	}
	return d.queue.Complete(item.ID, decision)
}

func (d *Daemon) markRetry(item domain.WorkItem, runErr error) {
	if problem, ok := errs.ProblemOf(runErr); ok &&
		problem.Subtype == errs.SubtypeModelNonConvergence {
		if item.LeaseBy != "" {
			if q, ok := d.queue.(fencedDeadLetterMarker); ok {
				_ = q.MarkDeadLetterClaim(item.ID, item.LeaseBy, runErr.Error())
				return
			}
		}
		if q, ok := d.queue.(deadLetterMarker); ok {
			_ = q.MarkDeadLetter(item.ID, runErr.Error())
			return
		}
	}
	if q, ok := d.queue.(fencedRetryMarker); ok && item.LeaseBy != "" {
		minimumDelay := time.Duration(0)
		var retryAfter interface{ RetryAfter() time.Duration }
		if errors.As(runErr, &retryAfter) {
			minimumDelay = retryAfter.RetryAfter()
		}
		_ = q.MarkRetryClaim(item.ID, item.LeaseBy, runErr.Error(), minimumDelay)
		return
	}
	var retryAfter interface{ RetryAfter() time.Duration }
	if errors.As(runErr, &retryAfter) {
		if q, ok := d.queue.(delayedRetryMarker); ok {
			_ = q.MarkRetryAfter(item.ID, runErr.Error(), retryAfter.RetryAfter())
			return
		}
	}
	if q, ok := d.queue.(retryMarker); ok {
		_ = q.MarkRetry(item.ID, runErr.Error())
	}
}

func appendReason(reason, actionReason string) string {
	if actionReason == "" {
		return reason
	}
	if reason == "" {
		return actionReason
	}
	return reason + "; " + actionReason
}

func (d *Daemon) shouldAskModel(decision domain.Decision) bool {
	if d.builder == nil || d.decider == nil {
		return false
	}
	switch decision.Kind {
	case domain.DecisionNotify, domain.DecisionRecord, domain.DecisionRequestApproval:
		return true
	default:
		return false
	}
}

func inheritRouteFields(modelDecision, routeDecision domain.Decision) domain.Decision {
	if modelDecision.Mode == "" {
		modelDecision.Mode = routeDecision.Mode
	}
	if routeDecision.Relevance == domain.RelevanceDirectMention || isAssistantFacingRequest(routeDecision.Relevance) {
		modelDecision.Relevance = routeDecision.Relevance
	} else if modelDecision.Relevance == "" || modelDecision.Relevance == domain.RelevanceNone {
		modelDecision.Relevance = routeDecision.Relevance
	}
	if modelDecision.Reason == "" {
		modelDecision.Reason = routeDecision.Reason
	}
	if modelDecision.Risk == "" {
		modelDecision.Risk = routeDecision.Risk
	}
	if modelDecision.WorkKind == "" || modelDecision.WorkKind == domain.WorkKindGeneric {
		modelDecision.WorkKind = routeDecision.WorkKind
	}
	if modelDecision.Priority == 0 {
		modelDecision.Priority = routeDecision.Priority
	}
	return modelDecision
}

func isAssistantFacingRequest(relevance domain.Relevance) bool {
	return relevance == domain.RelevanceOwnerRequest ||
		relevance == domain.RelevanceAssistantRequest
}

func leaseForWorkKind(kind domain.WorkKind, fallback time.Duration, configured map[domain.WorkKind]time.Duration) time.Duration {
	if configured != nil {
		if lease := configured[kind]; lease > 0 {
			return lease
		}
	}
	switch kind {
	case domain.WorkKindFastPath:
		return time.Minute
	case domain.WorkKindSimpleQuestion, domain.WorkKindDirectMention:
		return 5 * time.Minute
	case domain.WorkKindCodingQuestion:
		return 30 * time.Minute
	case domain.WorkKindCodingGoal:
		return 2 * time.Hour
	default:
		if fallback > 0 {
			return fallback
		}
		return 5 * time.Minute
	}
}

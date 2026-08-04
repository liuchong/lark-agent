// Package app wires the deterministic daemon loop.
package app

import (
	"context"
	"errors"
	"strings"
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

type workItemReader interface {
	GetWorkItem(context.Context, int64) (domain.WorkItem, error)
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

type approvedReplyBlocker interface {
	BlockReadyReplyApprovalClaim(
		context.Context,
		int64,
		string,
		string,
	) error
}

type replyCandidateStore interface {
	ReadyWorkReplyCandidate(workItemID int64) (domain.WorkReplyCandidate, bool, error)
	SaveWorkReplyCandidate(workItemID int64, leaseToken string, decision domain.Decision) error
	HoldWorkReplyCandidate(workItemID int64, leaseToken, reason string) error
	ConsumeWorkReplyCandidate(workItemID int64, leaseToken string) error
	CancelWorkReplyCandidate(workItemID int64, leaseToken, reason string) error
}

type replyCandidateCompleter interface {
	CompleteReplyCandidateClaim(id int64, leaseToken string, decision domain.Decision) error
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

type approvalNotificationHandler interface {
	HandleApprovalNotification(
		context.Context,
		domain.WorkItem,
		domain.Decision,
		domain.Action,
	) error
}

type replyApprovalPredictor interface {
	RequiresApproval(domain.Decision) bool
}

type replyPreflighter interface {
	Preflight(context.Context, domain.WorkItem, domain.Decision) (domain.Action, error)
}

// TerminalFailureHandler sends one durable owner resolution summary after a
// work item actually reaches dead letter.
type TerminalFailureHandler interface {
	HandleTerminalFailure(context.Context, domain.WorkItem, error) error
}

// DecisionPresenter renders deterministic identity/language text before the
// exact reply draft is persisted or sent.
type DecisionPresenter interface {
	Present(domain.WorkItem, domain.Decision) (domain.Decision, error)
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

// InvestigationProgressHandler owns the durable progress/finalization state
// for contextual delegated investigations.
type InvestigationProgressHandler interface {
	Begin(context.Context, domain.WorkItem, replymatch.Resolution) error
	Finalizing(context.Context, domain.WorkItem) error
	Complete(context.Context, domain.WorkItem) error
	Block(context.Context, domain.WorkItem, error) error
}

// OwnerActivityHandler provides transient feedback for owner-initiated work.
type OwnerActivityHandler interface {
	Begin(context.Context, domain.WorkItem) (string, error)
	End(context.Context, domain.WorkItem, string) error
}

// ControlHandler executes one already-authorized owner-private command.
type ControlHandler interface {
	Handle(
		context.Context,
		domain.WorkItem,
		domain.OwnerControlCommand,
	) (domain.Decision, error)
}

type SemanticControlKind = domain.SemanticControlKind

const (
	SemanticControlNotCommand = domain.SemanticControlNotCommand
	SemanticControlCommand    = domain.SemanticControlCommand
	SemanticControlAmbiguous  = domain.SemanticControlAmbiguous
)

// SemanticControlResolution is a constrained owner-private command decision.
type SemanticControlResolution = domain.SemanticControlResolution

// SemanticControlResolver maps contextual owner-private language to one typed
// command without granting the general answer model control authority.
type SemanticControlResolver interface {
	Resolve(
		context.Context,
		domain.WorkItem,
		agentcontext.Bundle,
	) (domain.SemanticControlResolution, error)
}

type ownerActivityRecoverer interface {
	Recover(context.Context) error
}

// Option customizes daemon wiring.
type Option func(*Daemon)

// Daemon processes queued work items.
type Daemon struct {
	queue                 Queue
	router                *router.Router
	worker                string
	leaseMaxAge           time.Duration
	builder               ContextBuilder
	decider               Decider
	replier               ReplyHandler
	notifier              NotificationHandler
	terminalFailure       TerminalFailureHandler
	presenter             DecisionPresenter
	activity              OwnerActivityHandler
	control               ControlHandler
	semanticControl       SemanticControlResolver
	poller                Poller
	workLeases            map[domain.WorkKind]time.Duration
	lane                  domain.SchedulerLane
	goalMaxTurns          int
	replyResolver         DelegatedReplyResolver
	replyConfidenceMin    float64
	replyResolutionRetry  time.Duration
	investigationProgress InvestigationProgressHandler
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

// WithInvestigationProgressHandler enables durable staged delegated replies.
func WithInvestigationProgressHandler(handler InvestigationProgressHandler) Option {
	return func(d *Daemon) {
		d.investigationProgress = handler
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

// WithTerminalFailureHandler wires durable owner summaries for dead letters.
func WithTerminalFailureHandler(handler TerminalFailureHandler) Option {
	return func(d *Daemon) { d.terminalFailure = handler }
}

// WithDecisionPresenter wires deterministic outward reply presentation.
func WithDecisionPresenter(presenter DecisionPresenter) Option {
	return func(d *Daemon) { d.presenter = presenter }
}

// WithOwnerActivityHandler wires transient owner-request feedback.
func WithOwnerActivityHandler(handler OwnerActivityHandler) Option {
	return func(d *Daemon) { d.activity = handler }
}

func WithControlHandler(handler ControlHandler) Option {
	return func(d *Daemon) { d.control = handler }
}

// WithSemanticControlResolver enables contextual owner-private commands.
func WithSemanticControlResolver(resolver SemanticControlResolver) Option {
	return func(d *Daemon) { d.semanticControl = resolver }
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
			return d.finishApprovedDecision(ctx, item, approved)
		}
	}
	if candidates, supported := d.queue.(replyCandidateStore); supported {
		candidate, found, err := candidates.ReadyWorkReplyCandidate(item.ID)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if found {
			currentRoute, routeErr := d.router.Route(ctx, item)
			if routeErr != nil {
				d.markRetry(item, routeErr)
				return Result{}, routeErr
			}
			if currentRoute.Kind != domain.DecisionNotify ||
				!isDelegatedReply(currentRoute.Relevance) {
				if cancelErr := candidates.CancelWorkReplyCandidate(
					item.ID,
					item.LeaseBy,
					"current routing policy no longer permits the held candidate",
				); cancelErr != nil {
					d.markRetry(item, cancelErr)
					return Result{}, cancelErr
				}
				return d.finishDecision(ctx, item, currentRoute)
			}
			return d.resolveHeldReplyCandidate(ctx, item, candidate, candidates)
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
	if isDelegatedReply(decision.Relevance) &&
		d.replyResolver != nil &&
		!item.InvestigationActive {
		resolution, resolveErr := d.replyResolver.Resolve(ctx, item)
		if resolveErr != nil {
			return d.deferDelegatedReply(
				item,
				decision,
				"owner_reply_resolution_failed",
				d.replyResolutionRetry,
			)
		}
		if semanticSuppressesDelegatedReply(resolution, d.replyConfidenceMin) {
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
			}
		}
		if resolution.Confidence < d.replyConfidenceMin ||
			resolution.Result == replymatch.ResultAmbiguous {
			delay := d.replyResolutionRetry
			if resolution.RetryAfter > delay {
				delay = resolution.RetryAfter
			}
			return d.deferDelegatedReply(
				item,
				decision,
				delegatedReplyAmbiguousReason("owner_reply_ambiguous", resolution),
				delay,
			)
		}
		switch resolution.Result {
		case replymatch.ResultUnanswered:
			if resolution.ClassificationConfidence < d.replyConfidenceMin {
				return d.deferDelegatedReply(
					item,
					decision,
					"delegated_task_classification_ambiguous",
					d.replyResolutionRetry,
				)
			}
			item.TaskSummary = resolution.TaskSummary
			item.TaskClass = resolution.TaskClass
			item.ContextCutoff = resolution.ContextCutoff
			item.ContextDigest = resolution.ContextDigest
			item.ResolvedContext = append(
				[]domain.NormalizedEvent(nil),
				resolution.ContextMessages...,
			)
			switch resolution.TaskClass {
			case domain.TaskClassCoding:
				item.WorkKind = domain.WorkKindCodingQuestion
				decision.WorkKind = domain.WorkKindCodingQuestion
				decision.Priority = domain.PriorityCodingQuestion
			case domain.TaskClassInvestigation, domain.TaskClassSimple:
			default:
				return d.deferDelegatedReply(
					item,
					decision,
					"delegated_task_classification_invalid",
					d.replyResolutionRetry,
				)
			}
			if scheduler, ok := d.queue.(schedulingStore); ok {
				lease := leaseForWorkKind(
					decision.WorkKind,
					d.leaseMaxAge,
					d.workLeases,
				)
				if err := scheduler.UpdateWorkItemSchedulingClaim(
					item.ID,
					item.LeaseBy,
					decision.WorkKind,
					decision.Priority,
					lease,
				); err != nil {
					d.markRetry(item, err)
					return Result{}, err
				}
			}
			if resolution.RequiresProgress && d.investigationProgress != nil {
				if err := d.investigationProgress.Begin(ctx, item, resolution); err != nil {
					d.markRetry(item, err)
					return Result{}, err
				}
				item.InvestigationActive = true
			}
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
	if decision.WorkKind == domain.WorkKindOwnerControl {
		if d.control == nil || decision.ControlCommand == nil {
			err := errs.NewInternalError(
				errs.SubtypeFailedPrecondition,
				"owner control handler is not configured",
			)
			d.markRetry(item, err)
			return Result{}, err
		}
		controlDecision, err := d.control.Handle(ctx, item, *decision.ControlCommand)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		controlDecision = inheritRouteFields(controlDecision, decision)
		return d.finishDecision(ctx, item, controlDecision)
	}
	var preparedBundle *agentcontext.Bundle
	if decision.Relevance == domain.RelevanceOwnerRequest &&
		d.semanticControl != nil &&
		d.builder != nil {
		bundle, err := d.builder.Build(item)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		preparedBundle = &bundle
		resolution, err := d.semanticControl.Resolve(ctx, item, bundle)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		switch resolution.Kind {
		case SemanticControlNotCommand:
		case SemanticControlCommand:
			if d.control == nil || resolution.Command == nil {
				err := errs.NewInternalError(
					errs.SubtypeFailedPrecondition,
					"semantic owner control handler is not configured",
				)
				d.markRetry(item, err)
				return Result{}, err
			}
			controlDecision, err := d.control.Handle(ctx, item, *resolution.Command)
			if err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			controlDecision = inheritRouteFields(controlDecision, decision)
			controlDecision.WorkKind = domain.WorkKindOwnerControl
			return d.finishDecision(ctx, item, controlDecision)
		case SemanticControlAmbiguous:
			text := strings.TrimSpace(resolution.Clarification)
			if text == "" {
				text = "这句话可能对应多项操作，请说明要处理的任务号或动作号。"
			}
			return d.finishDecision(ctx, item, inheritRouteFields(domain.Decision{
				Kind:       domain.DecisionReply,
				Confidence: 1,
				Risk:       domain.RiskLow,
				Reason:     "semantic_owner_control_ambiguous",
				ReplyText:  text,
			}, decision))
		default:
			err := errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"invalid semantic control resolution %q",
				resolution.Kind,
			)
			d.markRetry(item, err)
			return Result{}, err
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
		var bundle agentcontext.Bundle
		if preparedBundle != nil {
			bundle = *preparedBundle
		} else {
			var err error
			bundle, err = d.builder.Build(item)
			if err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
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
	var candidates replyCandidateStore
	candidateSaved := false
	if item.InvestigationActive && decision.Kind == domain.DecisionReply {
		var supported bool
		candidates, supported = d.queue.(replyCandidateStore)
		if supported {
			if err := candidates.SaveWorkReplyCandidate(item.ID, item.LeaseBy, decision); err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			candidateSaved = true
		}
	}
	if item.InvestigationActive && d.replyResolver != nil {
		latest, err := d.replyResolver.Resolve(ctx, item)
		if err != nil {
			if candidateSaved {
				if holdErr := candidates.HoldWorkReplyCandidate(
					item.ID,
					item.LeaseBy,
					"investigation_final_resolution_failed",
				); holdErr != nil {
					d.markRetry(item, holdErr)
					return Result{}, holdErr
				}
				return d.deferDelegatedReply(
					item,
					decision,
					"investigation_final_resolution_failed",
					d.replyResolutionRetry,
				)
			}
			d.markRetry(item, err)
			return Result{}, err
		}
		if semanticSuppressesDelegatedReply(latest, d.replyConfidenceMin) {
			switch latest.Result {
			case replymatch.ResultAnswered:
				if candidateSaved {
					if err := candidates.CancelWorkReplyCandidate(
						item.ID,
						item.LeaseBy,
						"owner answered",
					); err != nil {
						d.markRetry(item, err)
						return Result{}, err
					}
				}
				decision.Kind = domain.DecisionIgnore
				decision.Reason = "owner_semantically_replied_during_investigation"
				decision.ReplyText = ""
				decision.OwnerAction = ""
			case replymatch.ResultNoReplyNeeded:
				if candidateSaved {
					if err := candidates.CancelWorkReplyCandidate(
						item.ID,
						item.LeaseBy,
						"reply no longer needed",
					); err != nil {
						d.markRetry(item, err)
						return Result{}, err
					}
				}
				decision.Kind = domain.DecisionIgnore
				decision.Reason = "delegated_reply_no_longer_needed_during_investigation"
				decision.ReplyText = ""
				decision.OwnerAction = ""
			case replymatch.ResultWithdrawn:
				if candidateSaved {
					if err := candidates.CancelWorkReplyCandidate(
						item.ID,
						item.LeaseBy,
						"source message withdrawn",
					); err != nil {
						d.markRetry(item, err)
						return Result{}, err
					}
				}
				decision.Kind = domain.DecisionIgnore
				decision.Reason = "message_withdrawn_during_investigation"
				decision.ReplyText = ""
				decision.OwnerAction = ""
			}
		} else {
			if latest.Confidence < d.replyConfidenceMin ||
				latest.Result == replymatch.ResultAmbiguous {
				reason := delegatedReplyAmbiguousReason(
					"investigation_final_context_ambiguous",
					latest,
				)
				if candidateSaved {
					if err := candidates.HoldWorkReplyCandidate(
						item.ID,
						item.LeaseBy,
						reason,
					); err != nil {
						d.markRetry(item, err)
						return Result{}, err
					}
				}
				return d.deferDelegatedReply(
					item,
					decision,
					reason,
					d.replyResolutionRetry,
				)
			}
			switch latest.Result {
			case replymatch.ResultUnanswered:
			default:
				if candidateSaved {
					if err := candidates.HoldWorkReplyCandidate(
						item.ID,
						item.LeaseBy,
						"investigation_final_context_invalid",
					); err != nil {
						d.markRetry(item, err)
						return Result{}, err
					}
				}
				return d.deferDelegatedReply(
					item,
					decision,
					"investigation_final_context_invalid",
					d.replyResolutionRetry,
				)
			}
		}
	}
	if item.InvestigationActive && d.investigationProgress != nil {
		if err := d.investigationProgress.Finalizing(ctx, item); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
	}
	if candidateSaved && decision.Kind == domain.DecisionReply {
		return d.finishCandidateDecision(ctx, item, decision, candidates)
	}
	return d.finishDecision(ctx, item, decision)
}

func (d *Daemon) resolveHeldReplyCandidate(
	ctx context.Context,
	item domain.WorkItem,
	candidate domain.WorkReplyCandidate,
	candidates replyCandidateStore,
) (Result, error) {
	if d.replyResolver == nil {
		err := errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"held reply candidate requires delegated reply resolver",
		)
		if !d.markPermanentFailure(item, err) {
			d.markRetry(item, err)
		}
		return Result{}, err
	}
	resolution, err := d.replyResolver.Resolve(ctx, item)
	if err != nil {
		if holdErr := candidates.HoldWorkReplyCandidate(
			item.ID,
			item.LeaseBy,
			"candidate_resolution_failed",
		); holdErr != nil {
			d.markRetry(item, holdErr)
			return Result{}, holdErr
		}
		return d.deferDelegatedReply(
			item,
			candidate.Decision,
			"candidate_resolution_failed",
			d.replyResolutionRetry,
		)
	}
	if semanticSuppressesDelegatedReply(resolution, d.replyConfidenceMin) {
		if err := candidates.CancelWorkReplyCandidate(
			item.ID,
			item.LeaseBy,
			"candidate no longer needs a sender-facing reply: "+string(resolution.Result),
		); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		decision := candidate.Decision
		decision.Kind = domain.DecisionIgnore
		decision.ReplyText = ""
		decision.OwnerAction = ""
		decision.Reason = "held_candidate_" + string(resolution.Result)
		return d.finishDecision(ctx, item, decision)
	}
	if resolution.Confidence < d.replyConfidenceMin ||
		resolution.Result == replymatch.ResultAmbiguous {
		delay := d.replyResolutionRetry
		if resolution.RetryAfter > delay {
			delay = resolution.RetryAfter
		}
		reason := delegatedReplyAmbiguousReason("candidate_context_ambiguous", resolution)
		if err := candidates.HoldWorkReplyCandidate(
			item.ID,
			item.LeaseBy,
			reason,
		); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		return d.deferDelegatedReply(
			item,
			candidate.Decision,
			reason,
			delay,
		)
	}
	switch resolution.Result {
	case replymatch.ResultUnanswered:
		return d.finishCandidateDecision(ctx, item, candidate.Decision, candidates)
	default:
		if err := candidates.HoldWorkReplyCandidate(
			item.ID,
			item.LeaseBy,
			"candidate_context_invalid",
		); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		return d.deferDelegatedReply(
			item,
			candidate.Decision,
			"candidate_context_invalid",
			d.replyResolutionRetry,
		)
	}
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
	d.notifyTerminalFailure(
		item,
		errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"delegated reply context did not converge before the retry ceiling: %s",
			reason,
		),
	)
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

func delegatedReplyAmbiguousReason(
	fallback string,
	resolution replymatch.Resolution,
) string {
	if strings.Contains(strings.ToLower(resolution.Reason), "owner reaction read failed") {
		return "owner_reaction_read_failed"
	}
	return fallback
}

func semanticSuppressesDelegatedReply(
	resolution replymatch.Resolution,
	configuredMin float64,
) bool {
	if resolution.Confidence < semanticSuppressionConfidenceMin(configuredMin) {
		return false
	}
	switch resolution.Result {
	case replymatch.ResultAnswered:
		return len(resolution.MatchedOwnerMessageIDs) > 0 ||
			resolution.OwnerAckReaction != nil
	case replymatch.ResultNoReplyNeeded, replymatch.ResultWithdrawn:
		return true
	default:
		return false
	}
}

func semanticSuppressionConfidenceMin(configuredMin float64) float64 {
	const defaultSuppressionFloor = 0.70
	return defaultSuppressionFloor
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
	return d.finishDecisionWithState(ctx, item, decision, false, nil)
}

func (d *Daemon) finishApprovedDecision(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
) (Result, error) {
	return d.finishDecisionWithState(ctx, item, decision, true, nil)
}

func (d *Daemon) finishCandidateDecision(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	candidates replyCandidateStore,
) (Result, error) {
	return d.finishDecisionWithState(ctx, item, decision, false, candidates)
}

func (d *Daemon) finishDecisionWithState(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	approvalGranted bool,
	candidates replyCandidateStore,
) (Result, error) {
	if err := d.ensureLease(ctx, item); err != nil {
		d.markRetry(item, err)
		return Result{}, err
	}
	if isSenderFacingDecision(decision) {
		currentRoute, err := d.router.Route(ctx, item)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		if !routeAllowsSenderFacingDecision(currentRoute) {
			if approvalGranted {
				blocker, ok := d.queue.(approvedReplyBlocker)
				if !ok {
					err := errs.NewInternalError(
						errs.SubtypeFailedPrecondition,
						"approved reply blocker is not configured",
					)
					d.markRetry(item, err)
					return Result{}, err
				}
				if err := blocker.BlockReadyReplyApprovalClaim(
					ctx,
					item.ID,
					item.LeaseBy,
					currentRoute.Reason,
				); err != nil {
					d.markRetry(item, err)
					return Result{}, err
				}
			}
			currentRoute.Reason = appendReason(
				currentRoute.Reason,
				"sender_facing_reply_blocked_by_current_route",
			)
			decision = currentRoute
		}
	}
	if (decision.Kind == domain.DecisionReply || decision.Kind == domain.DecisionRequestApproval) && d.replier == nil {
		err := errs.NewInternalError(errs.SubtypeFailedPrecondition, "reply handler is not configured")
		d.markRetry(item, err)
		return Result{}, err
	}
	if (decision.Kind == domain.DecisionReply || decision.Kind == domain.DecisionRequestApproval) &&
		d.presenter != nil {
		presented, err := d.presenter.Present(item, decision)
		if err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
		decision = presented
	}
	ownerNotified := false
	if decision.Kind == domain.DecisionReply &&
		!isAssistantFacingRequest(decision.Relevance) &&
		(approvalGranted || !replyRequiresApproval(d.replier, decision)) &&
		d.notifier != nil {
		notifyReady := true
		if preflighter, ok := d.replier.(replyPreflighter); ok {
			action, err := preflighter.Preflight(ctx, item, decision)
			if err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			notifyReady = action.Status == domain.ActionReady ||
				(approvalGranted && action.Status == domain.ActionAwaitingApproval)
		}
		if notifyReady {
			if notifications, ok := d.queue.(postReplyNotificationStore); ok {
				if err := d.ensureOwnerNotification(ctx, item, decision, notifications); err != nil {
					d.markRetry(item, err)
					return Result{}, err
				}
			} else if err := d.notifier.HandleNotification(ctx, item, decision, ""); err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
			ownerNotified = true
		}
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
			if err := d.notifyReplyApproval(ctx, item, decision, replyResult.Action); err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
		case domain.ActionCancelled, domain.ActionBlocked:
			decision.Kind = domain.DecisionIgnore
			decision.Reason = appendReason(decision.Reason, replyResult.Action.CancelReason)
		default:
			return Result{}, errs.NewInternalError(errs.SubtypeUnknown, "unexpected reply action status: %s", replyResult.Action.Status)
		}
	}
	if decision.Kind == domain.DecisionReply &&
		!isAssistantFacingRequest(decision.Relevance) &&
		d.notifier != nil &&
		!ownerNotified {
		if notifications, ok := d.queue.(postReplyNotificationStore); ok {
			if err := d.ensureOwnerNotification(ctx, item, decision, notifications); err != nil {
				d.markRetry(item, err)
				return Result{}, err
			}
		} else if err := d.notifier.HandleNotification(ctx, item, decision, ""); err != nil {
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
	if d.investigationProgress != nil &&
		(decision.Kind == domain.DecisionReply ||
			decision.Kind == domain.DecisionIgnore) {
		if err := d.investigationProgress.Complete(ctx, item); err != nil {
			d.markRetry(item, err)
			return Result{}, err
		}
	}
	if candidates != nil {
		if decision.Kind != domain.DecisionReply {
			cancelReason := "reply action did not complete"
			if strings.Contains(
				decision.Reason,
				"sender_facing_reply_blocked_by_current_route",
			) {
				cancelReason = "sender_facing_reply_blocked_by_current_route"
			}
			if err := candidates.CancelWorkReplyCandidate(
				item.ID,
				item.LeaseBy,
				cancelReason,
			); err != nil {
				return Result{}, err
			}
			if err := d.complete(item, decision); err != nil {
				return Result{}, err
			}
		} else if completer, ok := d.queue.(replyCandidateCompleter); ok {
			if err := completer.CompleteReplyCandidateClaim(item.ID, item.LeaseBy, decision); err != nil {
				return Result{}, err
			}
		} else {
			if err := candidates.ConsumeWorkReplyCandidate(item.ID, item.LeaseBy); err != nil {
				return Result{}, err
			}
			if err := d.complete(item, decision); err != nil {
				return Result{}, err
			}
		}
	} else if err := d.complete(item, decision); err != nil {
		return Result{}, err
	}
	return Result{Processed: true, Decision: decision}, nil
}

func isSenderFacingDecision(decision domain.Decision) bool {
	return decision.Kind == domain.DecisionReply ||
		decision.Kind == domain.DecisionRequestApproval
}

func routeAllowsSenderFacingDecision(decision domain.Decision) bool {
	if decision.Kind != domain.DecisionNotify &&
		decision.Kind != domain.DecisionReply {
		return false
	}
	switch decision.Relevance {
	case domain.RelevanceAssistantRequest,
		domain.RelevanceOwnerRequest,
		domain.RelevanceDirectMention,
		domain.RelevancePrivateMessage:
		return true
	default:
		return false
	}
}

func replyRequiresApproval(handler ReplyHandler, decision domain.Decision) bool {
	predictor, ok := handler.(replyApprovalPredictor)
	return ok && predictor.RequiresApproval(decision)
}

func (d *Daemon) notifyReplyApproval(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	action domain.Action,
) error {
	if d.notifier == nil {
		return nil
	}
	if notifier, ok := d.notifier.(approvalNotificationHandler); ok {
		return notifier.HandleApprovalNotification(ctx, item, decision, action)
	}
	return errs.NewInternalError(
		errs.SubtypeFailedPrecondition,
		"approval notification handler is not configured",
	)
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
	if err := d.ensureOwnerNotification(ctx, item, decision, notifications); err != nil {
		d.markRetry(item, err)
		return Result{}, err
	}
	if err := d.complete(item, decision); err != nil {
		return Result{}, err
	}
	return Result{Processed: true, Decision: decision}, nil
}

func (d *Daemon) ensureOwnerNotification(
	ctx context.Context,
	item domain.WorkItem,
	decision domain.Decision,
	notifications postReplyNotificationStore,
) error {
	actionID, key, completed, err := notifications.BeginPostReplyNotification(ctx, item.DedupKey, decision)
	if err != nil {
		return err
	}
	if !completed {
		if d.notifier == nil {
			err := errs.NewInternalError(errs.SubtypeUnknown, "post-reply owner notifier is not configured")
			_ = notifications.CompletePostReplyNotification(context.WithoutCancel(ctx), actionID, err.Error())
			return err
		}
		if err := d.ensureLease(ctx, item); err != nil {
			return err
		}
		if err := d.notifier.HandleNotification(ctx, item, decision, key); err != nil {
			_ = notifications.CompletePostReplyNotification(context.WithoutCancel(ctx), actionID, err.Error())
			return err
		}
		if err := notifications.CompletePostReplyNotification(ctx, actionID, ""); err != nil {
			return err
		}
	}
	return nil
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
		if d.markPermanentFailure(item, runErr) {
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
		d.notifyTerminalFailure(item, runErr)
		return
	}
	var retryAfter interface{ RetryAfter() time.Duration }
	if errors.As(runErr, &retryAfter) {
		if q, ok := d.queue.(delayedRetryMarker); ok {
			_ = q.MarkRetryAfter(item.ID, runErr.Error(), retryAfter.RetryAfter())
			d.notifyTerminalFailure(item, runErr)
			return
		}
	}
	if q, ok := d.queue.(retryMarker); ok {
		_ = q.MarkRetry(item.ID, runErr.Error())
		d.notifyTerminalFailure(item, runErr)
	}
}

func (d *Daemon) markPermanentFailure(item domain.WorkItem, runErr error) bool {
	if item.LeaseBy != "" {
		if q, ok := d.queue.(fencedDeadLetterMarker); ok {
			if err := q.MarkDeadLetterClaim(item.ID, item.LeaseBy, runErr.Error()); err != nil {
				return false
			}
			d.notifyTerminalFailure(item, runErr)
			return true
		}
	}
	if q, ok := d.queue.(deadLetterMarker); ok {
		if err := q.MarkDeadLetter(item.ID, runErr.Error()); err != nil {
			return false
		}
		d.notifyTerminalFailure(item, runErr)
		return true
	}
	return false
}

func (d *Daemon) notifyTerminalFailure(item domain.WorkItem, runErr error) {
	reader, ok := d.queue.(workItemReader)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	current, err := reader.GetWorkItem(ctx, item.ID)
	if err != nil || current.Status != domain.StatusDeadLetter {
		return
	}
	if d.investigationProgress != nil {
		_ = d.investigationProgress.Block(ctx, current, runErr)
	}
	if d.terminalFailure == nil {
		return
	}
	_ = d.terminalFailure.HandleTerminalFailure(ctx, current, runErr)
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

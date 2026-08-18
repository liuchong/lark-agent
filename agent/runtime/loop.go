package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// AgentLoop drives an explicit native tool-calling conversation.
type AgentLoop struct {
	Model             einomodel.BaseChatModel
	TerminalFinalizer einomodel.BaseChatModel
	Tools             *agenttools.Registry
	MaxTurns          int
	MaxToolBytes      int
	MaxTotalBytes     int
	MaxContextBytes   int
	ContextCompaction float64
	MaxElapsed        time.Duration
	MaxRepeatedCalls  int
	MaxToolCalls      int
	MaxNoProgress     int
	SimpleMaxTurns    int
	CodingMaxTurns    int
	GoalMaxTurns      int
	SystemPrompt      string
	ToolChoice        schema.ToolChoice
	Recorder          RunRecorder
	ModelFingerprint  string
	ConfigFingerprint string
}

type AgentPhase string

const (
	PhasePrepare        AgentPhase = "prepare"
	PhaseGenerate       AgentPhase = "generate"
	PhaseValidateTurn   AgentPhase = "validate_turn"
	PhaseExecuteTools   AgentPhase = "execute_tools"
	PhaseObserve        AgentPhase = "observe"
	PhaseVerifyProgress AgentPhase = "verify_progress"
	PhaseConverge       AgentPhase = "converge"
	PhaseFinalize       AgentPhase = "finalize"
	PhaseTerminal       AgentPhase = "terminal"
)

// RunRecorder persists one loop's lifecycle and trajectory.
type RunRecorder interface {
	StartAgentRun(context.Context, domain.NormalizedEvent, string, string) (domain.AgentRun, error)
	AppendAgentStep(context.Context, domain.AgentStep) error
	FinishAgentRun(context.Context, string, domain.AgentRunStatus, string) error
}

// LoopDecisionAgent adapts AgentLoop to app.Decider.
type LoopDecisionAgent struct {
	Loop AgentLoop
}

// Decide returns the loop's terminal decision.
func (a LoopDecisionAgent) Decide(ctx context.Context, bundle agentcontext.Bundle) (domain.Decision, error) {
	decision, _, err := a.Loop.Decide(ctx, bundle)
	return decision, err
}

// Decide runs until submit_decision produces a validated terminal decision.
func (l AgentLoop) Decide(ctx context.Context, bundle agentcontext.Bundle) (decision domain.Decision, trajectory []*schema.Message, err error) {
	if bundle.WorkKind != domain.WorkKindSmartCommand {
		if guardedDecision, guarded := guardedRequestDecision(bundle); guarded {
			return guardedDecision, nil, nil
		}
	}
	if l.Model == nil {
		return domain.Decision{}, nil, errs.NewInternalError(errs.SubtypeUnknown, "agent loop model is not configured")
	}
	if l.Tools == nil {
		return domain.Decision{}, nil, errs.NewInternalError(errs.SubtypeUnknown, "agent loop tools are not configured")
	}
	if l.MaxTurns <= 0 {
		l.MaxTurns = 150
	}
	if bundle.WorkKind == domain.WorkKindFastPath {
		return domain.Decision{}, nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "fast_path work must be answered before entering the model loop")
	}
	l.MaxTurns = l.maxTurnsForWorkKind(bundle.WorkKind)
	if bundle.MaxTurns > 0 && bundle.MaxTurns < l.MaxTurns {
		l.MaxTurns = bundle.MaxTurns
	}
	if l.MaxToolBytes <= 0 {
		l.MaxToolBytes = 32 * 1024
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 128 * 1024
	}
	if l.MaxContextBytes <= 0 {
		l.MaxContextBytes = 64 * 1024
	}
	if l.ContextCompaction < 0.5 || l.ContextCompaction > 0.95 {
		l.ContextCompaction = 0.80
	}
	if l.MaxElapsed <= 0 {
		l.MaxElapsed = 10 * time.Minute
	}
	if l.MaxRepeatedCalls <= 0 {
		l.MaxRepeatedCalls = 3
	}
	if l.MaxToolCalls <= 0 {
		l.MaxToolCalls = 16
	}
	if l.MaxNoProgress <= 0 {
		l.MaxNoProgress = 3
	}
	ctx, cancel := context.WithTimeout(ctx, l.MaxElapsed)
	defer cancel()
	if strings.TrimSpace(l.SystemPrompt) == "" {
		l.SystemPrompt = agentcontext.AgentSystemPrompt()
	}
	l.SystemPrompt = strings.TrimSpace(l.SystemPrompt) + "\n\n" + modelTurnBudgetPrompt(l.MaxTurns)
	if l.ToolChoice == "" {
		l.ToolChoice = schema.ToolChoiceAllowed
	}
	invocationScope := agenttools.InvocationScope{
		Owner:                 bundle.User.OpenID != "" && bundle.Event.SenderID == bundle.User.OpenID,
		ReadOnly:              bundle.User.OpenID == "" || bundle.Event.SenderID != bundle.User.OpenID,
		WorkspaceWriteAllowed: bundle.User.OpenID != "" && bundle.Event.SenderID == bundle.User.OpenID && domain.IsWorkspaceWriteRequested(bundle.Event.Content),
		ChatID:                bundle.Event.ChatID,
		WorkKind:              bundle.WorkKind,
		GitHubReference:       bundle.GitHubReference,
		ResourceURLs:          bundleResourceURLs(bundle),
	}
	requestedWorkspaceScope := requestedCodingWorkspaceScope(bundle)
	visibleToolInfos := l.Tools.InfosFor(invocationScope)
	if requestedWorkspaceScope != "" {
		visibleToolInfos = exactScopeToolInfos(visibleToolInfos)
	}
	bundle = filterBundleTools(bundle, visibleToolInfos, invocationScope)
	messages := []*schema.Message{
		schema.SystemMessage(l.SystemPrompt),
		schema.SystemMessage(agentcontext.AgentTaskProcessPrompt(bundle)),
		initialUserMessage(bundle),
	}
	if requestedWorkspaceScope != "" {
		messages = append(messages, schema.SystemMessage(
			exactCodingWorkspaceScopePrompt(requestedWorkspaceScope, l.MaxToolCalls),
		))
	}
	var runID string
	runFinished := false
	if l.Recorder != nil {
		run, startErr := l.Recorder.StartAgentRun(ctx, bundle.Event, l.ModelFingerprint, l.ConfigFingerprint)
		if startErr != nil {
			return domain.Decision{}, nil, startErr
		}
		runID = run.ID
		defer func() {
			if !runFinished && err != nil {
				_ = l.Recorder.FinishAgentRun(context.WithoutCancel(ctx), runID, domain.AgentRunFailed, err.Error())
			}
		}()
	}
	sequence := 0
	allowedSources := make(map[string]bool)
	observedSources := make(map[string]map[string]domain.SourceRef)
	authoritativeSources := make(map[string]bool)
	authoritativeContents := make(map[string]string)
	codingEvidenceReads := 0
	for _, source := range bundle.Sources {
		allowedSources[sourceKey(source)] = true
		recordObservedSource(observedSources, source)
	}
	totalToolBytes := 0
	resultFingerprints := map[string]int{}
	sourceLessWorkspaceSearches := 0
	toolBudgetConvergencePrompted := false
	codingEvidenceConvergencePrompted := false
	codingEvidenceAvailable := false
	var codingSearches codingSearchEvidence
	var resourceProgress resourceHandoffProgress
	investigationPlanSubmitted := false
	noProgressLarkContext := false
	toolCalls := 0
	noProgressStreak := 0
	lastObservation := ""
	forceDecision := false
	terminalOnlyAttempts := 0
	groundingCorrectionPending := false
	groundingCorrectionGranted := false
	structuralRecoverySearchAttempted := false
	structuralRecoveryReadAttempted := false
	var structuralRecoveryCandidates []string
	evidence := responseEvidence{}
	terminalRepair := terminalRepairContext{}
	runTerminalFinalizer := func(failure string) (domain.Decision, error) {
		if l.TerminalFinalizer == nil {
			return domain.Decision{}, errs.NewInternalError(
				errs.SubtypeModelNonConvergence,
				"%s; terminal finalizer is not configured",
				failure,
			)
		}
		finalizerMessages := terminalFinalizerMessages(bundle, trajectory, terminalRepair, failure)
		assistant, generateErr := l.TerminalFinalizer.Generate(ctx, finalizerMessages)
		if generateErr != nil {
			return domain.Decision{}, generateErr
		}
		if assistant == nil {
			return domain.Decision{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "terminal finalizer returned no message")
		}
		sequence++
		if err := l.recordModelStep(ctx, runID, sequence, assistant); err != nil {
			return domain.Decision{}, err
		}
		trajectory = append(trajectory, assistant)
		if len(assistant.ToolCalls) > 0 {
			return domain.Decision{}, errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"terminal finalizer must not call tools",
			)
		}
		finalized, err := ParseDecision(assistant.Content)
		if err != nil {
			return domain.Decision{}, err
		}
		normalized := normalizeDecisionLanguage(bundle, finalized)
		normalized = canonicalizeDecisionSources(normalized, allowedSources, observedSources)
		if err := validateTerminalDecision(bundle, normalized); err != nil {
			return domain.Decision{}, err
		}
		if err := validateDecisionSources(normalized, allowedSources); err != nil {
			return domain.Decision{}, err
		}
		if normalized.Kind == domain.DecisionReply ||
			normalized.Kind == domain.DecisionRequestApproval {
			normalized.Progress.CompletedChecks = append(
				[]string(nil),
				terminalRepair.CompletedChecks...,
			)
		}
		normalized = normalizeCodingDecisionWithSearchEvidence(
			bundle,
			normalized,
			codingSearches,
		)
		if err := validateResponseQuality(bundle, normalized, evidence); err != nil {
			return domain.Decision{}, err
		}
		if err := verifyCodingDecision(
			bundle,
			normalized,
			allowedSources,
			authoritativeSources,
			codingEvidenceReads,
		); err != nil {
			return domain.Decision{}, err
		}
		if err := verifyGroundedCodingReply(
			codingStructuralQuestion(bundle),
			normalized,
			authoritativeContents,
		); err != nil {
			return domain.Decision{}, errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"%v. Terminal finalizer cannot repair unsupported reply wording with more tools; emit only grounded facts, explicit unknowns, and a next step",
				err,
			)
		}
		return normalized, nil
	}
	for turn := 0; turn < l.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return domain.Decision{}, trajectory, err
		}
		if turn > 0 {
			var removed int
			messages, removed = expireEphemeralImages(messages)
			if removed > 0 {
				messages = append(messages, schema.SystemMessage(
					"Ephemeral image inputs were supplied only in the first model turn and have now been removed from repeated context. Use facts already extracted into the trajectory; if they are insufficient, state that the image evidence cannot be rechecked instead of guessing.",
				))
			}
		}
		compaction := compactMessages(
			messages,
			contextMessageBudget(l.MaxContextBytes),
			l.ContextCompaction,
		)
		if compaction.Overflow {
			return domain.Decision{}, trajectory, errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"model context cannot preserve the latest complete tool protocol unit within %d bytes",
				l.MaxContextBytes,
			)
		}
		messages = compaction.Messages
		requestMessages := append([]*schema.Message(nil), messages...)
		budget := runBudget{
			Phase:            PhaseGenerate,
			CurrentTurn:      turn + 1,
			MaxTurns:         l.MaxTurns,
			ToolCalls:        toolCalls,
			MaxToolCalls:     l.MaxToolCalls,
			ContextBytes:     messageBytes(messages),
			MaxContextBytes:  l.MaxContextBytes,
			Compacted:        compaction.Compacted,
			ReplacedMessages: compaction.ReplacedMessages,
			TargetLanguage:   resolvedBundleLanguage(bundle),
		}
		progressMessageIndex := len(requestMessages)
		requestMessages = append(
			requestMessages,
			schema.SystemMessage(modelRunProgressPrompt(budget, terminalRepair)),
		)
		structuralEvidenceCompletion := false
		structuralEvidenceSearchRecovery := false
		structuralEvidenceReadRecovery := false
		structuralQuestion := codingStructuralQuestion(bundle)
		missingStructuralEvidence := bundle.WorkKind == domain.WorkKindCodingQuestion &&
			codingEvidenceAvailable &&
			hasOpaqueSerializationDeclarationForQuestion(
				structuralQuestion,
				authoritativeContents,
			) &&
			missingStructuralSerializationEvidence(
				structuralQuestion,
				authoritativeContents,
			)
		if budget.RemainingTurns() == 0 {
			forceDecision = true
		}
		if missingStructuralEvidence &&
			!forceDecision &&
			budget.RemainingToolCalls() > 0 {
			switch {
			case len(structuralRecoveryCandidates) > 0 &&
				!structuralRecoveryReadAttempted:
				structuralEvidenceReadRecovery = true
			case !structuralRecoverySearchAttempted &&
				budget.RemainingTurns() >= 2 &&
				budget.RemainingToolCalls() >= 2 &&
				structuralEvidenceSearchQuery(structuralQuestion) != "" &&
				toolInfoAvailable(visibleToolInfos, "search_workspace"):
				structuralEvidenceSearchRecovery = true
			}
		}
		if bundle.WorkKind == domain.WorkKindCodingQuestion &&
			codingEvidenceAvailable &&
			budget.RemainingTurns() <= 1 &&
			!structuralEvidenceReadRecovery {
			if budget.RemainingTurns() == 1 &&
				!forceDecision &&
				budget.RemainingToolCalls() > 0 &&
				toolInfoAvailable(visibleToolInfos, "read_workspace") &&
				missingStructuralEvidence {
				structuralEvidenceCompletion = true
			} else {
				forceDecision = true
			}
		}
		if toolCalls >= l.MaxToolCalls {
			forceDecision = true
			structuralEvidenceCompletion = false
			structuralEvidenceSearchRecovery = false
			structuralEvidenceReadRecovery = false
		}
		terminalOnly := forceDecision
		turnToolInfos := visibleToolInfos
		if terminalOnly {
			terminalAttemptLimit := maxTerminalOnlyAttempts
			if groundingCorrectionGranted {
				terminalAttemptLimit++
			}
			if terminalOnlyAttempts >= terminalAttemptLimit {
				failure := fmt.Sprintf(
					"model did not submit a terminal decision after %d attempts",
					terminalAttemptLimit,
				)
				decision, finalizerErr := runTerminalFinalizer(failure)
				if finalizerErr != nil {
					return domain.Decision{}, trajectory, errs.NewInternalError(
						errs.SubtypeModelNonConvergence,
						"%s; terminal finalizer failed: %v",
						failure,
						finalizerErr,
					)
				}
				if l.Recorder != nil {
					if err := l.Recorder.FinishAgentRun(ctx, runID, domain.AgentRunCompleted, ""); err != nil {
						return domain.Decision{}, trajectory, err
					}
					runFinished = true
				}
				return decision, trajectory, nil
			}
			terminalOnlyAttempts++
			turnToolInfos = submitDecisionOnly(visibleToolInfos)
			requestMessages = append(requestMessages, schema.SystemMessage(
				terminalOnlyPrompt(terminalOnlyAttempts, terminalAttemptLimit, terminalRepair),
			))
		} else if structuralEvidenceSearchRecovery {
			turnToolInfos = namedToolOnly(visibleToolInfos, "search_workspace")
			requestMessages = append(requestMessages, schema.SystemMessage(
				structuralEvidenceSearchRecoveryPrompt(
					structuralEvidenceSearchQuery(structuralQuestion),
				),
			))
		} else if structuralEvidenceReadRecovery {
			turnToolInfos = namedToolOnly(visibleToolInfos, "read_workspace")
			requestMessages = append(requestMessages, schema.SystemMessage(
				structuralEvidenceReadRecoveryPrompt(structuralRecoveryCandidates),
			))
		} else if structuralEvidenceCompletion {
			turnToolInfos = namedToolOnly(visibleToolInfos, "read_workspace")
			requestMessages = append(requestMessages, schema.SystemMessage(
				structuralEvidenceCompletionPrompt(),
			))
		}
		for range 4 {
			budget.ContextBytes = messageBytes(requestMessages)
			progress := modelRunProgressPrompt(budget, terminalRepair)
			if requestMessages[progressMessageIndex].Content == progress {
				break
			}
			requestMessages[progressMessageIndex] = schema.SystemMessage(progress)
		}
		if requestBytes := messageBytes(requestMessages); requestBytes > l.MaxContextBytes {
			return domain.Decision{}, trajectory, errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"model request context is %d bytes and exceeds the configured %d-byte limit after compaction",
				requestBytes,
				l.MaxContextBytes,
			)
		}
		assistant, err := l.Model.Generate(ctx, requestMessages,
			einomodel.WithTools(turnToolInfos),
			einomodel.WithToolChoice(l.ToolChoice))
		if err != nil {
			return domain.Decision{}, trajectory, err
		}
		if assistant == nil {
			return domain.Decision{}, trajectory, errs.NewInternalError(errs.SubtypeInvalidResponse, "agent model returned no message")
		}
		if len(assistant.ToolCalls) == 0 {
			sequence++
			if err := l.recordModelStep(ctx, runID, sequence, assistant); err != nil {
				return domain.Decision{}, trajectory, err
			}
			messages = append(messages, assistant)
			trajectory = append(trajectory, assistant)
			messages = append(messages, schema.SystemMessage(
				"Plain assistant text is not accepted. Continue by calling an available tool, and finish exactly once with submit_decision.",
			))
			continue
		}
		sequence++
		if err := l.recordModelStep(ctx, runID, sequence, assistant); err != nil {
			return domain.Decision{}, trajectory, err
		}
		if hasMixedSubmitCalls(assistant.ToolCalls) {
			return domain.Decision{}, trajectory, errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"submit_decision cannot be combined with other tool calls",
			)
		}
		messages = append(messages, assistant)
		trajectory = append(trajectory, assistant)
		if modelOutputTruncated(assistant) {
			for _, call := range assistant.ToolCalls {
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
					return domain.Decision{}, trajectory, errs.NewInternalError(errs.SubtypeInvalidResponse, "model tool call is missing id or name")
				}
				content := `{"ok":false,"error":"model output was truncated; this incomplete tool call was not executed. Retry with a complete tool call."}`
				toolMessage := schema.ToolMessage(content, call.ID, schema.WithToolName(call.Function.Name))
				messages = append(messages, toolMessage)
				trajectory = append(trajectory, toolMessage)
				sequence++
				if err := l.recordToolStep(ctx, runID, sequence, call, content, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"model output was truncated; incomplete tool call was not executed",
				)); err != nil {
					return domain.Decision{}, trajectory, err
				}
			}
			messages = append(messages, schema.SystemMessage(
				"Model output was truncated. Incomplete tool calls were not executed. Retry with complete tool calls that fit the output budget, then finish with submit_decision.",
			))
			continue
		}
		turnMadeProgress := false
		turnHadNoProgress := false
		postToolPrompts := make([]string, 0, 2)
		structuralEvidenceReadAttempts := 0
		structuralEvidenceSearchAttempts := 0
		for _, call := range assistant.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
				return domain.Decision{}, trajectory, errs.NewInternalError(errs.SubtypeInvalidResponse, "model tool call is missing id or name")
			}
			callSignature := call.Function.Name + "\x00" + normalizeToolArguments(call.Function.Arguments)
			toolCtx := agenttools.WithWorkItemDedup(ctx, domain.DedupKey(bundle.Event))
			toolCtx = agenttools.WithInvocationScope(toolCtx, invocationScope)
			var execution agenttools.Execution
			var toolErr error
			var submittedProgress domain.DecisionProgress
			executionArguments := call.Function.Arguments
			if (structuralEvidenceCompletion || structuralEvidenceReadRecovery) &&
				call.Function.Name == "read_workspace" {
				structuralEvidenceReadAttempts++
			}
			if structuralEvidenceSearchRecovery &&
				call.Function.Name == "search_workspace" {
				structuralEvidenceSearchAttempts++
			}
			if structuralEvidenceReadAttempts > 1 {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"the penultimate structural-evidence turn permits exactly one read_workspace call; submit the verified result in the final turn",
				)
			} else if structuralEvidenceSearchAttempts > 1 {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"structural-evidence recovery permits exactly one field-name search_workspace call",
				)
			} else if (terminalOnly ||
				structuralEvidenceCompletion ||
				structuralEvidenceSearchRecovery ||
				structuralEvidenceReadRecovery) &&
				!toolInfoAvailable(turnToolInfos, call.Function.Name) {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"tool %s is not available in this model turn; use the remaining turn budget and the tools currently exposed by the runtime",
					call.Function.Name,
				)
			} else if forceDecision && call.Function.Name != "submit_decision" {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"investigation must converge because the remaining turn, no-progress, or tool budget requires a terminal decision; submit_decision now with verified facts and explicit unknowns",
				)
			} else if toolCalls >= l.MaxToolCalls && call.Function.Name != "submit_decision" {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"tool-call budget exhausted after %d calls; submit_decision now",
					l.MaxToolCalls,
				)
				forceDecision = true
			} else if isCodingBundle(bundle) &&
				!investigationPlanSubmitted &&
				call.Function.Name == "search_workspace" &&
				!structuralEvidenceSearchRecovery {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"coding investigation requires submit_investigation_plan before broad workspace search; name entry points, symbols, tools, and stop conditions first",
				)
			} else if bundle.WorkKind == domain.WorkKindSimpleQuestion && call.Function.Name == "shell" {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"simple_question work cannot call shell; answer directly, ask a clarification, or classify it as a coding question",
				)
			} else if call.Function.Name == "get_lark_context" && noProgressLarkContext {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"get_lark_context returned no new context for this target message; use another evidence tool or submit_decision with explicit unknowns",
				)
			} else if structuralEvidenceSearchRecovery {
				toolErr = validateStructuralEvidenceSearchArguments(
					structuralQuestion,
					call.Function.Arguments,
					requestedWorkspaceScope,
				)
				if toolErr == nil && isCodingBundle(bundle) && requestedWorkspaceScope != "" {
					executionArguments, toolErr = prepareCodingWorkspaceToolArguments(
						call.Function.Name,
						call.Function.Arguments,
						requestedWorkspaceScope,
						codingWorkspaceRoot(bundle),
					)
				}
				if toolErr == nil && isCodingBundle(bundle) && requestedWorkspaceScope != "" {
					toolErr = validateCodingWorkspaceToolRealPath(
						call.Function.Name,
						executionArguments,
						requestedWorkspaceScope,
						codingWorkspaceRoot(bundle),
					)
				}
				if toolErr == nil {
					execution, toolErr = l.Tools.Execute(
						toolCtx,
						call.Function.Name,
						json.RawMessage(executionArguments),
					)
					if toolErr == nil {
						toolCalls++
						structuralRecoverySearchAttempted = true
						structuralRecoveryCandidates = structuralEvidenceCandidatePaths(
							structuralQuestion,
							execution.Content,
						)
					}
				}
			} else if structuralEvidenceReadRecovery {
				toolErr = validateStructuralEvidenceReadArguments(
					call.Function.Arguments,
					requestedWorkspaceScope,
					structuralRecoveryCandidates,
				)
				if toolErr == nil && isCodingBundle(bundle) && requestedWorkspaceScope != "" {
					executionArguments, toolErr = prepareCodingWorkspaceToolArguments(
						call.Function.Name,
						call.Function.Arguments,
						requestedWorkspaceScope,
						codingWorkspaceRoot(bundle),
					)
				}
				if toolErr == nil && isCodingBundle(bundle) && requestedWorkspaceScope != "" {
					toolErr = validateCodingWorkspaceToolRealPath(
						call.Function.Name,
						executionArguments,
						requestedWorkspaceScope,
						codingWorkspaceRoot(bundle),
					)
				}
				if toolErr == nil {
					execution, toolErr = l.Tools.Execute(
						toolCtx,
						call.Function.Name,
						json.RawMessage(executionArguments),
					)
					if toolErr == nil {
						toolCalls++
						structuralRecoveryReadAttempted = true
					}
				}
			} else if call.Function.Name == "search_workspace" && sourceLessWorkspaceSearches >= 3 {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"search_workspace is exhausted for this work item after repeated searches without citable sources; use list_workspace, read_workspace, a path-specific shell command, or submit_decision with explicit unknowns",
				)
			} else if isCodingBundle(bundle) && requestedWorkspaceScope != "" {
				executionArguments, toolErr = prepareCodingWorkspaceToolArguments(
					call.Function.Name,
					call.Function.Arguments,
					requestedWorkspaceScope,
					codingWorkspaceRoot(bundle),
				)
				if toolErr == nil {
					toolErr = validateCodingWorkspaceToolRealPath(
						call.Function.Name,
						executionArguments,
						requestedWorkspaceScope,
						codingWorkspaceRoot(bundle),
					)
				}
				if toolErr == nil {
					execution, toolErr = l.Tools.Execute(toolCtx, call.Function.Name, json.RawMessage(executionArguments))
					if toolErr == nil && consumesInvestigationToolBudget(call.Function.Name) {
						toolCalls++
					}
				}
			} else {
				toolErr = resourceProgress.ValidateMutation(bundle, call.Function.Name)
				if toolErr == nil {
					execution, toolErr = l.Tools.Execute(toolCtx, call.Function.Name, json.RawMessage(call.Function.Arguments))
				}
				if toolErr == nil && consumesInvestigationToolBudget(call.Function.Name) {
					toolCalls++
				}
			}
			if execution.Decision != nil {
				submittedProgress = execution.Decision.Progress
			}
			if toolErr == nil && execution.Decision != nil {
				normalized := normalizeDecisionLanguage(bundle, *execution.Decision)
				normalized = canonicalizeDecisionSources(normalized, allowedSources, observedSources)
				execution.Decision = &normalized
			}
			if toolErr == nil && execution.Decision != nil {
				if err := validateTerminalDecision(bundle, *execution.Decision); err != nil {
					toolErr = err
					execution.Decision = nil
				}
			}
			if toolErr == nil && execution.Decision != nil {
				if err := validateDecisionSources(*execution.Decision, allowedSources); err != nil {
					toolErr = err
					execution.Decision = nil
				}
			}
			if toolErr == nil &&
				execution.Decision != nil &&
				groundingCorrectionPending &&
				execution.Decision.EvidenceStatus == domain.EvidenceInsufficient &&
				claimsRuntimeEvidenceWasLost(*execution.Decision) {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"a prior verified coding draft failed only reply-local grounding; exact current-run reads remain retained by the runtime, so context compaction did not erase that evidence. You must not downgrade to insufficient by claiming those reads were lost. Remove or rephrase unsupported reply wording and resubmit a narrower verified answer with the same valid sources; a genuinely new evidence gap may still be stated precisely",
				)
				execution.Decision = nil
			}
			if toolErr == nil && execution.Decision != nil {
				if execution.Decision.Kind == domain.DecisionReply ||
					execution.Decision.Kind == domain.DecisionRequestApproval {
					execution.Decision.Progress.CompletedChecks = append(
						[]string(nil),
						terminalRepair.CompletedChecks...,
					)
				}
				normalized := normalizeCodingDecisionWithSearchEvidence(
					bundle,
					*execution.Decision,
					codingSearches,
				)
				execution.Decision = &normalized
			}
			if toolErr == nil && execution.Decision != nil {
				if err := validateResponseQuality(bundle, *execution.Decision, evidence); err != nil {
					toolErr = err
					execution.Decision = nil
				}
			}
			if toolErr == nil && execution.Decision != nil {
				if err := verifyCodingDecision(
					bundle,
					*execution.Decision,
					allowedSources,
					authoritativeSources,
					codingEvidenceReads,
				); err != nil {
					toolErr = err
					execution.Decision = nil
				}
			}
			if toolErr == nil && execution.Decision != nil {
				if err := verifyGroundedCodingReply(
					structuralQuestion,
					*execution.Decision,
					authoritativeContents,
				); err != nil {
					toolErr = errs.NewInternalError(
						errs.SubtypeInvalidResponse,
						"%v. This is a reply-local grounding correction, not evidence loss: exact current-run reads remain retained outside the compacted model transcript. Remove or rephrase only the unsupported path, identifier, callback, field, or serialized example and resubmit a narrower verified answer; do not downgrade supported facts to insufficient",
						err,
					)
					execution.Decision = nil
					groundingCorrectionPending = true
					forceDecision = true
					if !groundingCorrectionGranted {
						l.MaxTurns++
						groundingCorrectionGranted = true
						postToolPrompts = append(
							postToolPrompts,
							fmt.Sprintf(
								"One dedicated submit-only grounding-correction turn has been added. The total model-turn limit is now %d. Runtime-held authoritative reads remain available to validation even if older model messages were compacted. Correct only the rejected reply wording and preserve supported facts.",
								l.MaxTurns,
							),
						)
					}
				}
			}
			if toolErr == nil && execution.Decision == nil && call.Function.Name == "search_workspace" && len(execution.Sources) == 0 {
				sourceLessWorkspaceSearches++
			}
			if toolErr == nil && call.Function.Name == "submit_investigation_plan" {
				investigationPlanSubmitted = true
			}
			if toolErr == nil && call.Function.Name == "get_lark_context" && toolContentNoNewContext(execution.Content) {
				noProgressLarkContext = true
			}
			if toolErr == nil && execution.Decision == nil {
				resourceProgress.Observe(
					bundle,
					call.Function.Name,
					executionArguments,
					execution.Content,
					execution.Sources,
				)
			}
			if toolErr == nil && execution.Decision == nil && isRelevantEvidenceTool(call.Function.Name) {
				content := strings.TrimSpace(execution.Content)
				nonEmpty := content != "" &&
					(call.Function.Name != "get_lark_context" ||
						!toolContentNoNewContext(execution.Content))
				evidence.RecordRelevantRead(
					evidenceDigest(call.Function.Name, content),
					nonEmpty,
				)
				if nonEmpty {
					terminalRepair.CompletedChecks = appendUniqueString(
						terminalRepair.CompletedChecks,
						call.Function.Name,
					)
				}
			}
			if toolErr == nil &&
				execution.Decision == nil &&
				isCodingBundle(bundle) &&
				isCodingEvidenceTool(call.Function.Name) {
				codingEvidenceReads++
			}
			if toolErr == nil && execution.Decision == nil && isCodingBundle(bundle) {
				codingSearches.Record(
					call.Function.Name,
					executionArguments,
					execution.Content,
				)
			}
			content := toolResultContent(execution, toolErr)
			if toolErr != nil {
				terminalRepair.LastFailure = toolErr.Error()
				if len(submittedProgress.Unknowns) > 0 {
					terminalRepair.Unknowns = append(
						[]string(nil),
						submittedProgress.Unknowns...,
					)
				}
				if resourceEvidenceFailureRequiresConvergence(
					bundle,
					call.Function.Name,
					toolErr,
					resourceProgress.resourceEvidence,
				) {
					forceDecision = true
					terminalRepair.Unknowns = appendUniqueString(
						terminalRepair.Unknowns,
						toolErr.Error(),
					)
					postToolPrompts = append(
						postToolPrompts,
						resourceEvidenceFailureConvergencePrompt(toolErr),
					)
				}
			}
			fingerprint := toolResultFingerprint(
				call.Function.Name,
				executionArguments,
				toolFingerprintSummary(execution, toolErr),
				toolErr,
			)
			resultFingerprints[fingerprint]++
			repeatCount := resultFingerprints[fingerprint]
			if repeatCount == 2 {
				postToolPrompts = append(
					postToolPrompts,
					"Recovery disposition retry_with_changed_input: this exact tool, normalized input, and result repeated without new evidence. Change the arguments or evidence source; do not mechanically repeat it.",
				)
			}
			repeatLimit := l.MaxRepeatedCalls
			if l.MaxNoProgress > 0 &&
				(repeatLimit <= 0 || l.MaxNoProgress < repeatLimit) {
				repeatLimit = l.MaxNoProgress
			}
			if repeatCount >= repeatLimit {
				forceDecision = true
				terminalRepair.LastFailure = fmt.Sprintf(
					"converge_partial: repeated tool-result fingerprint for %s occurred %d times without changed conditions",
					call.Function.Name,
					repeatCount,
				)
				postToolPrompts = append(
					postToolPrompts,
					"Recovery disposition converge_partial: repeated conditions did not change. Broad investigation is closed; preserve supported facts, state exact unknowns, and submit a typed terminal outcome.",
				)
			}
			observation := callSignature + "\x00" + content
			if toolErr == nil && observation != lastObservation {
				turnMadeProgress = true
			} else {
				turnHadNoProgress = true
			}
			lastObservation = observation
			rawToolBytes := len(content)
			content = clipToolContent(content, l.MaxToolBytes)
			totalToolBytes += len(content)
			toolMessage := schema.ToolMessage(content, call.ID, schema.WithToolName(call.Function.Name))
			messages = append(messages, toolMessage)
			trajectory = append(trajectory, toolMessage)
			sequence++
			if err := l.recordToolStep(ctx, runID, sequence, call, content, toolErr); err != nil {
				return domain.Decision{}, trajectory, err
			}
			if toolErr != nil {
				continue
			}
			if call.Function.Name == "edit_workspace" || call.Function.Name == "write_workspace" {
				invalidateWorkspaceFileSources(
					allowedSources,
					observedSources,
					authoritativeSources,
					authoritativeContents,
					toolCallPath(executionArguments),
				)
			}
			if !toolBudgetConvergencePrompted && shouldPromptToolBudgetConvergence(rawToolBytes, l.MaxToolBytes, totalToolBytes, l.MaxTotalBytes) {
				postToolPrompts = append(postToolPrompts, toolBudgetConvergencePrompt())
				toolBudgetConvergencePrompted = true
			}
			for _, source := range execution.Sources {
				allowedSources[sourceKey(source)] = true
				recordObservedSource(observedSources, source)
				if call.Function.Name == "read_workspace" {
					authoritativeContents[sourceKey(source)] = execution.Content
					if hasProductionSource([]domain.SourceRef{source}) {
						authoritativeSources[sourceKey(source)] = true
					}
				}
			}
			if bundle.WorkKind == domain.WorkKindCodingQuestion &&
				call.Function.Name == "read_workspace" &&
				hasProductionSource(execution.Sources) {
				codingEvidenceAvailable = true
				if budget.RemainingTurns() <= 1 {
					forceDecision = true
				}
				if !codingEvidenceConvergencePrompted {
					postToolPrompts = append(postToolPrompts, codingEvidenceConvergencePrompt())
					codingEvidenceConvergencePrompted = true
				}
			}
			if execution.Decision != nil {
				if l.Recorder != nil {
					if err := l.Recorder.FinishAgentRun(ctx, runID, domain.AgentRunCompleted, ""); err != nil {
						return domain.Decision{}, trajectory, err
					}
					runFinished = true
				}
				return *execution.Decision, trajectory, nil
			}
		}
		for _, prompt := range postToolPrompts {
			messages = append(messages, schema.SystemMessage(prompt))
		}
		if turnMadeProgress {
			noProgressStreak = 0
		} else if turnHadNoProgress {
			noProgressStreak++
		}
		if noProgressStreak >= l.MaxNoProgress {
			forceDecision = true
		}
	}
	return domain.Decision{}, trajectory, errs.NewInternalError(errs.SubtypeInvalidResponse, "agent loop exceeded maximum turns")
}

func resourceEvidenceFailureRequiresConvergence(
	bundle agentcontext.Bundle,
	toolName string,
	err error,
	resourceEvidenceAvailable bool,
) bool {
	if bundle.WorkKind != domain.WorkKindResourceHandoff ||
		toolName != "get_resource_evidence" ||
		err == nil ||
		resourceEvidenceAvailable {
		return false
	}
	problem, ok := errs.ProblemOf(err)
	if !ok || problem.Retryable {
		return false
	}
	switch problem.Category {
	case errs.CategoryAuthorization, errs.CategoryConfig:
		return true
	case errs.CategoryValidation:
		return problem.Subtype == errs.SubtypeFailedPrecondition
	default:
		return false
	}
}

func resourceEvidenceFailureConvergencePrompt(err error) string {
	return fmt.Sprintf(
		"Authoritative resource evidence is unavailable: %v. Broad investigation is closed. "+
			"Only submit_decision is allowed now. For a human handoff, reply with "+
			"evidence_status=insufficient and reply_outcome=clarification, state this exact "+
			"authorization, configuration, or record-link gap, and give the concrete recovery "+
			"step. Do not inspect adjacent chat subjects, search workspace code, propose a "+
			"status mutation, or claim that the issue was verified.",
		err,
	)
}

func filterBundleTools(
	bundle agentcontext.Bundle,
	infos []*schema.ToolInfo,
	scope agenttools.InvocationScope,
) agentcontext.Bundle {
	allowed := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info != nil {
			allowed[info.Name] = true
		}
	}
	filtered := bundle
	filtered.Environment.Tools = make([]agentcontext.ToolSpec, 0, len(bundle.Environment.Tools))
	for _, tool := range bundle.Environment.Tools {
		if allowed[tool.Name] {
			filtered.Environment.Tools = append(filtered.Environment.Tools, tool)
		}
	}
	if scope.ReadOnly {
		filtered.Environment.Commands = nil
	}
	return filtered
}

func (l AgentLoop) maxTurnsForWorkKind(kind domain.WorkKind) int {
	switch kind {
	case domain.WorkKindSimpleQuestion:
		limit := l.SimpleMaxTurns
		if limit <= 0 {
			limit = 2
		}
		if l.MaxTurns <= 0 || l.MaxTurns > limit {
			return limit
		}
		return l.MaxTurns
	case domain.WorkKindResourceHandoff:
		if l.MaxTurns > 20 {
			return 20
		}
	case domain.WorkKindCodingQuestion:
		if l.CodingMaxTurns > 0 && l.MaxTurns > l.CodingMaxTurns {
			return l.CodingMaxTurns
		}
	case domain.WorkKindCodingGoal:
		if l.GoalMaxTurns > 0 && l.MaxTurns > l.GoalMaxTurns {
			return l.GoalMaxTurns
		}
	case domain.WorkKindSmartCommand:
		return 20
	}
	return l.MaxTurns
}

func bundleResourceURLs(bundle agentcontext.Bundle) []string {
	seen := make(map[string]struct{})
	var urls []string
	add := func(values []string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			urls = append(urls, value)
		}
	}
	add(bundle.Event.ResourceURLs)
	for _, message := range bundle.Conversation {
		add(message.ResourceURLs)
	}
	return urls
}

func modelTurnBudgetPrompt(maxTurns int) string {
	return fmt.Sprintf(
		"The hard model-turn limit for this run is %d. This is an upper bound, not a target. Plan the shortest reliable investigation, stop as soon as evidence is sufficient, and call submit_decision before the limit is exhausted.",
		maxTurns,
	)
}

func modelTurnProgressPrompt(currentTurn, maxTurns int) string {
	remaining := maxTurns - currentTurn
	if remaining < 0 {
		remaining = 0
	}
	if remaining == 0 {
		return fmt.Sprintf(
			"Current model turn: %d of %d. Remaining model turns after this request: 0. You are at the final model turn: do not call broad tools, and finish now with submit_decision using verified facts plus explicit unknowns.",
			currentTurn,
			maxTurns,
		)
	}
	return fmt.Sprintf(
		"Current model turn: %d of %d. Remaining model turns after this request: %d. Use the remaining budget deliberately; prefer fewer turns and converge on submit_decision as soon as the available evidence supports a complete, partial, or clarification outcome.",
		currentTurn,
		maxTurns,
		remaining,
	)
}

type resourceHandoffProgress struct {
	resourceEvidence bool
	baseSchema       bool
	projectRules     bool
	productionRead   bool
	testRead         bool
	gitHistory       bool
}

func (p resourceHandoffProgress) ValidateMutation(
	bundle agentcontext.Bundle,
	toolName string,
) error {
	if bundle.WorkKind != domain.WorkKindResourceHandoff {
		return nil
	}
	if !p.projectRules && isResourceProjectEvidenceTool(toolName) {
		return errs.NewInternalError(
			errs.SubtypeFailedPrecondition,
			"resource handoff must call read_workspace_rules with the selected project path before %s",
			toolName,
		)
	}
	switch toolName {
	case "update_base_status":
		var missing []string
		checks := []struct {
			ok   bool
			name string
		}{
			{p.resourceEvidence, "get_resource_evidence"},
			{p.baseSchema, "inspect_base_schema"},
			{p.projectRules, "read_workspace_rules for the selected project"},
			{p.productionRead, "read_workspace on authoritative implementation"},
			{p.testRead, "read_workspace on regression/integration tests"},
			{p.gitHistory, "inspect_git_history for the fix"},
		}
		for _, check := range checks {
			if !check.ok {
				missing = append(missing, check.name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			return errs.NewInternalError(
				errs.SubtypeFailedPrecondition,
				"resource status mutation is blocked until these verified reads complete: %s",
				strings.Join(missing, ", "),
			)
		}
	case "reply_resource_comment":
		if !p.resourceEvidence || !p.projectRules || !p.productionRead ||
			!p.testRead || !p.gitHistory {
			return errs.NewInternalError(
				errs.SubtypeFailedPrecondition,
				"resource comment reply is blocked until linked evidence, project rules, implementation, regression tests, and Git evidence are verified",
			)
		}
	}
	return nil
}

func isResourceProjectEvidenceTool(name string) bool {
	switch name {
	case "explore_workspace",
		"search_workspace",
		"read_workspace",
		"search_code_symbols",
		"trace_code_path",
		"inspect_git_history",
		"shell":
		return true
	default:
		return false
	}
}

func (p *resourceHandoffProgress) Observe(
	bundle agentcontext.Bundle,
	toolName, arguments, content string,
	sources []domain.SourceRef,
) {
	if p == nil || bundle.WorkKind != domain.WorkKindResourceHandoff ||
		strings.TrimSpace(content) == "" {
		return
	}
	switch toolName {
	case "get_resource_evidence":
		p.resourceEvidence = len(sources) > 0
	case "inspect_base_schema":
		p.baseSchema = true
	case "read_workspace_rules":
		p.projectRules = len(sources) > 0
	case "inspect_git_history":
		p.gitHistory = len(sources) > 0
	case "read_workspace":
		var args struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(arguments), &args) != nil || len(sources) == 0 {
			return
		}
		path := strings.ToLower(filepath.ToSlash(args.Path))
		if strings.Contains(path, "integration_test") ||
			strings.Contains(path, "/test") ||
			strings.Contains(path, "_test.") ||
			strings.Contains(path, "/spec") ||
			strings.Contains(path, "fixture") {
			p.testRead = true
		} else if !strings.HasSuffix(path, ".md") {
			p.productionRead = true
		}
	}
}

type runBudget struct {
	Phase            AgentPhase
	CurrentTurn      int
	MaxTurns         int
	ToolCalls        int
	MaxToolCalls     int
	ContextBytes     int
	MaxContextBytes  int
	Compacted        bool
	ReplacedMessages int
	TargetLanguage   agentlocale.Language
}

func (b runBudget) RemainingTurns() int {
	remaining := b.MaxTurns - b.CurrentTurn
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (b runBudget) RemainingToolCalls() int {
	remaining := b.MaxToolCalls - b.ToolCalls
	if remaining < 0 {
		return 0
	}
	return remaining
}

func modelRunProgressPrompt(budget runBudget, repair ...terminalRepairContext) string {
	remainingBytes := budget.MaxContextBytes - budget.ContextBytes
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	percent := 0
	if budget.MaxContextBytes > 0 {
		percent = budget.ContextBytes * 100 / budget.MaxContextBytes
	}
	urgency := "normal"
	if percent >= 80 ||
		budget.RemainingTurns()*5 <= budget.MaxTurns ||
		(budget.MaxToolCalls > 0 && budget.ToolCalls*5 >= budget.MaxToolCalls*4) {
		urgency = "urgent"
	}
	dynamicState := "Dynamic run state: no completed checks or failed terminal gate recorded yet."
	if len(repair) > 0 {
		current := repair[0]
		completed := strings.Join(nonEmptyTrimmedStrings(current.CompletedChecks), ", ")
		if completed == "" {
			completed = "none"
		}
		unknowns := strings.Join(nonEmptyTrimmedStrings(current.Unknowns), ", ")
		if unknowns == "" {
			unknowns = "none"
		}
		lastFailure := strings.TrimSpace(current.LastFailure)
		if lastFailure == "" {
			lastFailure = "none"
		}
		dynamicState = fmt.Sprintf(
			"Dynamic run state: completed_checks=%s; unknowns=%s; last_failed_gate=%s; allowed_terminal_outcomes=complete,partial,clarification.",
			completed,
			unknowns,
			lastFailure,
		)
	}
	return fmt.Sprintf(
		"Agent phase: %s. %s Tool-call budget: %d of %d investigation calls used, %d remaining. "+
			"Context budget: %d of %d bytes used (%d%%), %d bytes remaining. "+
			"Automatic compaction: %t; replaced old messages: %d. Urgency: %s. "+
			"Required outward language: %s; use that language for all explanatory prose. "+
			"Do not spend turns just because the ceiling is high; when a narrow answer is supported, submit it. "+
			"When urgency is urgent, stop broad investigation, preserve explicit unknowns, "+
			"and converge on submit_decision. %s",
		phaseLabel(budget.Phase),
		modelTurnProgressPrompt(budget.CurrentTurn, budget.MaxTurns),
		budget.ToolCalls,
		budget.MaxToolCalls,
		budget.RemainingToolCalls(),
		budget.ContextBytes,
		budget.MaxContextBytes,
		percent,
		remainingBytes,
		budget.Compacted,
		budget.ReplacedMessages,
		urgency,
		budget.TargetLanguage,
		dynamicState,
	)
}

func phaseLabel(phase AgentPhase) AgentPhase {
	if phase == "" {
		return PhaseGenerate
	}
	return phase
}

func terminalFinalizerMessages(
	bundle agentcontext.Bundle,
	trajectory []*schema.Message,
	repair terminalRepairContext,
	failure string,
) []*schema.Message {
	type retainedCall struct {
		Name      string
		Arguments string
	}
	retainedCalls := make(map[string]retainedCall)
	for _, message := range trajectory {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, call := range message.ToolCalls {
			retainedCalls[call.ID] = retainedCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
		}
	}
	var evidenceLines []string
	for _, message := range trajectory {
		if message == nil || message.Role != schema.Tool {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if len(content) > 4000 {
			content = content[:4000] + "\n... [truncated]"
		}
		call := retainedCalls[message.ToolCallID]
		toolName := strings.TrimSpace(message.ToolName)
		if toolName == "" {
			toolName = call.Name
		}
		arguments := strings.TrimSpace(call.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		sourceText := retainedToolSourceText(message.Content)
		evidenceLines = append(evidenceLines, fmt.Sprintf(
			"- tool_result id=%s name=%s arguments=%s source_refs=%s content=%s",
			message.ToolCallID,
			toolName,
			arguments,
			sourceText,
			content,
		))
	}
	if len(evidenceLines) == 0 {
		evidenceLines = []string{"- none"}
	}
	completed := strings.Join(nonEmptyTrimmedStrings(repair.CompletedChecks), "; ")
	if completed == "" {
		completed = "none"
	}
	unknowns := strings.Join(nonEmptyTrimmedStrings(repair.Unknowns), "; ")
	if unknowns == "" {
		unknowns = "derive only from retained evidence"
	}
	lastFailure := strings.TrimSpace(repair.LastFailure)
	if lastFailure == "" {
		lastFailure = "none recorded"
	}
	return []*schema.Message{
		schema.SystemMessage("You are the lark-agent terminal finalizer. You have no tools. Do not request, call, or imply any tool use. Produce exactly one JSON object with the same fields accepted by submit_decision. Use only retained runtime evidence below. If evidence is insufficient, return reply_outcome=partial or clarification with evidence_status=insufficient, completed_checks, unknowns, and next_step. Never invent source_refs, code paths, deployment state, or Lark context.\n" + bundle.TaskRules.ReviewProjection()),
		schema.UserMessage(fmt.Sprintf(
			"Original message: %s\nWork kind: %s\nTask class: %s\nTerminal failure: %s\nLast runtime gate failure: %s\nReusable completed checks: %s\nKnown unknowns: %s\nRetained successful tool receipts:\n%s",
			bundle.Event.Content,
			bundle.WorkKind,
			bundle.TaskClass,
			failure,
			lastFailure,
			completed,
			unknowns,
			strings.Join(evidenceLines, "\n"),
		)),
	}
}

func retainedToolSourceText(content string) string {
	var payload struct {
		Sources []domain.SourceRef `json:"sources"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil || len(payload.Sources) == 0 {
		return "none"
	}
	data, err := json.Marshal(payload.Sources)
	if err != nil {
		return "none"
	}
	return string(data)
}

func consumesInvestigationToolBudget(toolName string) bool {
	return toolName != "submit_decision" && toolName != "submit_investigation_plan"
}

func resolvedBundleLanguage(bundle agentcontext.Bundle) agentlocale.Language {
	language := agentlocale.Language(bundle.User.Language)
	if language == agentlocale.LanguageChinese || language == agentlocale.LanguageEnglish {
		return language
	}
	return agentlocale.Resolve(
		agentlocale.Language(bundle.User.PreferredLanguage),
		agentlocale.Language(bundle.User.FallbackLanguage),
		bundle.Event.Content,
		conversationText(bundle.Conversation),
	)
}

func shouldPromptToolBudgetConvergence(rawToolBytes, maxToolBytes, totalToolBytes, maxTotalBytes int) bool {
	if maxToolBytes > 0 && rawToolBytes > maxToolBytes {
		return true
	}
	if maxTotalBytes <= 0 {
		return false
	}
	return totalToolBytes >= maxTotalBytes*8/10
}

func toolBudgetConvergencePrompt() string {
	return "Tool output budget is near or above the configured limit. Old raw tool output has been clipped for context safety. Summarize the evidence already gathered, avoid broad repeat searches, and converge with submit_decision unless one narrow read is required to avoid a false claim."
}

const maxTerminalOnlyAttempts = 3

func claimsRuntimeEvidenceWasLost(decision domain.Decision) bool {
	text := strings.ToLower(decision.Reason + "\n" + decision.ReplyText)
	for _, marker := range []string{
		"compaction",
		"compacted",
		"compressed context",
		"evidence was lost",
		"evidence lost",
		"source content was lost",
		"cannot recheck",
		"上下文压缩",
		"压缩后",
		"证据丢失",
		"来源丢失",
		"无法再次核验",
		"无法重新核验",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type terminalRepairContext struct {
	LastFailure     string
	CompletedChecks []string
	Unknowns        []string
}

func terminalOnlyPrompt(attempt, maxAttempts int, repair ...terminalRepairContext) string {
	contextText := "Last rejection: none recorded. Reusable completed checks: none recorded. Explicit unknowns: derive only from current evidence."
	if len(repair) > 0 {
		current := repair[0]
		lastFailure := strings.TrimSpace(current.LastFailure)
		if lastFailure == "" {
			lastFailure = "none recorded"
		}
		completed := strings.Join(nonEmptyTrimmedStrings(current.CompletedChecks), "; ")
		if completed == "" {
			completed = "none recorded"
		}
		unknowns := strings.Join(nonEmptyTrimmedStrings(current.Unknowns), "; ")
		if unknowns == "" {
			unknowns = "derive only from current evidence"
		}
		contextText = fmt.Sprintf(
			"Last rejection: %s. Reusable completed checks: %s. Explicit unknowns: %s.",
			lastFailure,
			completed,
			unknowns,
		)
	}
	return fmt.Sprintf(
		"Only submit_decision is available now. Previous investigation tools are no longer available. "+
			"%s "+
			"Call submit_decision in this turn using verified facts and explicit unknowns. "+
			"Choose reply_outcome=complete when every requested fact is supported, partial when safe findings remain useful but named facts are unknown, or clarification when exact missing input prevents investigation. "+
			"Do not repeat the rejected action or claim. "+
			"This is terminal-only attempt %d of %d.",
		contextText,
		attempt,
		maxAttempts,
	)
}

func codingEvidenceConvergencePrompt() string {
	return "Citable workspace evidence is now available. If it answers every concrete field the user asked for, call submit_decision now. A declaration that only shows an opaque String, bytes, raw JSON, or generic container does not prove its concrete serialized shape: use a remaining bounded read for current docs, tests, protocol definitions, or serialization code before claiming that shape is verified. Do not expand into unrelated Lark history, repository-wide searches, or production call-site proof unless the user explicitly asked about reachability. For an exact function's direct behavior, its digest-backed definition is sufficient; preserve explicit unknowns instead of over-investigating."
}

func structuralEvidenceCompletionPrompt() string {
	return "The question asks for a concrete serialized shape, but current-run reads still prove only an opaque container. Use exactly one targeted read_workspace call in this penultimate turn. Read one already-known current documentation, fixture, protocol, or serializer path that directly exposes the structure. Candidate search, listing, shell, Lark history, and submit_decision are unavailable now. The final turn is reserved exclusively for submit_decision."
}

func submitDecisionOnly(infos []*schema.ToolInfo) []*schema.ToolInfo {
	if selected := namedToolOnly(infos, "submit_decision"); len(selected) > 0 {
		return selected
	}
	return infos
}

func namedToolOnly(infos []*schema.ToolInfo, name string) []*schema.ToolInfo {
	for _, info := range infos {
		if info != nil && info.Name == name {
			return []*schema.ToolInfo{info}
		}
	}
	return nil
}

func toolInfoAvailable(infos []*schema.ToolInfo, name string) bool {
	for _, info := range infos {
		if info != nil && info.Name == name {
			return true
		}
	}
	return false
}

type compactionResult struct {
	Messages         []*schema.Message
	BeforeBytes      int
	AfterBytes       int
	Compacted        bool
	ReplacedMessages int
	Overflow         bool
}

func contextMessageBudget(maxBytes int) int {
	if maxBytes <= 0 {
		return maxBytes
	}
	reserve := min(2*1024, maxBytes/4)
	return max(1, maxBytes-reserve)
}

func compactMessages(messages []*schema.Message, maxBytes int, ratio float64) compactionResult {
	before := messageBytes(messages)
	result := compactionResult{
		Messages:    messages,
		BeforeBytes: before,
		AfterBytes:  before,
	}
	if maxBytes <= 0 {
		return result
	}
	if ratio < 0.5 || ratio > 0.95 {
		ratio = 0.80
	}
	target := int(float64(maxBytes) * ratio)
	if before <= target {
		return result
	}
	protectedFrom := len(messages) - 2
	if protectedFrom < 2 {
		protectedFrom = 2
	}
	protectedFrom = latestCompleteToolUnitStart(messages, protectedFrom)
	compacted := make([]*schema.Message, 0, len(messages))
	if len(messages) > 0 {
		compacted = append(compacted, cloneMessageWithContentLimit(messages[0], maxBytes/8))
	}
	if len(messages) > 1 {
		compacted = append(compacted, cloneMessageWithContentLimit(messages[1], maxBytes/3))
	}
	checkpointOverflow := false
	if protectedFrom > 2 {
		checkpoint, complete := buildContextCheckpoint(
			messages[2:protectedFrom],
			maxBytes/4,
		)
		checkpointOverflow = !complete
		if checkpoint != "" {
			compacted = append(compacted, schema.SystemMessage(checkpoint))
		}
	}
	for i := protectedFrom; i < len(messages); i++ {
		compacted = append(compacted, cloneMessageWithContentLimit(messages[i], maxBytes/6))
	}
	for messageBytes(compacted) > maxBytes {
		changed := false
		for i := 1; i < len(compacted) && messageBytes(compacted) > maxBytes; i++ {
			var messageChanged bool
			compacted[i], messageChanged = shrinkMessageForContext(compacted[i])
			changed = changed || messageChanged
		}
		if !changed {
			break
		}
	}
	result.Messages = compacted
	result.AfterBytes = messageBytes(compacted)
	result.Compacted = true
	result.ReplacedMessages = max(0, protectedFrom-2)
	result.Overflow = checkpointOverflow || result.AfterBytes > maxBytes
	return result
}

func latestCompleteToolUnitStart(messages []*schema.Message, fallback int) int {
	for index := len(messages) - 1; index >= 2; index-- {
		message := messages[index]
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		if len(message.ToolCalls) == 0 {
			return fallback
		}
		pending := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) != "" {
				pending[call.ID] = true
			}
		}
		for _, result := range messages[index+1:] {
			if result != nil && result.Role == schema.Tool {
				delete(pending, result.ToolCallID)
			}
		}
		if len(pending) == 0 && index < fallback {
			return index
		}
		return fallback
	}
	return fallback
}

func cloneMessageWithContentLimit(message *schema.Message, limit int) *schema.Message {
	if message == nil {
		return nil
	}
	clone := *message
	clone.Content = clipMiddle(clone.Content, limit)
	clone.ReasoningContent = clipMiddle(clone.ReasoningContent, limit/2)
	clone.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
	argumentLimit := max(128, limit/2)
	for index := range clone.ToolCalls {
		arguments := clone.ToolCalls[index].Function.Arguments
		clone.ToolCalls[index].Function.Arguments = compactToolArguments(
			arguments,
			argumentLimit,
		)
	}
	return &clone
}

func compactToolArguments(arguments string, limit int) string {
	if len(arguments) <= limit {
		return arguments
	}
	compacted, _ := json.Marshal(map[string]any{
		"bytes":     len(arguments),
		"compacted": true,
		"digest":    evidenceDigest("tool_arguments", arguments),
	})
	return string(compacted)
}

func shrinkMessageForContext(message *schema.Message) (*schema.Message, bool) {
	if message == nil {
		return nil, false
	}
	clone := *message
	changed := false
	if len(message.Content) > 64 {
		clone.Content = clipMiddle(
			message.Content,
			max(64, len(message.Content)*3/4),
		)
		changed = changed || clone.Content != message.Content
	}
	if len(message.ReasoningContent) > 32 {
		clone.ReasoningContent = clipMiddle(
			message.ReasoningContent,
			max(32, len(message.ReasoningContent)*3/4),
		)
		changed = changed || clone.ReasoningContent != message.ReasoningContent
	}
	clone.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
	for index := range clone.ToolCalls {
		arguments := clone.ToolCalls[index].Function.Arguments
		if len(arguments) <= 128 {
			continue
		}
		clone.ToolCalls[index].Function.Arguments = compactToolArguments(arguments, 128)
		changed = changed ||
			clone.ToolCalls[index].Function.Arguments != arguments
	}
	if !changed {
		return message, false
	}
	return &clone, true
}

func buildContextCheckpoint(messages []*schema.Message, maxBytes int) (string, bool) {
	type entry struct {
		Role       schema.RoleType `json:"role"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
		Tool       string          `json:"tool,omitempty"`
		Arguments  string          `json:"arguments,omitempty"`
		Evidence   json.RawMessage `json:"evidence,omitempty"`
		Excerpt    string          `json:"excerpt,omitempty"`
	}
	entries := make([]entry, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		if len(message.ToolCalls) > 0 {
			for _, call := range message.ToolCalls {
				entries = append(entries, entry{
					Role:       message.Role,
					ToolCallID: call.ID,
					Tool:       call.Function.Name,
					Arguments:  clipMiddle(call.Function.Arguments, 256),
				})
			}
			continue
		}
		item := entry{
			Role:       message.Role,
			ToolCallID: message.ToolCallID,
			Tool:       message.ToolName,
		}
		if message.Role == schema.Tool && json.Valid([]byte(message.Content)) {
			var raw map[string]json.RawMessage
			if json.Unmarshal([]byte(message.Content), &raw) == nil {
				preserved := map[string]json.RawMessage{}
				for _, key := range []string{"ok", "sources", "receipt", "error", "truncated"} {
					if value, ok := raw[key]; ok {
						preserved[key] = value
					}
				}
				item.Evidence, _ = json.Marshal(preserved)
				if content, ok := raw["content"]; ok {
					var value string
					if json.Unmarshal(content, &value) == nil {
						item.Excerpt = clipMiddle(value, 384)
					}
				}
			}
		} else {
			item.Excerpt = clipMiddle(message.Content, 384)
		}
		entries = append(entries, item)
	}
	data, _ := json.Marshal(map[string]any{
		"context_checkpoint": true,
		"instruction":        "This is compacted prior evidence. Preserve receipts and explicit unknowns; do not restart broad investigation solely because raw text was removed.",
		"entries":            entries,
	})
	if maxBytes <= 0 || len(data) <= maxBytes {
		return string(data), true
	}
	minimalEntries := append([]entry(nil), entries...)
	for index := range minimalEntries {
		minimalEntries[index].Evidence = nil
		minimalEntries[index].Excerpt = ""
		minimalEntries[index].Arguments = compactToolArguments(
			minimalEntries[index].Arguments,
			128,
		)
	}
	minimal, _ := json.Marshal(map[string]any{
		"context_checkpoint": true,
		"instruction":        "Compacted prior tool protocol; preserve call/result bindings.",
		"entries":            minimalEntries,
	})
	if len(minimal) <= maxBytes {
		return string(minimal), true
	}
	return string(minimal), false
}

func messageBytes(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		total += len(message.Content) + len(message.ReasoningContent)
		if data, err := json.Marshal(message.UserInputMultiContent); err == nil {
			total += len(data)
		}
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return total
}

func expireEphemeralImages(
	messages []*schema.Message,
) ([]*schema.Message, int) {
	out := append([]*schema.Message(nil), messages...)
	removed := 0
	for messageIndex, message := range messages {
		if message == nil || len(message.UserInputMultiContent) == 0 {
			continue
		}
		parts := append(
			[]schema.MessageInputPart(nil),
			message.UserInputMultiContent...,
		)
		changed := false
		for partIndex := range parts {
			part := parts[partIndex]
			if part.Type != schema.ChatMessagePartTypeImageURL ||
				part.Image == nil ||
				part.Image.URL == nil ||
				!strings.HasPrefix(*part.Image.URL, "data:image/") {
				continue
			}
			parts[partIndex] = schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: "[ephemeral image removed after the first model turn]",
			}
			removed++
			changed = true
		}
		if !changed {
			continue
		}
		clone := *message
		clone.UserInputMultiContent = parts
		out[messageIndex] = &clone
	}
	return out, removed
}

func clipMiddle(content string, maxBytes int) string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}
	marker := "\n... compacted ...\n"
	if maxBytes <= len(marker) {
		return marker[:maxBytes]
	}
	left := (maxBytes - len(marker)) * 2 / 3
	right := maxBytes - len(marker) - left
	for left > 0 && left < len(content) && content[left]&0xc0 == 0x80 {
		left--
	}
	startRight := len(content) - right
	for startRight < len(content) && content[startRight]&0xc0 == 0x80 {
		startRight++
	}
	return content[:left] + marker + content[startRight:]
}

func (l AgentLoop) recordModelStep(ctx context.Context, runID string, sequence int, assistant *schema.Message) error {
	if l.Recorder == nil {
		return nil
	}
	data, err := json.Marshal(assistant)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "encode model trajectory step").WithCause(err)
	}
	step := domain.AgentStep{
		RunID:        runID,
		Sequence:     sequence,
		Kind:         "model",
		Phase:        string(PhaseGenerate),
		Attempt:      1,
		FinishReason: "completed",
		OutputJSON:   string(data),
		CreatedAt:    time.Now().UTC(),
	}
	if len(assistant.ToolCalls) > 0 {
		step.FinishReason = "tool_calls"
	}
	if assistant.ResponseMeta != nil && assistant.ResponseMeta.Usage != nil {
		step.PromptTokens = assistant.ResponseMeta.Usage.PromptTokens
		step.CompletionTokens = assistant.ResponseMeta.Usage.CompletionTokens
	}
	if assistant.Extra != nil {
		if requestID, ok := assistant.Extra["request_id"].(string); ok {
			step.RequestID = requestID
		}
	}
	return l.Recorder.AppendAgentStep(ctx, step)
}

func (l AgentLoop) recordToolStep(ctx context.Context, runID string, sequence int, call schema.ToolCall, content string, toolErr error) error {
	if l.Recorder == nil {
		return nil
	}
	step := domain.AgentStep{
		RunID:      runID,
		Sequence:   sequence,
		Kind:       "tool",
		Phase:      string(PhaseExecuteTools),
		Attempt:    1,
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
		InputJSON:  call.Function.Arguments,
		OutputJSON: content,
		CreatedAt:  time.Now().UTC(),
	}
	if toolErr != nil {
		step.Error = toolErr.Error()
	}
	return l.Recorder.AppendAgentStep(ctx, step)
}

// SubmitDecisionDefinition is the terminal no-side-effect model tool.
func SubmitDecisionDefinition() agenttools.Definition {
	return agenttools.Definition{
		NonOwnerReadOnly: true,
		Info: &schema.ToolInfo{
			Name: "submit_decision",
			Desc: "Finish the owner-assistant task with a structured decision. This tool does not send a Lark message. Use it instead of shell for every sender-facing reply.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"decision": {
					Type:     schema.String,
					Required: true,
					Enum:     []string{"ignore", "record", "notify", "reply", "request_approval"},
					Desc:     "ignore only irrelevant non-delegated content; record a non-delegated owner-relevant update that needs no response; notification-origin resource_handoff must finish as notify and must never reply to an app, while human conversational resource_handoff must finish as reply or request_approval after bounded evidence correlation; delegated direct_mention or private_message work has already passed a semantic unanswered gate and must finish as a useful sender-facing reply or request_approval with exact reply_text; delegated assignments and coordination requests require completed bounded read work plus a concise initial finding, not an acknowledgement or restatement; assistant_request and owner_request cannot finish as notify only; coding questions must finish as reply and cannot use ignore, record, notify, or request_approval; request_approval is only for a non-coding risky response or personal commitment with an exact proposed reply_text",
				},
				"relevance_confidence": {Type: schema.Number, Required: true},
				"reply_confidence": {
					Type: schema.Number,
					Desc: "Required for reply decisions. Omission is invalid and must be repaired; use an explicit value below the configured threshold when human approval is needed.",
				},
				"risk": {
					Type:     schema.String,
					Required: true,
					Enum:     []string{"low", "medium", "high", "forbidden"},
					Desc:     "Use exactly one risk enum value; put all explanatory prose in reason.",
				},
				"evidence_status": {
					Type: schema.String,
					Enum: []string{"verified", "insufficient"},
					Desc: "Required for coding replies. Use verified only after an authoritative current-run read_workspace production read; use insufficient when a definite code claim cannot be supported. Insufficient free-form reply_text is replaced by a canonical evidence-limited response.",
				},
				"reply_outcome": {
					Type:     schema.String,
					Required: true,
					Enum:     []string{"complete", "partial", "clarification"},
					Desc:     "Required for reply decisions. complete answers every requested fact at the required evidence level; partial preserves supported findings and names exact unknowns; clarification asks for exact missing input or an ambiguous referent. This does not weaken evidence_status.",
				},
				"progress": {
					Type: schema.Object,
					Desc: "Structured bounded progress. Required for partial and clarification, and recommended for investigated complete replies. Every claimed completed check must have a current-run receipt.",
					SubParams: map[string]*schema.ParameterInfo{
						"completed_checks": {
							Type:     schema.Array,
							ElemInfo: &schema.ParameterInfo{Type: schema.String},
						},
						"initial_finding": {Type: schema.String},
						"unknowns": {
							Type:     schema.Array,
							ElemInfo: &schema.ParameterInfo{Type: schema.String},
						},
						"next_step": {Type: schema.String},
					},
				},
				"reply_text": {
					Type: schema.String,
					Desc: "Exact sender-facing text. Required for reply and request_approval. For delegated work, state completed read-only work, a concise initial finding or explicit unknown, and concrete information passed to the owner; do not merely acknowledge, restate, or promise future coordination. For verified coding replies, keep the structure as 结论、依据、未知/下一步 and cite authoritative production source_refs. Every repository-relative path in the reply must be cited, and every lower-camel-case code identifier must occur in the cited authoritative reads. An opaque String, bytes, raw JSON, or generic container declaration does not prove its concrete serialized shape; cite a current example, fixture, protocol, or serialization implementation before claiming that shape. For insufficient coding replies, the runtime emits canonical evidence-limited text. Lark mention placeholders like @_user_1 are internal keys from the mentions mapping: do not invent them, and do not use shell to send messages. The runtime renders known mention placeholders into Lark-native mentions and adds the robot marker only when replying as the owner on the owner's behalf.",
				},
				"owner_action": {
					Type: schema.String,
					Desc: "Concise private follow-up for the owner after a successful reply; provide it when owner work remains. Never put an internal classifier label here.",
				},
				"reason": {Type: schema.String, Required: true},
				"source_refs": {
					Type: schema.Array,
					Desc: "Cite only exact entries copied from a successful tool result's top-level sources array. Its source digest identifies the file or reference; never substitute receipt.result_digest, which identifies the whole tool output.",
					ElemInfo: &schema.ParameterInfo{
						Type: schema.Object,
						SubParams: map[string]*schema.ParameterInfo{
							"relative_path": {Type: schema.String},
							"digest":        {Type: schema.String},
							"kind":          {Type: schema.String},
						},
					},
				},
			}),
		},
		Execute: func(ctx context.Context, raw json.RawMessage) (agenttools.Execution, error) {
			decision, err := ParseDecision(string(raw))
			if err != nil {
				return agenttools.Execution{}, err
			}
			if scope, ok := agenttools.InvocationScopeFrom(ctx); ok && scope.WorkKind == domain.WorkKindSmartCommand {
				if decision.Kind != domain.DecisionRecord {
					return agenttools.Execution{}, errs.NewValidationError(
						errs.SubtypeInvalidArgument,
						"smart command must finish with decision=record",
					)
				}
				decision.ReplyText = ""
			}
			return agenttools.Execution{Content: `{"accepted":true}`, Decision: &decision}, nil
		},
	}
}

// SubmitInvestigationPlanDefinition records a bounded plan before broad coding search.
func SubmitInvestigationPlanDefinition() agenttools.Definition {
	return agenttools.Definition{
		NonOwnerReadOnly: true,
		Info: &schema.ToolInfo{
			Name: "submit_investigation_plan",
			Desc: "Submit a bounded read-only investigation plan for a coding question before broad workspace search. Use the structured fields question, entry_points, symbols, tools, and stop_conditions; do not send a free-form plan field. This tool has no external side effect.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"question":        {Type: schema.String, Required: true},
				"entry_points":    {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
				"symbols":         {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
				"tools":           {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
				"stop_conditions": {Type: schema.Array, Required: true, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
			}),
		},
		Execute: func(_ context.Context, raw json.RawMessage) (agenttools.Execution, error) {
			var plan struct {
				Question       string   `json:"question"`
				EntryPoints    []string `json:"entry_points"`
				Symbols        []string `json:"symbols"`
				Tools          []string `json:"tools"`
				StopConditions []string `json:"stop_conditions"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&plan); err != nil {
				return agenttools.Execution{}, errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"invalid investigation plan arguments: use structured fields question, entry_points, symbols, tools, and stop_conditions; a free-form plan field is not accepted",
				).WithCause(err)
			}
			if strings.TrimSpace(plan.Question) == "" || len(nonEmptyStrings(plan.EntryPoints)) == 0 ||
				len(nonEmptyStrings(plan.Tools)) == 0 || len(nonEmptyStrings(plan.StopConditions)) == 0 {
				return agenttools.Execution{}, errs.NewValidationError(
					errs.SubtypeInvalidArgument,
					"investigation plan requires question, entry_points, tools, and stop_conditions",
				)
			}
			data, err := json.Marshal(map[string]any{
				"accepted":        true,
				"question":        strings.TrimSpace(plan.Question),
				"entry_points":    nonEmptyStrings(plan.EntryPoints),
				"symbols":         nonEmptyStrings(plan.Symbols),
				"tools":           nonEmptyStrings(plan.Tools),
				"stop_conditions": nonEmptyStrings(plan.StopConditions),
			})
			if err != nil {
				return agenttools.Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "encode investigation plan").WithCause(err)
			}
			return agenttools.Execution{Content: string(data)}, nil
		},
	}
}

func hasMixedSubmitCalls(calls []schema.ToolCall) bool {
	if len(calls) <= 1 {
		return false
	}
	for _, call := range calls {
		if call.Function.Name == "submit_decision" {
			return true
		}
	}
	return false
}

func toolResultContent(execution agenttools.Execution, err error) string {
	if err != nil {
		data, _ := json.Marshal(map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return string(data)
	}
	data, marshalErr := json.Marshal(map[string]any{
		"ok":      true,
		"content": execution.Content,
		"sources": execution.Sources,
		"receipt": execution.Receipt,
	})
	if marshalErr != nil {
		return `{"ok":false,"error":"encode tool result"}`
	}
	return string(data)
}

func clipToolContent(content string, maxBytes int) string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content
	}
	if maxBytes < len(`{"ok":false,"truncated":true}`) {
		return `{}`
	}
	target := maxBytes / 2
	for target > 0 {
		encoded, err := json.Marshal(map[string]any{
			"ok":        true,
			"truncated": true,
			"excerpt":   clipMiddle(content, target),
		})
		if err == nil && len(encoded) <= maxBytes {
			return string(encoded)
		}
		target = target * 3 / 4
	}
	return `{"ok":false,"truncated":true}`
}

func validateDecisionSources(decision domain.Decision, allowed map[string]bool) error {
	for _, source := range decision.Sources {
		if !allowed[sourceKey(source)] {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"model decision referenced an unavailable source: %s",
				source.RelativePath,
			)
		}
	}
	return nil
}

func recordObservedSource(
	observed map[string]map[string]domain.SourceRef,
	source domain.SourceRef,
) {
	if strings.TrimSpace(source.RelativePath) == "" ||
		strings.TrimSpace(source.Kind) == "" ||
		strings.TrimSpace(source.Digest) == "" {
		return
	}
	identity := sourceIdentityKey(source)
	if observed[identity] == nil {
		observed[identity] = make(map[string]domain.SourceRef)
	}
	observed[identity][sourceKey(source)] = source
}

func canonicalizeDecisionSources(
	decision domain.Decision,
	allowed map[string]bool,
	observed map[string]map[string]domain.SourceRef,
) domain.Decision {
	sources := append([]domain.SourceRef(nil), decision.Sources...)
	for index, source := range sources {
		if allowed[sourceKey(source)] {
			continue
		}
		candidates := observed[sourceIdentityKey(source)]
		if len(candidates) != 1 {
			continue
		}
		for _, candidate := range candidates {
			sources[index] = candidate
		}
	}
	decision.Sources = sources
	return decision
}

func validateTerminalDecision(bundle agentcontext.Bundle, decision domain.Decision) error {
	if bundle.WorkKind == domain.WorkKindSmartCommand {
		if decision.Kind != domain.DecisionRecord {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"smart command must finish with decision=record",
			)
		}
		return nil
	}
	if bundle.WorkKind == domain.WorkKindResourceHandoff {
		if resourceHandoffIsNotification(bundle) {
			if decision.Kind == domain.DecisionNotify {
				return nil
			}
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"notification-origin resource handoff work must finish as an owner notification; never reply to the notification app",
			)
		}
		switch decision.Kind {
		case domain.DecisionReply, domain.DecisionRequestApproval:
			language := agentlocale.Language(decision.Language)
			if language != agentlocale.LanguageChinese && language != agentlocale.LanguageEnglish {
				return errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"resource handoff reply is missing a resolved output language",
				)
			}
			return agentlocale.ValidateProse(decision.ReplyText, language)
		default:
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"human conversational resource handoff work must finish with a useful sender-facing reply or request_approval",
			)
		}
	}
	delegated := isDelegatedInvocation(bundle)
	if delegated &&
		decision.Kind == domain.DecisionNotify &&
		isCodingBundle(bundle) {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"coding question cannot finish as notify; delegated work requires a useful sender-facing reply with completed checks and explicit unknowns",
		)
	}
	if delegated {
		switch decision.Kind {
		case domain.DecisionReply, domain.DecisionRequestApproval:
		case domain.DecisionIgnore, domain.DecisionRecord, domain.DecisionNotify:
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"delegated work cannot finish as %s after the semantic gate found it unanswered; submit a useful sender-facing reply or request_approval with exact reply_text",
				decision.Kind,
			)
		}
	}
	if decision.Kind == domain.DecisionReply || decision.Kind == domain.DecisionRequestApproval {
		language := agentlocale.Language(decision.Language)
		if language != agentlocale.LanguageChinese && language != agentlocale.LanguageEnglish {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"terminal reply is missing a resolved output language",
			)
		}
		if err := agentlocale.ValidateProse(decision.ReplyText, language); err != nil {
			return err
		}
	}
	if decision.Kind != domain.DecisionNotify {
		return nil
	}
	return errs.NewInternalError(
		errs.SubtypeInvalidResponse,
		"assistant_request and owner_request cannot finish as notify only; submit a useful reply or request_approval with exact reply_text",
	)
}

func resourceHandoffIsNotification(bundle agentcontext.Bundle) bool {
	switch strings.ToLower(strings.TrimSpace(bundle.Event.SenderType)) {
	case "app", "bot", "resource":
		return true
	default:
		return strings.TrimSpace(bundle.Event.ChatID) == ""
	}
}

func normalizeDecisionLanguage(bundle agentcontext.Bundle, decision domain.Decision) domain.Decision {
	language := resolvedBundleLanguage(bundle)
	decision.Language = string(language)
	return decision
}

func conversationText(events []domain.NormalizedEvent) string {
	var text strings.Builder
	for _, event := range events {
		text.WriteString(event.Content)
		text.WriteByte('\n')
	}
	return text.String()
}

func codingStructuralQuestion(bundle agentcontext.Bundle) string {
	current := strings.TrimSpace(bundle.Event.Content)
	if current == "" ||
		asksConcreteSerializedShape(current) ||
		!asksContextualSerializedShape(current) {
		return current
	}
	if linked := linkedStructuralContext(bundle.Event, bundle.Conversation); linked != "" {
		return current + "\n" + linked
	}
	for index := len(bundle.Conversation) - 1; index >= 0; index-- {
		candidate := bundle.Conversation[index]
		if candidate.MessageID != "" && candidate.MessageID == bundle.Event.MessageID {
			continue
		}
		if strings.TrimSpace(candidate.Content) == "" {
			continue
		}
		if hasSerializedShapeTarget(strings.ToLower(candidate.Content)) {
			return current + "\n" + candidate.Content
		}
		break
	}
	return current
}

func linkedStructuralContext(
	current domain.NormalizedEvent,
	conversation []domain.NormalizedEvent,
) string {
	linkedIDs := map[string]bool{
		current.ReplyToMessageID: true,
		current.RootMessageID:    true,
	}
	delete(linkedIDs, "")
	if len(linkedIDs) == 0 {
		return ""
	}
	for index := len(conversation) - 1; index >= 0; index-- {
		candidate := conversation[index]
		if !linkedIDs[candidate.MessageID] {
			continue
		}
		if hasSerializedShapeTarget(strings.ToLower(candidate.Content)) {
			return candidate.Content
		}
	}
	return ""
}

func verifyCodingDecision(
	bundle agentcontext.Bundle,
	decision domain.Decision,
	allowed map[string]bool,
	authoritative map[string]bool,
	codingEvidenceReads int,
) error {
	if !isCodingBundle(bundle) {
		return nil
	}
	if decision.Kind != domain.DecisionReply {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"coding question cannot finish as %s; submit a verified or insufficient reply",
			decision.Kind,
		)
	}
	if strings.TrimSpace(decision.ReplyText) == "" {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "coding reply does not answer the original question")
	}
	if decision.Risk == domain.RiskHigh || decision.Risk == domain.RiskForbidden {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "coding reply with high or forbidden risk requires approval")
	}
	if decision.ReplyOutcome == domain.ReplyOutcomeClarification {
		if decision.EvidenceStatus != domain.EvidenceInsufficient {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"coding clarification must use evidence_status=insufficient because it makes no verified code claim",
			)
		}
		if len(decision.Progress.Unknowns) == 0 || strings.TrimSpace(decision.Progress.NextStep) == "" {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"coding clarification requires exact unknowns and next_step",
			)
		}
		return nil
	}
	if decision.EvidenceStatus == domain.EvidenceInsufficient {
		if codingEvidenceReads == 0 {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"insufficient coding reply requires at least one successful workspace code investigation",
			)
		}
		return nil
	}
	if decision.EvidenceStatus != domain.EvidenceVerified {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"coding reply missing evidence_status; use verified or insufficient",
		)
	}
	if len(decision.Sources) == 0 {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"verified coding reply has no cited code evidence",
		)
	}
	for _, source := range decision.Sources {
		if !allowed[sourceKey(source)] {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"coding reply source is not backed by a tool receipt: %s",
				source.RelativePath,
			)
		}
	}
	if !hasProductionSource(decision.Sources) {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"verified coding reply has supporting evidence only; cite production source or use evidence_status=insufficient",
		)
	}
	for _, source := range decision.Sources {
		if authoritative[sourceKey(source)] {
			return nil
		}
	}
	return errs.NewInternalError(
		errs.SubtypeInvalidResponse,
		"verified coding reply has no authoritative production read; use read_workspace before making a definite code claim",
	)
}

const canonicalInsufficientCodingReply = "结论：当前证据不足，不能确认你询问的代码事实。\n" +
	"依据：已完成有界的工作区代码定位，但没有取得足以支撑确定结论的生产源码证据。\n" +
	"未知/下一步：相关符号是否存在及实际行为仍未核实，我不会据此推测。"

func canonicalCodingClarification(
	bundle agentcontext.Bundle,
	progress domain.DecisionProgress,
) string {
	if resolvedBundleLanguage(bundle) == agentlocale.LanguageEnglish {
		return fmt.Sprintf(
			"Conclusion: the input required to verify the code fact is missing, so no code claim can be made.\nStill unknown: %s.\nNext step: %s.",
			strings.Join(progress.Unknowns, "; "),
			progress.NextStep,
		)
	}
	return fmt.Sprintf(
		"结论：当前缺少核对代码事实所需的输入，不能据此作出代码断言。\n仍未知：%s。\n下一步：%s。",
		strings.Join(progress.Unknowns, "；"),
		progress.NextStep,
	)
}

func normalizeCodingDecision(bundle agentcontext.Bundle, decision domain.Decision) domain.Decision {
	return normalizeCodingDecisionWithSearchEvidence(
		bundle,
		decision,
		codingSearchEvidence{},
	)
}

func normalizeCodingDecisionWithSearchEvidence(
	bundle agentcontext.Bundle,
	decision domain.Decision,
	searches codingSearchEvidence,
) domain.Decision {
	if isCodingBundle(bundle) &&
		decision.Kind == domain.DecisionReply &&
		decision.EvidenceStatus == domain.EvidenceInsufficient {
		if decision.ReplyOutcome == domain.ReplyOutcomeClarification {
			decision.ReplyText = canonicalCodingClarification(bundle, decision.Progress)
			return decision
		}
		if searches.canRenderNegativeResult() {
			decision.ReplyText = searches.renderNegativeResult(bundle)
		} else {
			decision.ReplyText = canonicalInsufficientCodingReply
		}
	}
	return decision
}

func isCodingBundle(bundle agentcontext.Bundle) bool {
	return bundle.WorkKind == domain.WorkKindCodingQuestion ||
		domain.IsCodingQuestion(bundle.Event.Content)
}

func isRelevantEvidenceTool(name string) bool {
	switch name {
	case "get_lark_context",
		"search_lark_messages",
		"search_related_lark_evidence",
		"get_resource_evidence",
		"get_github_context",
		"explore_workspace",
		"search_workspace",
		"read_workspace",
		"inspect_git_history",
		"search_code_symbols",
		"trace_code_path":
		return true
	default:
		return false
	}
}

func isCodingEvidenceTool(name string) bool {
	switch name {
	case "explore_workspace",
		"search_workspace",
		"read_workspace",
		"search_code_symbols",
		"trace_code_path":
		return true
	default:
		return false
	}
}

func toolContentNoNewContext(content string) bool {
	var payload struct {
		NoNewContext bool `json:"no_new_context"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err == nil && payload.NoNewContext {
		return true
	}
	return strings.Contains(content, `"no_new_context":true`)
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func normalizeToolArguments(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return strings.TrimSpace(raw)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return string(normalized)
}

func toolResultFingerprint(toolName, arguments, content string, toolErr error) string {
	category := "success"
	if toolErr != nil {
		category = "error"
		if problem, ok := errs.ProblemOf(toolErr); ok {
			category = string(problem.Subtype)
		}
	}
	return evidenceDigest(
		strings.TrimSpace(toolName)+"\x00"+normalizeToolArguments(arguments)+"\x00"+category,
		content,
	)
}

func toolFingerprintSummary(execution agenttools.Execution, toolErr error) string {
	if toolErr != nil {
		return toolErr.Error()
	}
	return strings.TrimSpace(execution.Content)
}

func sourceKey(source domain.SourceRef) string {
	return source.Kind + "\x00" + source.RelativePath + "\x00" + source.Digest
}

func modelOutputTruncated(message *schema.Message) bool {
	if message == nil || message.ResponseMeta == nil {
		return false
	}
	reason := strings.ToLower(strings.TrimSpace(message.ResponseMeta.FinishReason))
	return reason == "truncated" || reason == "length"
}

func toolCallPath(arguments string) string {
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(payload.Path)))
}

func invalidateWorkspaceFileSources(
	allowed map[string]bool,
	observed map[string]map[string]domain.SourceRef,
	authoritative map[string]bool,
	contents map[string]string,
	rel string,
) {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "" || rel == "." {
		return
	}
	identity := "workspace_file\x00" + rel
	for key := range observed[identity] {
		delete(allowed, key)
		delete(authoritative, key)
		delete(contents, key)
	}
	delete(observed, identity)
}

func sourceIdentityKey(source domain.SourceRef) string {
	return source.Kind + "\x00" + source.RelativePath
}

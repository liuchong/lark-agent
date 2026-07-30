package runtime

import (
	"context"
	"encoding/json"
	"fmt"
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
	if guardedDecision, guarded := guardedRequestDecision(bundle); guarded {
		return guardedDecision, nil, nil
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
		l.ToolChoice = schema.ToolChoiceForced
	}
	invocationScope := agenttools.InvocationScope{
		Owner:           bundle.User.OpenID != "" && bundle.Event.SenderID == bundle.User.OpenID,
		ReadOnly:        bundle.User.OpenID == "" || bundle.Event.SenderID != bundle.User.OpenID,
		ChatID:          bundle.Event.ChatID,
		GitHubReference: bundle.GitHubReference,
	}
	visibleToolInfos := l.Tools.InfosFor(invocationScope)
	bundle = filterBundleTools(bundle, visibleToolInfos, invocationScope)
	messages := []*schema.Message{
		schema.SystemMessage(l.SystemPrompt),
		initialUserMessage(bundle),
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
	authoritativeSources := make(map[string]bool)
	codingEvidenceReads := 0
	for _, source := range bundle.Sources {
		allowedSources[sourceKey(source)] = true
	}
	totalToolBytes := 0
	repeatedCalls := map[string]int{}
	sourceLessWorkspaceSearches := 0
	requestedWorkspaceScope := requestedCodingWorkspaceScope(bundle)
	toolBudgetConvergencePrompted := false
	codingEvidenceConvergencePrompted := false
	codingEvidenceAvailable := false
	var codingSearches codingSearchEvidence
	investigationPlanSubmitted := false
	noProgressLarkContext := false
	toolCalls := 0
	noProgressStreak := 0
	lastObservation := ""
	forceDecision := false
	terminalOnlyAttempts := 0
	evidence := responseEvidence{}
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
		compaction := compactMessages(messages, l.MaxContextBytes, l.ContextCompaction)
		messages = compaction.Messages
		requestMessages := append([]*schema.Message(nil), messages...)
		budget := runBudget{
			CurrentTurn:      turn + 1,
			MaxTurns:         l.MaxTurns,
			ContextBytes:     messageBytes(messages),
			MaxContextBytes:  l.MaxContextBytes,
			Compacted:        compaction.Compacted,
			ReplacedMessages: compaction.ReplacedMessages,
			TargetLanguage:   resolvedBundleLanguage(bundle),
		}
		requestMessages = append(requestMessages, schema.SystemMessage(modelRunProgressPrompt(budget)))
		if budget.RemainingTurns() == 0 {
			forceDecision = true
		}
		if bundle.WorkKind == domain.WorkKindCodingQuestion &&
			codingEvidenceAvailable &&
			budget.RemainingTurns() <= 1 {
			forceDecision = true
		}
		if toolCalls >= l.MaxToolCalls {
			forceDecision = true
		}
		terminalOnly := forceDecision
		turnToolInfos := visibleToolInfos
		if terminalOnly {
			if terminalOnlyAttempts >= maxTerminalOnlyAttempts {
				return domain.Decision{}, trajectory, errs.NewInternalError(
					errs.SubtypeModelNonConvergence,
					"model did not submit a terminal decision after %d attempts",
					maxTerminalOnlyAttempts,
				)
			}
			terminalOnlyAttempts++
			turnToolInfos = submitDecisionOnly(visibleToolInfos)
			requestMessages = append(requestMessages, schema.SystemMessage(
				terminalOnlyPrompt(terminalOnlyAttempts, maxTerminalOnlyAttempts),
			))
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
		turnMadeProgress := false
		turnHadNoProgress := false
		for _, call := range assistant.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
				return domain.Decision{}, trajectory, errs.NewInternalError(errs.SubtypeInvalidResponse, "model tool call is missing id or name")
			}
			callSignature := call.Function.Name + "\x00" + call.Function.Arguments
			repeatedCalls[callSignature]++
			if repeatedCalls[callSignature] > l.MaxNoProgress {
				forceDecision = true
			} else if repeatedCalls[callSignature] > l.MaxRepeatedCalls {
				return domain.Decision{}, trajectory, errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"agent repeated the same tool call without progress: %s",
					call.Function.Name,
				)
			}
			toolCtx := agenttools.WithWorkItemDedup(ctx, domain.DedupKey(bundle.Event))
			toolCtx = agenttools.WithInvocationScope(toolCtx, invocationScope)
			var execution agenttools.Execution
			var toolErr error
			if forceDecision && call.Function.Name != "submit_decision" {
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
			} else if isCodingBundle(bundle) && !investigationPlanSubmitted && call.Function.Name == "search_workspace" {
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
			} else if call.Function.Name == "search_workspace" && sourceLessWorkspaceSearches >= 3 {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"search_workspace is exhausted for this work item after repeated searches without citable sources; use list_workspace, read_workspace, a path-specific shell command, or submit_decision with explicit unknowns",
				)
			} else if isCodingBundle(bundle) && requestedWorkspaceScope != "" {
				toolErr = validateCodingWorkspaceScope(
					call.Function.Name,
					call.Function.Arguments,
					requestedWorkspaceScope,
				)
				if toolErr == nil {
					execution, toolErr = l.Tools.Execute(toolCtx, call.Function.Name, json.RawMessage(call.Function.Arguments))
					if toolErr == nil && call.Function.Name != "submit_decision" {
						toolCalls++
					}
				}
			} else {
				execution, toolErr = l.Tools.Execute(toolCtx, call.Function.Name, json.RawMessage(call.Function.Arguments))
				if toolErr == nil && call.Function.Name != "submit_decision" {
					toolCalls++
				}
			}
			if toolErr == nil && execution.Decision != nil {
				normalized := normalizeDecisionLanguage(bundle, *execution.Decision)
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
			if toolErr == nil && execution.Decision != nil {
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
			if toolErr == nil && execution.Decision == nil && call.Function.Name == "search_workspace" && len(execution.Sources) == 0 {
				sourceLessWorkspaceSearches++
			}
			if toolErr == nil && call.Function.Name == "submit_investigation_plan" {
				investigationPlanSubmitted = true
			}
			if toolErr == nil && call.Function.Name == "get_lark_context" && toolContentNoNewContext(execution.Content) {
				noProgressLarkContext = true
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
					call.Function.Arguments,
					execution.Content,
				)
			}
			content := toolResultContent(execution, toolErr)
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
			if !toolBudgetConvergencePrompted && shouldPromptToolBudgetConvergence(rawToolBytes, l.MaxToolBytes, totalToolBytes, l.MaxTotalBytes) {
				messages = append(messages, schema.SystemMessage(toolBudgetConvergencePrompt()))
				toolBudgetConvergencePrompted = true
			}
			for _, source := range execution.Sources {
				allowedSources[sourceKey(source)] = true
				if call.Function.Name == "read_workspace" &&
					hasProductionSource([]domain.SourceRef{source}) {
					authoritativeSources[sourceKey(source)] = true
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
					messages = append(messages, schema.SystemMessage(codingEvidenceConvergencePrompt()))
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
	case domain.WorkKindCodingQuestion:
		if l.CodingMaxTurns > 0 && l.MaxTurns > l.CodingMaxTurns {
			return l.CodingMaxTurns
		}
	case domain.WorkKindCodingGoal:
		if l.GoalMaxTurns > 0 && l.MaxTurns > l.GoalMaxTurns {
			return l.GoalMaxTurns
		}
	}
	return l.MaxTurns
}

func modelTurnBudgetPrompt(maxTurns int) string {
	return fmt.Sprintf(
		"The hard model-turn limit for this run is %d. Plan the investigation within this budget and call submit_decision before the limit is exhausted.",
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
		"Current model turn: %d of %d. Remaining model turns after this request: %d. Use the remaining budget deliberately and converge on submit_decision.",
		currentTurn,
		maxTurns,
		remaining,
	)
}

type runBudget struct {
	CurrentTurn      int
	MaxTurns         int
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

func modelRunProgressPrompt(budget runBudget) string {
	remainingBytes := budget.MaxContextBytes - budget.ContextBytes
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	percent := 0
	if budget.MaxContextBytes > 0 {
		percent = budget.ContextBytes * 100 / budget.MaxContextBytes
	}
	urgency := "normal"
	if percent >= 80 || budget.RemainingTurns()*5 <= budget.MaxTurns {
		urgency = "urgent"
	}
	return fmt.Sprintf(
		"%s Context budget: %d of %d bytes used (%d%%), %d bytes remaining. "+
			"Automatic compaction: %t; replaced old messages: %d. Urgency: %s. "+
			"Required outward language: %s; use that language for all explanatory prose. "+
			"When urgency is urgent, stop broad investigation, preserve explicit unknowns, "+
			"and converge on submit_decision.",
		modelTurnProgressPrompt(budget.CurrentTurn, budget.MaxTurns),
		budget.ContextBytes,
		budget.MaxContextBytes,
		percent,
		remainingBytes,
		budget.Compacted,
		budget.ReplacedMessages,
		urgency,
		budget.TargetLanguage,
	)
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

func terminalOnlyPrompt(attempt, maxAttempts int) string {
	return fmt.Sprintf(
		"Only submit_decision is available now. Previous investigation tools are no longer available. "+
			"Call submit_decision in this turn using verified facts and explicit unknowns. "+
			"This is terminal-only attempt %d of %d.",
		attempt,
		maxAttempts,
	)
}

func codingEvidenceConvergencePrompt() string {
	return "Citable workspace evidence is now available. If it answers the concrete fields the user asked for, call submit_decision now. Do not expand into unrelated Lark history, repository-wide searches, or production call-site proof unless the user explicitly asked about reachability. For an exact function's direct behavior, its digest-backed definition is sufficient; preserve explicit unknowns instead of over-investigating."
}

func submitDecisionOnly(infos []*schema.ToolInfo) []*schema.ToolInfo {
	for _, info := range infos {
		if info != nil && info.Name == "submit_decision" {
			return []*schema.ToolInfo{info}
		}
	}
	return infos
}

type compactionResult struct {
	Messages         []*schema.Message
	BeforeBytes      int
	AfterBytes       int
	Compacted        bool
	ReplacedMessages int
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
	compacted := make([]*schema.Message, 0, len(messages))
	if len(messages) > 0 {
		compacted = append(compacted, cloneMessageWithContentLimit(messages[0], maxBytes/8))
	}
	if len(messages) > 1 {
		compacted = append(compacted, cloneMessageWithContentLimit(messages[1], maxBytes/3))
	}
	if protectedFrom > 2 {
		checkpoint := buildContextCheckpoint(messages[2:protectedFrom], maxBytes/4)
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
			if compacted[i] == nil || len(compacted[i].Content) <= 512 {
				continue
			}
			compacted[i] = cloneMessageWithContentLimit(compacted[i], len(compacted[i].Content)*3/4)
			changed = true
		}
		if !changed {
			break
		}
	}
	result.Messages = compacted
	result.AfterBytes = messageBytes(compacted)
	result.Compacted = true
	result.ReplacedMessages = max(0, protectedFrom-2)
	return result
}

func cloneMessageWithContentLimit(message *schema.Message, limit int) *schema.Message {
	if message == nil {
		return nil
	}
	clone := *message
	clone.Content = clipMiddle(clone.Content, limit)
	clone.ReasoningContent = clipMiddle(clone.ReasoningContent, limit/2)
	return &clone
}

func buildContextCheckpoint(messages []*schema.Message, maxBytes int) string {
	type entry struct {
		Role      schema.RoleType `json:"role"`
		Tool      string          `json:"tool,omitempty"`
		Arguments string          `json:"arguments,omitempty"`
		Evidence  json.RawMessage `json:"evidence,omitempty"`
		Excerpt   string          `json:"excerpt,omitempty"`
	}
	entries := make([]entry, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		item := entry{Role: message.Role, Tool: message.ToolName}
		if len(message.ToolCalls) > 0 {
			item.Tool = message.ToolCalls[0].Function.Name
			item.Arguments = clipMiddle(message.ToolCalls[0].Function.Arguments, 256)
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
		return string(data)
	}
	target := maxBytes / 2
	for target > 64 {
		bounded, _ := json.Marshal(map[string]any{
			"context_checkpoint": true,
			"instruction":        "Compacted prior evidence; preserve receipts and explicit unknowns.",
			"compacted_entries":  clipMiddle(string(data), target),
		})
		if len(bounded) <= maxBytes {
			return string(bounded)
		}
		target = target * 3 / 4
	}
	return `{"context_checkpoint":true,"instruction":"Prior evidence was compacted; do not restart broad investigation."}`
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
		RunID:      runID,
		Sequence:   sequence,
		Kind:       "model",
		OutputJSON: string(data),
		CreatedAt:  time.Now().UTC(),
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
					Desc:     "ignore only irrelevant non-delegated content; record a non-delegated owner-relevant update that needs no response; delegated direct_mention or private_message work has already passed a semantic unanswered gate and must finish as a useful sender-facing reply or request_approval with exact reply_text; delegated assignments and coordination requests require completed bounded read work plus a concise initial finding, not an acknowledgement or restatement; assistant_request and owner_request cannot finish as notify only; coding questions must finish as reply and cannot use ignore, record, notify, or request_approval; request_approval is only for a non-coding risky response or personal commitment with an exact proposed reply_text",
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
				"reply_text": {
					Type: schema.String,
					Desc: "Exact sender-facing text. Required for reply and request_approval. For delegated work, state completed read-only work, a concise initial finding or explicit unknown, and concrete information passed to the owner; do not merely acknowledge, restate, or promise future coordination. For verified coding replies, keep the structure as 结论、依据、未知/下一步 and cite authoritative production source_refs. For insufficient coding replies, the runtime emits canonical evidence-limited text. Lark mention placeholders like @_user_1 are internal keys from the mentions mapping: do not invent them, and do not use shell to send messages. The runtime renders known mention placeholders into Lark-native mentions and adds the robot marker only when replying as the owner on the owner's behalf.",
				},
				"owner_action": {
					Type: schema.String,
					Desc: "Concise private follow-up for the owner after a successful reply; provide it when owner work remains. Never put an internal classifier label here.",
				},
				"reason": {Type: schema.String, Required: true},
				"source_refs": {
					Type: schema.Array,
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
		Execute: func(_ context.Context, raw json.RawMessage) (agenttools.Execution, error) {
			decision, err := ParseDecision(string(raw))
			if err != nil {
				return agenttools.Execution{}, err
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

func validateTerminalDecision(bundle agentcontext.Bundle, decision domain.Decision) error {
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
		"get_github_context",
		"explore_workspace",
		"search_workspace",
		"read_workspace",
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

func sourceKey(source domain.SourceRef) string {
	return source.Kind + "\x00" + source.RelativePath + "\x00" + source.Digest
}

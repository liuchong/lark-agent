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
		l.MaxContextBytes = 192 * 1024
	}
	if l.MaxElapsed <= 0 {
		l.MaxElapsed = 10 * time.Minute
	}
	if l.MaxRepeatedCalls <= 0 {
		l.MaxRepeatedCalls = 3
	}
	if l.MaxToolCalls <= 0 {
		l.MaxToolCalls = 10
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
	messages := []*schema.Message{
		schema.SystemMessage(l.SystemPrompt),
		schema.UserMessage(agentcontext.AgentUserPrompt(bundle)),
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
	for _, source := range bundle.Sources {
		allowedSources[sourceKey(source)] = true
	}
	totalToolBytes := 0
	repeatedCalls := map[string]int{}
	sourceLessWorkspaceSearches := 0
	toolBudgetConvergencePrompted := false
	investigationPlanSubmitted := false
	noProgressLarkContext := false
	toolCalls := 0
	noProgressStreak := 0
	lastObservation := ""
	forceDecision := false
	for turn := 0; turn < l.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return domain.Decision{}, trajectory, err
		}
		messages = compactMessages(messages, l.MaxContextBytes)
		requestMessages := append([]*schema.Message(nil), messages...)
		requestMessages = append(requestMessages, schema.SystemMessage(modelTurnProgressPrompt(turn+1, l.MaxTurns)))
		assistant, err := l.Model.Generate(ctx, requestMessages,
			einomodel.WithTools(l.Tools.Infos()),
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
			var execution agenttools.Execution
			var toolErr error
			if call.Function.Name != "submit_decision" {
				toolCalls++
			}
			if forceDecision && call.Function.Name != "submit_decision" {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"investigation made no progress or exhausted its tool budget; submit_decision now with verified facts and explicit unknowns",
				)
			} else if toolCalls > l.MaxToolCalls && call.Function.Name != "submit_decision" {
				toolErr = errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"tool-call budget exhausted after %d calls; submit_decision now",
					l.MaxToolCalls,
				)
				forceDecision = true
			} else if isCodingQuestion(bundle.Event.Content) && !investigationPlanSubmitted && call.Function.Name == "search_workspace" {
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
			} else {
				execution, toolErr = l.Tools.Execute(toolCtx, call.Function.Name, json.RawMessage(call.Function.Arguments))
			}
			if toolErr == nil && execution.Decision != nil {
				if err := validateTerminalDecision(bundle, *execution.Decision); err != nil {
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
			content := toolResultContent(execution, toolErr)
			observation := callSignature + "\x00" + content
			if observation == lastObservation || toolErr != nil {
				noProgressStreak++
			} else {
				noProgressStreak = 0
			}
			lastObservation = observation
			if noProgressStreak >= l.MaxNoProgress {
				forceDecision = true
			}
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
			}
			if execution.Decision != nil {
				if err := validateDecisionSources(*execution.Decision, allowedSources); err != nil {
					return domain.Decision{}, trajectory, err
				}
				if err := verifyCodingDecision(bundle, *execution.Decision, allowedSources); err != nil {
					return domain.Decision{}, trajectory, err
				}
				if l.Recorder != nil {
					if err := l.Recorder.FinishAgentRun(ctx, runID, domain.AgentRunCompleted, ""); err != nil {
						return domain.Decision{}, trajectory, err
					}
					runFinished = true
				}
				return *execution.Decision, trajectory, nil
			}
		}
	}
	return domain.Decision{}, trajectory, errs.NewInternalError(errs.SubtypeInvalidResponse, "agent loop exceeded maximum turns")
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

func compactMessages(messages []*schema.Message, maxBytes int) []*schema.Message {
	if maxBytes <= 0 || messageBytes(messages) <= maxBytes {
		return messages
	}
	compacted := append([]*schema.Message(nil), messages...)
	protectedFrom := len(compacted) - 4
	if protectedFrom < 2 {
		protectedFrom = 2
	}
	for i := 2; i < protectedFrom; i++ {
		if compacted[i] == nil || compacted[i].Role != schema.Tool || len(compacted[i].Content) <= 1024 {
			continue
		}
		clone := *compacted[i]
		clone.Content = clipMiddle(clone.Content, 1024)
		compacted[i] = &clone
	}
	for i := 1; i < len(compacted) && messageBytes(compacted) > maxBytes; i++ {
		if compacted[i] == nil || len(compacted[i].Content) <= 1024 {
			continue
		}
		clone := *compacted[i]
		over := messageBytes(compacted) - maxBytes
		target := len(clone.Content) - over
		if target < 1024 {
			target = 1024
		}
		clone.Content = clipMiddle(clone.Content, target)
		compacted[i] = &clone
	}
	return compacted
}

func messageBytes(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		total += len(message.Content) + len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return total
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
		Info: &schema.ToolInfo{
			Name: "submit_decision",
			Desc: "Finish the owner-assistant task with a structured decision. This tool does not send a Lark message. Use it instead of shell for every sender-facing reply.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"decision": {
					Type:     schema.String,
					Required: true,
					Enum:     []string{"ignore", "record", "notify", "reply", "request_approval"},
					Desc:     "ignore only owner-irrelevant content; record a relevant update that needs no response; reply to a direct owner question, owner_request, status update, handoff, or coordination request whenever a safe useful owner response can acknowledge it without inventing a commitment, even when remaining owner work or unknowns remain—the runtime then privately notifies the owner that it replied and what work remains; direct owner mentions cannot finish as notify only, because incomplete facts should be stated as unknowns in reply_text; owner_request messages also cannot finish as notify only; notify only for owner-relevant messages that do not directly mention the owner or when a sender-facing reply would expose sensitive private context; request_approval only with an exact proposed reply_text for a risky response or personal commitment",
				},
				"relevance_confidence": {Type: schema.Number, Required: true},
				"reply_confidence":     {Type: schema.Number},
				"risk": {
					Type:     schema.String,
					Required: true,
					Enum:     []string{"low", "medium", "high", "forbidden"},
					Desc:     "Use exactly one risk enum value; put all explanatory prose in reason.",
				},
				"reply_text": {
					Type: schema.String,
					Desc: "Exact sender-facing text. Required for reply and request_approval. For coding replies, keep the structure as 结论、依据、未知/下一步; cite source_refs for definite code claims, or explicitly state unknowns when code evidence is insufficient. Lark mention placeholders like @_user_1 are internal keys from the mentions mapping: do not invent them, and do not use shell to send messages. The runtime renders known mention placeholders into Lark-native mentions and adds the robot marker only when replying as the owner on the owner's behalf.",
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
		Info: &schema.ToolInfo{
			Name: "submit_investigation_plan",
			Desc: "Submit a bounded read-only investigation plan for a coding question before broad workspace search. This tool has no external side effect.",
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
				return agenttools.Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid investigation plan arguments").WithCause(err)
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
	if decision.Kind != domain.DecisionNotify {
		return nil
	}
	if !bundle.Event.MentionsUser(bundle.User.OpenID) {
		return nil
	}
	return errs.NewInternalError(
		errs.SubtypeInvalidResponse,
		"direct owner mention cannot finish as notify only; submit a reply with exact reply_text, or request_approval with exact reply_text if the sender-facing response contains a risky commitment",
	)
}

func verifyCodingDecision(bundle agentcontext.Bundle, decision domain.Decision, allowed map[string]bool) error {
	if !isCodingQuestion(bundle.Event.Content) || decision.Kind != domain.DecisionReply {
		return nil
	}
	if strings.TrimSpace(decision.ReplyText) == "" {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "coding reply does not answer the original question")
	}
	if decision.Risk == domain.RiskHigh || decision.Risk == domain.RiskForbidden {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "coding reply with high or forbidden risk requires approval")
	}
	if len(decision.Sources) == 0 && !replyStatesInsufficientEvidence(decision.ReplyText) {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"coding reply has no cited code evidence; cite source_refs or state the unknowns and next confirmation step",
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
	return nil
}

func replyStatesInsufficientEvidence(reply string) bool {
	for _, marker := range []string{
		"没有足够",
		"不能确认",
		"无法确认",
		"还不能确认",
		"需要",
		"未知",
		"缺少",
		"not enough",
		"cannot confirm",
		"unknown",
	} {
		if strings.Contains(strings.ToLower(reply), marker) {
			return true
		}
	}
	return false
}

func isCodingQuestion(content string) bool {
	lower := strings.ToLower(content)
	for _, marker := range []string{
		"api",
		"接口",
		"代码",
		"数据库",
		"sampledb",
		"redis",
		"sdk",
		"回调",
		"限流",
		"缓存",
		"高频",
		"endpoint",
		"handler",
		"service",
		"repository",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	modelruntime "github.com/liuchong/lark-agent/agent/runtime/model"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// OpenAICompatibleModel adapts an OpenAI Chat Completions compatible endpoint
// to Eino's BaseModel interface.
type OpenAICompatibleModel struct {
	APIKey  string
	BaseURL string
	Model   string
	// Timeout bounds one attempt, not the whole call.
	Timeout time.Duration
	// MaxAttempts bounds how many attempts one call spends on retryable
	// failures. Attempts after the first are only sent for failures the model
	// runtime classifies as retryable.
	MaxAttempts int
	// RetryBackoff is the wait before the second attempt; it doubles for each
	// later attempt and yields to a provider `Retry-After`.
	RetryBackoff time.Duration
	// Profile carries the declared provider traits, such as reasoning behavior
	// and output limits, that belong on the wire.
	Profile modelruntime.Profile
	Client  *http.Client
	Tools   []*schema.ToolInfo
}

type retryAfterError struct {
	err   error
	delay time.Duration
}

func (e *retryAfterError) Error() string             { return e.err.Error() }
func (e *retryAfterError) Unwrap() error             { return e.err }
func (e *retryAfterError) RetryAfter() time.Duration { return e.delay }

type classifiedModelError struct {
	err     error
	failure modelruntime.Failure
}

func (e *classifiedModelError) Error() string                 { return e.err.Error() }
func (e *classifiedModelError) Unwrap() error                 { return e.err }
func (e *classifiedModelError) Failure() modelruntime.Failure { return e.failure }

// WithTools returns an immutable model view with the supplied tools bound.
func (m *OpenAICompatibleModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if m == nil {
		return nil, errs.NewInternalError(errs.SubtypeUnknown, "bind tools on nil model")
	}
	clone := *m
	clone.Tools = append([]*schema.ToolInfo(nil), tools...)
	return &clone, nil
}

// Generate calls /chat/completions and returns the first assistant message. A
// failure the model runtime classifies as retryable is retried within the
// profile's attempt budget; a deterministic failure such as a rejected key
// returns after one round trip.
func (m *OpenAICompatibleModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	payload, err := m.encodeRequest(input, opts...)
	if err != nil {
		return nil, err
	}
	attempts := m.maxAttempts()
	for attempt := 1; ; attempt++ {
		msg, failure, err := m.attempt(ctx, payload)
		if err == nil {
			return msg, nil
		}
		if attempt >= attempts || !failure.Retryable {
			return nil, &classifiedModelError{err: err, failure: failure}
		}
		if waitErr := waitBeforeRetry(ctx, failure.RetryAfter, m.backoffFor(attempt)); waitErr != nil {
			// The caller went away while waiting. Report why the call failed,
			// not that the wait was cut short.
			return nil, &classifiedModelError{err: err, failure: failure}
		}
	}
}

func (m *OpenAICompatibleModel) encodeRequest(input []*schema.Message, opts ...einomodel.Option) ([]byte, error) {
	cfg := einomodel.GetCommonOptions(&einomodel.Options{Tools: append([]*schema.ToolInfo(nil), m.Tools...)}, opts...)
	modelName := m.Model
	if cfg.Model != nil && *cfg.Model != "" {
		modelName = *cfg.Model
	}
	profile := m.runtimeProfile(modelName)
	var tools []modelruntime.Tool
	if len(cfg.Tools) > 0 && m.allowsToolUse() {
		var err error
		tools, err = toModelTools(cfg.Tools)
		if err != nil {
			return nil, err
		}
	}
	var structured json.RawMessage
	if len(tools) == 0 {
		structured = json.RawMessage(`{"type":"json_object"}`)
	}
	req, err := (modelruntime.OpenAIChatCodec{}).Encode(modelruntime.Request{
		Profile:          profile,
		Messages:         toModelMessages(input, m.allowsImageInput()),
		Tools:            tools,
		ToolChoice:       toModelToolChoice(cfg.ToolChoice),
		StructuredOutput: structured,
		Budgets: modelruntime.Budgets{
			MaxOutputTokens: valueOrZero(cfg.MaxTokens),
		},
	})
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeUnknown, "encode OpenAI-compatible request").WithCause(err)
	}
	body := req.Body
	if cfg.Temperature != nil {
		body["temperature"] = *cfg.Temperature
	}
	if len(cfg.Stop) > 0 {
		body["stop"] = cfg.Stop
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeUnknown, "encode OpenAI-compatible request").WithCause(err)
	}
	return payload, nil
}

// attempt performs one bounded request and reports both the caller-facing error
// and the runtime classification the retry decision needs.
func (m *OpenAICompatibleModel) attempt(ctx context.Context, payload []byte) (*schema.Message, modelruntime.Failure, error) {
	attemptCtx := ctx
	if timeout := m.attemptTimeout(); timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, strings.TrimRight(m.baseURL(), "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, modelruntime.Failure{}, errs.NewNetworkError(errs.SubtypeNetworkTransport, "build OpenAI-compatible request").WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: m.attemptTimeout()}
	}
	resp, err := client.Do(req)
	if err != nil {
		failure := modelruntime.ClassifyTransportError(err)
		if ctx.Err() != nil {
			// The caller, not this attempt, ended the call.
			failure.Retryable = false
		}
		return nil, failure, errs.NewNetworkError(errs.SubtypeNetworkTransport, "call OpenAI-compatible model").WithCause(err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure := modelruntime.ClassifyHTTPFailure(resp.StatusCode, string(respBody), resp.Header.Get("Retry-After"), time.Now())
		apiErr := errs.NewAPIError(errs.SubtypeServerError, "OpenAI-compatible model returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))).WithCode(resp.StatusCode)
		if failure.RetryAfter > 0 {
			return nil, failure, &retryAfterError{err: apiErr, delay: failure.RetryAfter}
		}
		return nil, failure, apiErr
	}
	turn, err := (modelruntime.OpenAIChatCodec{}).Decode(respBody)
	if err != nil {
		if strings.Contains(err.Error(), "no choices") {
			return nil, modelruntime.ClassifyEmptyProviderOutput(err.Error()),
				errs.NewInternalError(errs.SubtypeInvalidResponse, "OpenAI-compatible response contained no choices")
		}
		return nil, modelruntime.Failure{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode OpenAI-compatible response").WithCause(err)
	}
	msg := &schema.Message{
		Role:      schema.Assistant,
		Content:   turn.Text,
		ToolCalls: schemaToolCalls(turn.ToolCalls),
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: string(turn.FinishReason),
			Usage: &schema.TokenUsage{
				PromptTokens:     turn.Usage.PromptTokens,
				CompletionTokens: turn.Usage.CompletionTokens,
				TotalTokens:      turn.Usage.TotalTokens,
			},
		},
		Extra: map[string]any{"request_id": turn.RequestID},
	}
	return msg, modelruntime.Failure{}, nil
}

func (m *OpenAICompatibleModel) attemptTimeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	if m.Profile.Timeout > 0 {
		return m.Profile.Timeout
	}
	return modelruntime.DefaultTimeout
}

func (m *OpenAICompatibleModel) maxAttempts() int {
	if m.MaxAttempts > 0 {
		return m.MaxAttempts
	}
	return modelruntime.DefaultMaxAttempts
}

func (m *OpenAICompatibleModel) backoffFor(attempt int) time.Duration {
	base := m.RetryBackoff
	if base <= 0 {
		base = modelruntime.DefaultRetryBackoff
	}
	wait := base
	for i := 1; i < attempt; i++ {
		wait *= 2
	}
	return wait
}

// waitBeforeRetry honors a provider `Retry-After` when it asks for a longer
// pause than the local backoff, and gives up the moment the caller does.
func waitBeforeRetry(ctx context.Context, retryAfter, backoff time.Duration) error {
	wait := backoff
	if retryAfter > wait {
		wait = retryAfter
	}
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Stream is intentionally not exposed in V1. The daemon uses bounded
// non-streaming runs so policy checks can inspect complete drafts.
func (m *OpenAICompatibleModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errs.NewValidationError(errs.SubtypeFailedPrecondition, "streaming model calls are not enabled for lark-agent V1")
}

func (m *OpenAICompatibleModel) baseURL() string {
	if m.BaseURL != "" {
		return m.BaseURL
	}
	return "https://api.openai.com/v1"
}

func (m *OpenAICompatibleModel) hasProfile() bool {
	return m.Profile.Name != "" ||
		m.Profile.Provider != "" ||
		m.Profile.Protocol != "" ||
		m.Profile.Model != "" ||
		m.Profile.BaseURL != ""
}

func (m *OpenAICompatibleModel) allowsToolUse() bool {
	return !m.hasProfile() || m.Profile.Capabilities.ToolUse
}

func (m *OpenAICompatibleModel) allowsImageInput() bool {
	return !m.hasProfile() || m.Profile.Capabilities.ImageInput
}

func (m *OpenAICompatibleModel) runtimeProfile(modelName string) modelruntime.Profile {
	profile := m.Profile
	if !m.hasProfile() {
		profile = modelruntime.Profile{
			Protocol: modelruntime.ProtocolOpenAIChat,
			Model:    modelName,
			Capabilities: modelruntime.Capabilities{
				ToolUse:          true,
				ParallelToolCall: true,
				ImageInput:       true,
			},
		}
	}
	if profile.Model == "" {
		profile.Model = modelName
	} else {
		profile.Model = modelName
	}
	return profile
}

func toModelMessages(input []*schema.Message, allowImages bool) []modelruntime.Message {
	out := make([]modelruntime.Message, 0, len(input))
	for _, msg := range input {
		if msg == nil {
			continue
		}
		message := modelruntime.Message{
			Role:       modelruntime.Role(msg.Role),
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
		}
		if msg.ToolName != "" {
			message.Name = msg.ToolName
		}
		if msg.Content != "" {
			message.Blocks = append(message.Blocks, modelruntime.Block{
				Type: modelruntime.BlockText,
				Text: msg.Content,
			})
		}
		if len(msg.UserInputMultiContent) > 0 {
			for _, part := range msg.UserInputMultiContent {
				switch part.Type {
				case schema.ChatMessagePartTypeText:
					message.Blocks = append(message.Blocks, modelruntime.Block{
						Type: modelruntime.BlockText,
						Text: part.Text,
					})
				case schema.ChatMessagePartTypeImageURL:
					if !allowImages {
						continue
					}
					if part.Image == nil || part.Image.URL == nil {
						continue
					}
					message.Blocks = append(message.Blocks, modelruntime.Block{
						Type:        modelruntime.BlockImage,
						ImageURL:    *part.Image.URL,
						ImageDetail: string(part.Image.Detail),
					})
				}
			}
		}
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				message.Blocks = append(message.Blocks, modelruntime.Block{
					Type: modelruntime.BlockToolCall,
					ToolCall: &modelruntime.ToolCall{
						ID:        call.ID,
						Name:      call.Function.Name,
						Arguments: json.RawMessage(call.Function.Arguments),
					},
				})
			}
		}
		if msg.Role == schema.Tool && msg.Content != "" {
			message.Blocks = []modelruntime.Block{{
				Type: modelruntime.BlockToolResult,
				ToolResult: &modelruntime.ToolResult{
					ID:      msg.ToolCallID,
					Name:    msg.ToolName,
					Content: msg.Content,
				},
			}}
		}
		out = append(out, message)
	}
	return out
}

func toModelTools(tools []*schema.ToolInfo) ([]modelruntime.Tool, error) {
	out := make([]modelruntime.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "model tool name is required")
		}
		parameters := json.RawMessage(`{"type":"object","properties":{}}`)
		if tool.ParamsOneOf != nil {
			jsonSchema, err := tool.ToJSONSchema()
			if err != nil {
				return nil, errs.NewInternalError(errs.SubtypeUnknown, "encode model tool schema: %s", tool.Name).WithCause(err)
			}
			data, err := json.Marshal(jsonSchema)
			if err != nil {
				return nil, errs.NewInternalError(errs.SubtypeUnknown, "encode model tool schema: %s", tool.Name).WithCause(err)
			}
			parameters = data
		}
		out = append(out, modelruntime.Tool{
			Name:        tool.Name,
			Description: tool.Desc,
			Schema:      parameters,
		})
	}
	return out, nil
}

func toModelToolChoice(choice *schema.ToolChoice) modelruntime.ToolChoiceIntent {
	if choice == nil {
		return modelruntime.ToolChoiceAuto
	}
	switch *choice {
	case schema.ToolChoiceForbidden:
		return modelruntime.ToolChoiceNone
	case schema.ToolChoiceForced:
		return modelruntime.ToolChoiceRequired
	default:
		return modelruntime.ToolChoiceAuto
	}
}

func schemaToolCalls(calls []modelruntime.ToolCall) []schema.ToolCall {
	out := make([]schema.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, schema.ToolCall{
			ID:   call.ID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      call.Name,
				Arguments: string(call.Arguments),
			},
		})
	}
	return out
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

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
			return nil, err
		}
		if waitErr := waitBeforeRetry(ctx, failure.RetryAfter, m.backoffFor(attempt)); waitErr != nil {
			// The caller went away while waiting. Report why the call failed,
			// not that the wait was cut short.
			return nil, err
		}
	}
}

func (m *OpenAICompatibleModel) encodeRequest(input []*schema.Message, opts ...einomodel.Option) ([]byte, error) {
	cfg := einomodel.GetCommonOptions(&einomodel.Options{Tools: append([]*schema.ToolInfo(nil), m.Tools...)}, opts...)
	modelName := m.Model
	if cfg.Model != nil && *cfg.Model != "" {
		modelName = *cfg.Model
	}
	body := map[string]any{
		"model":    modelName,
		"messages": toOpenAIMessages(input),
	}
	if len(cfg.Tools) > 0 {
		tools, err := toOpenAITools(cfg.Tools)
		if err != nil {
			return nil, err
		}
		body["tools"] = tools
		if choice, ok := openAIToolChoice(cfg.ToolChoice); ok {
			body["tool_choice"] = choice
		}
	} else {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if cfg.Temperature != nil {
		body["temperature"] = *cfg.Temperature
	}
	switch {
	case cfg.MaxTokens != nil:
		body["max_tokens"] = *cfg.MaxTokens
	case m.Profile.Capabilities.MaxOutputTokens > 0:
		body["max_tokens"] = m.Profile.Capabilities.MaxOutputTokens
	}
	if len(cfg.Stop) > 0 {
		body["stop"] = cfg.Stop
	}
	if thinking, ok := modelruntime.ThinkingPayload(m.Profile); ok {
		body["thinking"] = thinking
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
	var decoded struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Role      string            `json:"role"`
				Content   string            `json:"content"`
				ToolCalls []schema.ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, modelruntime.Failure{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode OpenAI-compatible response").WithCause(err)
	}
	if len(decoded.Choices) == 0 {
		return nil, modelruntime.ClassifyEmptyProviderOutput("response contained no choices"),
			errs.NewInternalError(errs.SubtypeInvalidResponse, "OpenAI-compatible response contained no choices")
	}
	choice := decoded.Choices[0]
	msg := &schema.Message{
		Role:      schema.Assistant,
		Content:   choice.Message.Content,
		ToolCalls: choice.Message.ToolCalls,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: choice.FinishReason,
			Usage: &schema.TokenUsage{
				PromptTokens:     decoded.Usage.PromptTokens,
				CompletionTokens: decoded.Usage.CompletionTokens,
				TotalTokens:      decoded.Usage.TotalTokens,
			},
		},
		Extra: map[string]any{"request_id": decoded.ID},
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

func toOpenAIMessages(input []*schema.Message) []map[string]any {
	out := make([]map[string]any, 0, len(input))
	for _, msg := range input {
		if msg == nil {
			continue
		}
		content := any(msg.Content)
		if len(msg.UserInputMultiContent) > 0 {
			parts := make([]map[string]any, 0, len(msg.UserInputMultiContent))
			for _, part := range msg.UserInputMultiContent {
				switch part.Type {
				case schema.ChatMessagePartTypeText:
					parts = append(parts, map[string]any{
						"type": "text",
						"text": part.Text,
					})
				case schema.ChatMessagePartTypeImageURL:
					if part.Image == nil || part.Image.URL == nil {
						continue
					}
					parts = append(parts, map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url":    *part.Image.URL,
							"detail": string(part.Image.Detail),
						},
					})
				}
			}
			content = parts
		}
		wire := map[string]any{
			"role":    string(msg.Role),
			"content": content,
		}
		if msg.Name != "" {
			wire["name"] = msg.Name
		}
		if len(msg.ToolCalls) > 0 {
			wire["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			wire["tool_call_id"] = msg.ToolCallID
		}
		if msg.ToolName != "" {
			wire["name"] = msg.ToolName
		}
		out = append(out, wire)
	}
	return out
}

func toOpenAITools(tools []*schema.ToolInfo) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "model tool name is required")
		}
		parameters := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if tool.ParamsOneOf != nil {
			jsonSchema, err := tool.ToJSONSchema()
			if err != nil {
				return nil, errs.NewInternalError(errs.SubtypeUnknown, "encode model tool schema: %s", tool.Name).WithCause(err)
			}
			parameters = jsonSchema
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Desc,
				"parameters":  parameters,
			},
		})
	}
	return out, nil
}

func openAIToolChoice(choice *schema.ToolChoice) (string, bool) {
	if choice == nil {
		return "", false
	}
	switch *choice {
	case schema.ToolChoiceForbidden:
		return "none", true
	case schema.ToolChoiceForced:
		return "required", true
	default:
		return "", false
	}
}

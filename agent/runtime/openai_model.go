package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/internal/apperr"
)

// OpenAICompatibleModel adapts an OpenAI Chat Completions compatible endpoint
// to Eino's BaseModel interface.
type OpenAICompatibleModel struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
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

// Generate calls /chat/completions and returns the first assistant message.
func (m *OpenAICompatibleModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
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
		body["tool_choice"] = openAIToolChoice(cfg.ToolChoice)
	} else {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if cfg.Temperature != nil {
		body["temperature"] = *cfg.Temperature
	}
	if cfg.MaxTokens != nil {
		body["max_tokens"] = *cfg.MaxTokens
	}
	if len(cfg.Stop) > 0 {
		body["stop"] = cfg.Stop
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeUnknown, "encode OpenAI-compatible request").WithCause(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.baseURL(), "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkTransport, "build OpenAI-compatible request").WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: m.Timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkTransport, "call OpenAI-compatible model").WithCause(err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := errs.NewAPIError(errs.SubtypeServerError, "OpenAI-compatible model returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))).WithCode(resp.StatusCode)
		if delay := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); delay > 0 {
			return nil, &retryAfterError{err: apiErr, delay: delay}
		}
		return nil, apiErr
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
		return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode OpenAI-compatible response").WithCause(err)
	}
	if len(decoded.Choices) == 0 {
		return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "OpenAI-compatible response contained no choices")
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
	return msg, nil
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
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

func openAIToolChoice(choice *schema.ToolChoice) string {
	if choice == nil {
		return "auto"
	}
	switch *choice {
	case schema.ToolChoiceForbidden:
		return "none"
	case schema.ToolChoiceForced:
		return "required"
	default:
		return "auto"
	}
}

package model

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type OpenAIChatCodec struct{}

func (OpenAIChatCodec) Encode(req Request) (HTTPRequest, error) {
	body := map[string]any{
		"model":    req.Profile.Model,
		"messages": encodeOpenAIChatMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		tools, err := encodeOpenAIChatTools(req.Tools)
		if err != nil {
			return HTTPRequest{}, err
		}
		body["tools"] = tools
		if choice, ok := openAIChatToolChoice(req); ok {
			body["tool_choice"] = choice
		}
	}
	if req.CacheKey != "" {
		body["prompt_cache_key"] = req.CacheKey
	}
	if thinking, ok := kimiThinking(req.Profile); ok {
		body["thinking"] = thinking
	}
	return HTTPRequest{
		Method: "POST",
		Path:   "/chat/completions",
		Headers: map[string]string{
			"content-type": "application/json",
		},
		Body: body,
	}, nil
}

func (OpenAIChatCodec) Decode(raw []byte) (Turn, error) {
	var decoded struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage openAIUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Turn{}, err
	}
	if len(decoded.Choices) == 0 {
		return Turn{}, fmt.Errorf("openai chat response contained no choices")
	}
	choice := decoded.Choices[0]
	turn := Turn{
		Text:         choice.Message.Content,
		RequestID:    decoded.ID,
		FinishReason: normalizeOpenAIFinish(choice.FinishReason, len(choice.Message.ToolCalls) > 0),
		Usage:        usageFromOpenAI(decoded.Usage),
		ToolCalls:    make([]ToolCall, 0, len(choice.Message.ToolCalls)),
	}
	if choice.Message.ReasoningContent != "" {
		turn.ThinkingBlocks = append(turn.ThinkingBlocks, ThinkingBlock{
			Type:    string(BlockThinking),
			Content: choice.Message.ReasoningContent,
		})
	}
	for _, call := range choice.Message.ToolCalls {
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}
	return turn, nil
}

func (OpenAIChatCodec) DecodeSSE(r io.Reader) (Turn, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	builders := map[int]*streamToolCallBuilder{}
	var indexes []int
	var turn Turn
	var content strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var event struct {
			ID      string `json:"id"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage openAIUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Turn{}, err
		}
		if event.ID != "" {
			turn.RequestID = event.ID
		}
		if event.Usage.TotalTokens != 0 || event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			turn.Usage = usageFromOpenAI(event.Usage)
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
			}
			for _, deltaCall := range choice.Delta.ToolCalls {
				builder := builders[deltaCall.Index]
				if builder == nil {
					builder = &streamToolCallBuilder{}
					builders[deltaCall.Index] = builder
					indexes = append(indexes, deltaCall.Index)
				}
				if deltaCall.ID != "" {
					builder.id = deltaCall.ID
				}
				if deltaCall.Function.Name != "" {
					builder.name = deltaCall.Function.Name
				}
				builder.arguments.WriteString(deltaCall.Function.Arguments)
			}
			if choice.FinishReason != nil {
				turn.FinishReason = normalizeOpenAIFinish(*choice.FinishReason, len(builders) > 0)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Turn{}, err
	}
	sort.Ints(indexes)
	turn.Text = content.String()
	for _, index := range indexes {
		builder := builders[index]
		args := builder.arguments.String()
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return Turn{}, fmt.Errorf("tool call %s arguments are not complete JSON", builder.id)
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{
			ID:        builder.id,
			Name:      builder.name,
			Arguments: json.RawMessage(args),
		})
	}
	if turn.FinishReason == "" {
		turn.FinishReason = normalizeOpenAIFinish("", len(turn.ToolCalls) > 0)
	}
	return turn, nil
}

type streamToolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
	TotalTokens              int `json:"total_tokens"`
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	ReasoningTokens          int `json:"reasoning_tokens"`
	PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens    int `json:"prompt_cache_miss_tokens"`
	InputCachedTokens        int `json:"input_cached_tokens"`
	OutputReasoningTokens    int `json:"output_reasoning_tokens"`
	CachedTokens             int `json:"cached_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func encodeOpenAIChatMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		wire := map[string]any{
			"role": string(message.Role),
		}
		if message.Name != "" {
			wire["name"] = message.Name
		}
		if message.ToolCallID != "" {
			wire["tool_call_id"] = message.ToolCallID
		}
		if len(message.Blocks) == 1 && message.Blocks[0].Type == BlockText {
			wire["content"] = message.Blocks[0].Text
		} else {
			wire["content"] = encodeOpenAIContentParts(message.Blocks)
		}
		out = append(out, wire)
	}
	return out
}

func encodeOpenAIContentParts(blocks []Block) []map[string]any {
	parts := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			parts = append(parts, map[string]any{"type": "text", "text": block.Text})
		case BlockImage:
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": block.ImageURL},
			})
		}
	}
	return parts
}

func encodeOpenAIChatTools(tools []Tool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		parameters := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if len(tool.Schema) > 0 {
			var decoded any
			if err := json.Unmarshal(tool.Schema, &decoded); err != nil {
				return nil, fmt.Errorf("decode schema for tool %s: %w", tool.Name, err)
			}
			parameters = decoded
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return out, nil
}

func openAIChatToolChoice(req Request) (string, bool) {
	if req.ToolChoice == "" || req.ToolChoice == ToolChoiceAuto {
		return "auto", false
	}
	if req.ToolChoice == ToolChoiceRequired &&
		req.Profile.Provider == ProviderKimi &&
		req.Profile.Capabilities.Thinking &&
		req.Profile.Reasoning.Mode != ReasoningDisabled {
		return "", false
	}
	switch req.ToolChoice {
	case ToolChoiceRequired:
		return "required", true
	case ToolChoiceNone:
		return "none", true
	default:
		return "auto", false
	}
}

func kimiThinking(profile Profile) (map[string]any, bool) {
	if profile.Provider != ProviderKimi || !profile.Capabilities.Thinking {
		return nil, false
	}
	mode := profile.Reasoning.Mode
	if mode == "" || mode == ReasoningProviderDefault || mode == ReasoningEnabled {
		out := map[string]any{"type": "enabled"}
		if profile.Reasoning.Effort != "" {
			out["effort"] = profile.Reasoning.Effort
		}
		return out, true
	}
	if mode == ReasoningDisabled {
		return map[string]any{"type": "disabled"}, true
	}
	return nil, false
}

func normalizeOpenAIFinish(raw string, hasToolCalls bool) FinishReason {
	switch raw {
	case "tool_calls":
		return FinishToolCalls
	case "stop", "":
		if hasToolCalls {
			return FinishToolCalls
		}
		return FinishCompleted
	case "length":
		return FinishTruncated
	case "content_filter":
		return FinishFiltered
	default:
		return FinishOther
	}
}

func usageFromOpenAI(usage openAIUsage) Usage {
	prompt := usage.PromptTokens
	if prompt == 0 {
		prompt = usage.InputTokens
	}
	completion := usage.CompletionTokens
	if completion == 0 {
		completion = usage.OutputTokens
	}
	total := usage.TotalTokens
	if total == 0 {
		total = prompt + completion
	}
	thinking := usage.ReasoningTokens
	if thinking == 0 {
		thinking = usage.OutputReasoningTokens
	}
	cacheRead := usage.PromptCacheHitTokens
	if cacheRead == 0 {
		cacheRead = usage.InputCachedTokens
	}
	cacheWrite := usage.PromptCacheMissTokens
	if cacheWrite == 0 {
		cacheWrite = usage.CacheCreationInputTokens
	}
	return Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		ThinkingTokens:   thinking,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}
}

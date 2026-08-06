package model

import (
	"encoding/json"
	"fmt"
)

type AnthropicMessagesCodec struct{}

func (AnthropicMessagesCodec) Encode(req Request) (HTTPRequest, error) {
	body := map[string]any{
		"model":    req.Profile.Model,
		"messages": encodeAnthropicMessages(req.Messages),
	}
	if len(req.Tools) > 0 {
		tools, err := encodeAnthropicTools(req.Tools)
		if err != nil {
			return HTTPRequest{}, err
		}
		body["tools"] = tools
	}
	if req.Profile.Reasoning.Mode == ReasoningEnabled {
		body["thinking"] = map[string]any{"type": "enabled"}
	}
	return HTTPRequest{
		Method: "POST",
		Path:   "/v1/messages",
		Headers: map[string]string{
			"content-type":      "application/json",
			"anthropic-version": "2023-06-01",
		},
		Body: body,
	}, nil
}

func (AnthropicMessagesCodec) Decode(raw []byte) (Turn, error) {
	var decoded struct {
		ID         string            `json:"id"`
		Content    []json.RawMessage `json:"content"`
		StopReason string            `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Turn{}, err
	}
	turn := Turn{
		RequestID: decoded.ID,
		Usage: Usage{
			PromptTokens:     decoded.Usage.InputTokens,
			CompletionTokens: decoded.Usage.OutputTokens,
			TotalTokens:      decoded.Usage.InputTokens + decoded.Usage.OutputTokens,
		},
		FinishReason: normalizeAnthropicStop(decoded.StopReason),
	}
	for _, item := range decoded.Content {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return Turn{}, err
		}
		switch header.Type {
		case "text":
			var text struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(item, &text); err != nil {
				return Turn{}, err
			}
			turn.Text += text.Text
		case "thinking":
			var thinking struct {
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
			}
			if err := json.Unmarshal(item, &thinking); err != nil {
				return Turn{}, err
			}
			turn.ThinkingBlocks = append(turn.ThinkingBlocks, ThinkingBlock{
				Type:      "anthropic_thinking",
				Content:   thinking.Thinking,
				Signature: thinking.Signature,
			})
		case "tool_use":
			var call struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(item, &call); err != nil {
				return Turn{}, err
			}
			if len(call.Input) == 0 {
				call.Input = json.RawMessage(`{}`)
			}
			if !json.Valid(call.Input) {
				return Turn{}, fmt.Errorf("anthropic tool_use %s input is not valid JSON", call.ID)
			}
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: call.Input,
			})
		}
	}
	if len(turn.ToolCalls) > 0 {
		turn.FinishReason = FinishToolCalls
	}
	return turn, nil
}

func encodeAnthropicMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == RoleSystem {
			continue
		}
		out = append(out, map[string]any{
			"role":    string(message.Role),
			"content": encodeAnthropicContent(message.Blocks),
		})
	}
	return out
}

func encodeAnthropicContent(blocks []Block) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			out = append(out, map[string]any{"type": "text", "text": block.Text})
		case BlockImage:
			out = append(out, map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "url", "url": block.ImageURL},
			})
		case BlockToolResult:
			if block.ToolResult != nil {
				out = append(out, map[string]any{
					"type":        "tool_result",
					"tool_use_id": block.ToolResult.ID,
					"content":     block.ToolResult.Content,
				})
			}
		case BlockThinking:
			if block.Thinking != nil {
				out = append(out, map[string]any{
					"type":      "thinking",
					"thinking":  block.Thinking.Content,
					"signature": block.Thinking.Signature,
				})
			}
		}
	}
	return out
}

func encodeAnthropicTools(tools []Tool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		var schema any = map[string]any{"type": "object", "properties": map[string]any{}}
		if len(tool.Schema) > 0 {
			if err := json.Unmarshal(tool.Schema, &schema); err != nil {
				return nil, fmt.Errorf("decode schema for tool %s: %w", tool.Name, err)
			}
		}
		out = append(out, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": schema,
		})
	}
	return out, nil
}

func normalizeAnthropicStop(raw string) FinishReason {
	switch raw {
	case "tool_use":
		return FinishToolCalls
	case "end_turn", "":
		return FinishCompleted
	case "max_tokens":
		return FinishTruncated
	case "stop_sequence":
		return FinishCompleted
	default:
		return FinishOther
	}
}

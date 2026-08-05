package model

import (
	"encoding/json"
	"fmt"
)

type OpenAIResponsesCodec struct{}

func (OpenAIResponsesCodec) Encode(req Request) (HTTPRequest, error) {
	body := map[string]any{
		"model": req.Profile.Model,
		"input": encodeResponsesInput(req.Messages),
		"store": false,
	}
	if len(req.Tools) > 0 {
		tools, err := encodeOpenAIChatTools(req.Tools)
		if err != nil {
			return HTTPRequest{}, err
		}
		body["tools"] = tools
	}
	if req.CacheKey != "" {
		body["prompt_cache_key"] = req.CacheKey
	}
	return HTTPRequest{
		Method: "POST",
		Path:   "/responses",
		Headers: map[string]string{
			"content-type": "application/json",
		},
		Body: body,
	}, nil
}

func (OpenAIResponsesCodec) Decode(raw []byte) (Turn, error) {
	var decoded struct {
		ID     string            `json:"id"`
		Output []json.RawMessage `json:"output"`
		Usage  openAIUsage       `json:"usage"`
		Status string            `json:"status"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Turn{}, err
	}
	turn := Turn{
		RequestID:      decoded.ID,
		Usage:          usageFromOpenAI(decoded.Usage),
		ProviderStatus: map[string]string{"status": decoded.Status},
	}
	for _, item := range decoded.Output {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return Turn{}, err
		}
		switch header.Type {
		case "reasoning":
			var reasoning struct {
				ID               string `json:"id"`
				EncryptedContent string `json:"encrypted_content"`
			}
			if err := json.Unmarshal(item, &reasoning); err != nil {
				return Turn{}, err
			}
			turn.ThinkingBlocks = append(turn.ThinkingBlocks, ThinkingBlock{
				ID:      reasoning.ID,
				Type:    "responses_reasoning",
				Content: reasoning.EncryptedContent,
			})
		case "message":
			var message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(item, &message); err != nil {
				return Turn{}, err
			}
			for _, content := range message.Content {
				if content.Type == "output_text" {
					turn.Text += content.Text
				}
			}
		case "function_call":
			var call struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(item, &call); err != nil {
				return Turn{}, err
			}
			if !json.Valid([]byte(call.Arguments)) {
				return Turn{}, fmt.Errorf("responses function_call %s arguments are not valid JSON", call.CallID)
			}
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{
				ID:        call.CallID,
				Name:      call.Name,
				Arguments: json.RawMessage(call.Arguments),
			})
		}
	}
	if len(turn.ToolCalls) > 0 {
		turn.FinishReason = FinishToolCalls
	} else {
		turn.FinishReason = FinishCompleted
	}
	return turn, nil
}

func encodeResponsesInput(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, map[string]any{
			"role":    string(message.Role),
			"content": encodeResponsesContent(message.Blocks),
		})
	}
	return out
}

func encodeResponsesContent(blocks []Block) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			out = append(out, map[string]any{"type": "input_text", "text": block.Text})
		case BlockImage:
			out = append(out, map[string]any{"type": "input_image", "image_url": block.ImageURL})
		}
	}
	return out
}

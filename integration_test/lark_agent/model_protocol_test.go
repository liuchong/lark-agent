package larkagent_test

import (
	"encoding/json"
	"testing"

	modelruntime "github.com/liuchong/lark-agent/agent/runtime/model"
)

func TestKimiThinkingToolRequestOmitsRequiredToolChoice(t *testing.T) {
	codec := modelruntime.OpenAIChatCodec{}
	req := modelruntime.Request{
		Profile: modelruntime.Profile{
			Name:     "primary",
			Provider: modelruntime.ProviderKimi,
			Protocol: modelruntime.ProtocolOpenAIChat,
			BaseURL:  "https://api.kimi.com/coding/v1",
			Model:    "k3-256k",
			Reasoning: modelruntime.ReasoningConfig{
				Mode: modelruntime.ReasoningProviderDefault,
			},
			Capabilities: modelruntime.Capabilities{
				ToolUse:  true,
				Thinking: true,
			},
		},
		Messages: []modelruntime.Message{{
			Role: modelruntime.RoleUser,
			Blocks: []modelruntime.Block{{
				Type: modelruntime.BlockText,
				Text: "检查工作区代码",
			}},
		}},
		Tools: []modelruntime.Tool{{
			Name:        "search_workspace",
			Description: "Search bounded workspace text.",
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"]
			}`),
		}},
		ToolChoice: modelruntime.ToolChoiceRequired,
		CacheKey:   "work-5994",
	}

	httpReq, err := codec.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := httpReq.Body["tools"]; !ok {
		t.Fatalf("tools missing: %#v", httpReq.Body)
	}
	if got, ok := httpReq.Body["tool_choice"]; ok {
		t.Fatalf("Kimi thinking tool request must omit tool_choice, got %#v", got)
	}
	if got := httpReq.Body["prompt_cache_key"]; got != "work-5994" {
		t.Fatalf("prompt_cache_key=%v", got)
	}
}

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOpenAIChatKimiThinkingOmitsForcedToolChoice(t *testing.T) {
	codec := OpenAIChatCodec{}
	req := Request{
		Profile: Profile{
			Name:     "primary",
			Provider: ProviderKimi,
			Protocol: ProtocolOpenAIChat,
			Model:    "k3-256k",
			Reasoning: ReasoningConfig{
				Mode: ReasoningProviderDefault,
			},
			Capabilities: Capabilities{
				ToolUse:  true,
				Thinking: true,
			},
		},
		Messages: []Message{{
			Role: RoleUser,
			Blocks: []Block{{
				Type: BlockText,
				Text: "检查这段代码",
			}},
		}},
		Tools: []Tool{{
			Name:        "search_workspace",
			Description: "Search bounded workspace text.",
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"]
			}`),
		}},
		ToolChoice: ToolChoiceRequired,
		CacheKey:   "work-5994",
	}

	httpReq, err := codec.Encode(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if httpReq.Path != "/chat/completions" {
		t.Fatalf("unexpected path: %s", httpReq.Path)
	}
	if got := httpReq.Body["model"]; got != "k3-256k" {
		t.Fatalf("model = %v", got)
	}
	if _, ok := httpReq.Body["tools"]; !ok {
		t.Fatalf("tools missing from encoded request")
	}
	if got, ok := httpReq.Body["tool_choice"]; ok {
		t.Fatalf("tool_choice must be omitted for Kimi thinking tools, got %#v", got)
	}
	thinking, ok := httpReq.Body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("Kimi thinking payload missing: %#v", httpReq.Body["thinking"])
	}
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("thinking.type = %v", got)
	}
	if got := httpReq.Body["prompt_cache_key"]; got != "work-5994" {
		t.Fatalf("prompt_cache_key = %v", got)
	}
}

func TestOpenAIChatEncodesToolHistory(t *testing.T) {
	codec := OpenAIChatCodec{}
	req := Request{
		Profile: Profile{Model: "k3-256k", Capabilities: Capabilities{ToolUse: true, ParallelToolCall: false}},
		Messages: []Message{
			{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "find router"}}},
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockToolCall, ToolCall: &ToolCall{
					ID:        "call_1",
					Name:      "search_workspace",
					Arguments: json.RawMessage(`{"query":"router"}`),
				}},
			}},
			{Role: RoleTool, Name: "search_workspace", ToolCallID: "call_1", Blocks: []Block{
				{Type: BlockToolResult, ToolResult: &ToolResult{ID: "call_1", Name: "search_workspace", Content: `{"matches":[]}`}},
			}},
		},
		Tools: []Tool{{Name: "search_workspace"}},
	}

	httpReq, err := codec.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	messages := httpReq.Body["messages"].([]map[string]any)
	assistant := messages[1]
	toolCalls, ok := assistant["tool_calls"].([]map[string]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("assistant tool calls=%+v", assistant["tool_calls"])
	}
	function := toolCalls[0]["function"].(map[string]any)
	if function["name"] != "search_workspace" || function["arguments"] != `{"query":"router"}` {
		t.Fatalf("function=%+v", function)
	}
	toolMessage := messages[2]
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_1" ||
		toolMessage["content"] != `{"matches":[]}` {
		t.Fatalf("tool message=%+v", toolMessage)
	}
	if got := httpReq.Body["parallel_tool_calls"]; got != false {
		t.Fatalf("parallel_tool_calls=%+v", got)
	}
}

func TestOpenAIChatSSEAssemblesParallelToolCalls(t *testing.T) {
	codec := OpenAIChatCodec{}
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"search_workspace","arguments":"{\"query\":\"owner"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"read_workspace","arguments":"{\"path\":\"agent/"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" reply\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"runtime/loop.go\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_1","choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	turn, err := codec.DecodeSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("decode sse: %v", err)
	}
	if turn.RequestID != "chatcmpl_1" {
		t.Fatalf("request id = %q", turn.RequestID)
	}
	if turn.FinishReason != FinishToolCalls {
		t.Fatalf("finish reason = %s", turn.FinishReason)
	}
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d", len(turn.ToolCalls))
	}
	if turn.ToolCalls[0].ID != "call_a" || turn.ToolCalls[0].Name != "search_workspace" {
		t.Fatalf("first call = %#v", turn.ToolCalls[0])
	}
	if string(turn.ToolCalls[0].Arguments) != `{"query":"owner reply"}` {
		t.Fatalf("first arguments = %s", turn.ToolCalls[0].Arguments)
	}
	if turn.ToolCalls[1].ID != "call_b" || turn.ToolCalls[1].Name != "read_workspace" {
		t.Fatalf("second call = %#v", turn.ToolCalls[1])
	}
	if string(turn.ToolCalls[1].Arguments) != `{"path":"agent/runtime/loop.go"}` {
		t.Fatalf("second arguments = %s", turn.ToolCalls[1].Arguments)
	}
	if turn.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", turn.Usage)
	}
}

func TestResponsesAndAnthropicDecodeToCanonicalTurns(t *testing.T) {
	responses := OpenAIResponsesCodec{}
	responsesTurn, err := responses.Decode([]byte(`{
		"id":"resp_1",
		"output":[
			{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},
			{"type":"message","content":[{"type":"output_text","text":"需要读取代码"}]},
			{"type":"function_call","call_id":"call_a","name":"search_workspace","arguments":"{\"query\":\"tool choice\"}"},
			{"type":"function_call","call_id":"call_b","name":"read_workspace","arguments":"{\"path\":\"agent/runtime/openai_model.go\"}"}
		],
		"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}
	}`))
	if err != nil {
		t.Fatalf("decode responses: %v", err)
	}
	if responsesTurn.RequestID != "resp_1" || responsesTurn.Text != "需要读取代码" {
		t.Fatalf("responses turn = %#v", responsesTurn)
	}
	if len(responsesTurn.ToolCalls) != 2 || responsesTurn.ToolCalls[0].ID != "call_a" || responsesTurn.ToolCalls[1].ID != "call_b" {
		t.Fatalf("responses tool calls = %#v", responsesTurn.ToolCalls)
	}
	if len(responsesTurn.ThinkingBlocks) != 1 {
		t.Fatalf("responses thinking blocks = %#v", responsesTurn.ThinkingBlocks)
	}

	anthropic := AnthropicMessagesCodec{}
	anthropicTurn, err := anthropic.Decode([]byte(`{
		"id":"msg_1",
		"content":[
			{"type":"thinking","thinking":"opaque","signature":"sig"},
			{"type":"text","text":"先搜索"},
			{"type":"tool_use","id":"toolu_1","name":"search_workspace","input":{"query":"provider runtime"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":13,"output_tokens":8}
	}`))
	if err != nil {
		t.Fatalf("decode anthropic: %v", err)
	}
	if anthropicTurn.RequestID != "msg_1" || anthropicTurn.Text != "先搜索" {
		t.Fatalf("anthropic turn = %#v", anthropicTurn)
	}
	if anthropicTurn.FinishReason != FinishToolCalls {
		t.Fatalf("anthropic finish = %s", anthropicTurn.FinishReason)
	}
	if len(anthropicTurn.ToolCalls) != 1 || anthropicTurn.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("anthropic tool calls = %#v", anthropicTurn.ToolCalls)
	}
	if len(anthropicTurn.ThinkingBlocks) != 1 {
		t.Fatalf("anthropic thinking blocks = %#v", anthropicTurn.ThinkingBlocks)
	}
}

func TestProviderFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		header    string
		category  FailureCategory
		retryable bool
		delay     time.Duration
	}{
		{
			name:      "bad request",
			status:    400,
			body:      `{"error":{"message":"tool_choice 'required' is incompatible with thinking enabled"}}`,
			category:  FailureInvalidRequest,
			retryable: false,
		},
		{
			name:      "rate limited with retry after",
			status:    429,
			body:      `{"error":{"message":"rate limited"}}`,
			header:    "2",
			category:  FailureRateLimit,
			retryable: true,
			delay:     2 * time.Second,
		},
		{
			name:      "quota exhausted",
			status:    429,
			body:      `{"error":{"message":"insufficient quota or account balance"}}`,
			category:  FailureQuotaExhausted,
			retryable: false,
		},
		{
			name:      "server overload",
			status:    529,
			body:      `overloaded`,
			category:  FailureOverloaded,
			retryable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := ClassifyHTTPFailure(tc.status, tc.body, tc.header, time.Unix(0, 0))
			if failure.Category != tc.category {
				t.Fatalf("category = %s", failure.Category)
			}
			if failure.Retryable != tc.retryable {
				t.Fatalf("retryable = %v", failure.Retryable)
			}
			if failure.RetryAfter != tc.delay {
				t.Fatalf("retry after = %s", failure.RetryAfter)
			}
			if strings.Contains(strings.ToLower(failure.Diagnostic), "bearer ") {
				t.Fatalf("diagnostic leaked credential: %q", failure.Diagnostic)
			}
		})
	}
}

func TestProviderFactoryResolvesRoleProfiles(t *testing.T) {
	factory := NewProviderFactory([]Profile{
		{
			Name:     "primary",
			Provider: ProviderKimi,
			Protocol: ProtocolOpenAIChat,
			BaseURL:  "https://api.kimi.com/coding/v1",
			Model:    "k3-256k",
		},
		{
			Name:     "finalizer",
			Provider: ProviderOpenAI,
			Protocol: ProtocolOpenAIResponses,
			BaseURL:  "https://api.openai.com/v1",
			Model:    "gpt-5.5",
		},
	}, RoleBindings{
		Agent:     "primary",
		Semantic:  "primary",
		Finalizer: "finalizer",
		Compactor: "primary",
		Vision:    "primary",
	})

	selected, codec, err := factory.Resolve(RoleFinalizer)
	if err != nil {
		t.Fatalf("resolve finalizer: %v", err)
	}
	if selected.Name != "finalizer" {
		t.Fatalf("profile = %#v", selected)
	}
	if _, ok := codec.(OpenAIResponsesCodec); !ok {
		t.Fatalf("codec = %T", codec)
	}

	_, _, err = factory.Resolve(Role("unknown"))
	if err == nil {
		t.Fatalf("unknown role should fail")
	}
}

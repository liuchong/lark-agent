package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestOpenAICompatibleModelGenerate(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "test-model" {
			t.Fatalf("body=%+v", body)
		}
		format, ok := body["response_format"].(map[string]any)
		if !ok || format["type"] != "json_object" {
			t.Fatalf("response_format=%+v body=%+v", body["response_format"], body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{APIKey: "key", BaseURL: server.URL, Model: "test-model", Client: server.Client()}
	msg, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" || msg.Content != "done" {
		t.Fatalf("path=%q msg=%+v", gotPath, msg)
	}
}

func TestOpenAICompatibleModelToolCallingWireFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["response_format"]; ok {
			t.Fatalf("tool request must not include response_format: %+v", body)
		}
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("tools=%+v", body["tools"])
		}
		if body["tool_choice"] != "required" {
			t.Fatalf("tool_choice=%+v", body["tool_choice"])
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 3 {
			t.Fatalf("messages=%+v", body["messages"])
		}
		assistant := messages[1].(map[string]any)
		if _, ok := assistant["tool_calls"]; !ok {
			t.Fatalf("assistant=%+v", assistant)
		}
		toolMessage := messages[2].(map[string]any)
		if toolMessage["tool_call_id"] != "call_1" || toolMessage["name"] != "search_workspace" {
			t.Fatalf("tool message=%+v", toolMessage)
		}
		_, _ = w.Write([]byte(`{
			"id":"req_1",
			"choices":[{
				"finish_reason":"tool_calls",
				"message":{
					"role":"assistant",
					"content":"",
					"tool_calls":[{
						"id":"call_2",
						"type":"function",
						"function":{"name":"read_workspace","arguments":"{\"path\":\"router.go\"}"}
					}]
				}
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`))
	}))
	t.Cleanup(server.Close)

	params := schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"query": {Type: schema.String, Required: true},
	})
	tool := &schema.ToolInfo{Name: "search_workspace", Desc: "search code", ParamsOneOf: params}
	model := &OpenAICompatibleModel{APIKey: "key", BaseURL: server.URL, Model: "test-model", Client: server.Client()}
	msg, err := model.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("find router"),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "search_workspace",
				Arguments: `{"query":"router"}`,
			},
		}}),
		schema.ToolMessage(`{"matches":[]}`, "call_1", schema.WithToolName("search_workspace")),
	}, einomodel.WithTools([]*schema.ToolInfo{tool}), einomodel.WithToolChoice(schema.ToolChoiceForced))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read_workspace" {
		t.Fatalf("msg=%+v", msg)
	}
	if msg.ResponseMeta == nil || msg.ResponseMeta.FinishReason != "tool_calls" ||
		msg.ResponseMeta.Usage == nil || msg.ResponseMeta.Usage.TotalTokens != 13 {
		t.Fatalf("response meta=%+v", msg.ResponseMeta)
	}
	if msg.Extra["request_id"] != "req_1" {
		t.Fatalf("extra=%+v", msg.Extra)
	}
}

func TestOpenAICompatibleModelWithToolsReturnsIndependentModel(t *testing.T) {
	base := &OpenAICompatibleModel{APIKey: "key", Model: "test-model"}
	tool := &schema.ToolInfo{Name: "search_workspace"}
	bound, err := base.WithTools([]*schema.ToolInfo{tool})
	if err != nil {
		t.Fatal(err)
	}
	if bound == base {
		t.Fatal("WithTools mutated the shared model")
	}
	if len(base.Tools) != 0 {
		t.Fatalf("base tools=%+v", base.Tools)
	}
}

func TestOpenAICompatibleModelExposesProviderRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	t.Cleanup(server.Close)
	model := &OpenAICompatibleModel{APIKey: "key", BaseURL: server.URL, Model: "test", Client: server.Client()}
	_, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("test")})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	retryable, ok := err.(interface{ RetryAfter() time.Duration })
	if !ok || retryable.RetryAfter() != 90*time.Second {
		t.Fatalf("err=%T %v", err, err)
	}
}

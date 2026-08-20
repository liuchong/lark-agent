package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	modelruntime "github.com/liuchong/lark-agent/agent/runtime/model"
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

func TestOpenAICompatibleModelSerializesUserMultimodalContentParts(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	imageURL := "https://example.test/evidence.png"
	model := &OpenAICompatibleModel{
		APIKey:  "key",
		BaseURL: server.URL,
		Model:   "vision-model",
		Client:  server.Client(),
	}
	_, err := model.Generate(context.Background(), []*schema.Message{{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "Inspect this evidence."},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: &imageURL},
					Detail:            schema.ImageURLDetailHigh,
				},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages=%+v", body["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("message=%+v", messages[0])
	}
	parts, ok := message["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("multimodal content=%+v, want text and image_url parts", message["content"])
	}
	textPart, ok := parts[0].(map[string]any)
	if !ok || textPart["type"] != "text" || textPart["text"] != "Inspect this evidence." {
		t.Fatalf("text part=%+v", parts[0])
	}
	imagePart, ok := parts[1].(map[string]any)
	if !ok || imagePart["type"] != "image_url" {
		t.Fatalf("image part=%+v", parts[1])
	}
	imageContent, ok := imagePart["image_url"].(map[string]any)
	if !ok || imageContent["url"] != imageURL || imageContent["detail"] != "high" {
		t.Fatalf("image_url content=%+v", imagePart["image_url"])
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

func TestOpenAICompatibleModelAllowedToolChoiceOmitsWireField(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	tool := &schema.ToolInfo{Name: "search_workspace"}
	model := &OpenAICompatibleModel{APIKey: "key", BaseURL: server.URL, Model: "test-model", Client: server.Client()}
	_, err := model.Generate(
		context.Background(),
		[]*schema.Message{schema.UserMessage("find router")},
		einomodel.WithTools([]*schema.ToolInfo{tool}),
		einomodel.WithToolChoice(schema.ToolChoiceAllowed),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tools"]; !ok {
		t.Fatalf("tools missing: %+v", body)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("allowed tool choice must omit wire tool_choice: %+v", body)
	}
}

func TestOpenAICompatibleModelHonorsToolUseCapability(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test-model", Client: server.Client(),
		Profile: modelruntime.Profile{
			Name:         "primary",
			Provider:     modelruntime.ProviderKimi,
			Protocol:     modelruntime.ProtocolOpenAIChat,
			Capabilities: modelruntime.Capabilities{ToolUse: false},
		},
	}
	_, err := model.Generate(
		context.Background(),
		[]*schema.Message{schema.UserMessage("find router")},
		einomodel.WithTools([]*schema.ToolInfo{{Name: "search_workspace"}}),
		einomodel.WithToolChoice(schema.ToolChoiceForced),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tools"]; ok {
		t.Fatalf("tool_use=false must omit tools: %+v", body)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf("tool_use=false must omit tool_choice: %+v", body)
	}
	if format, ok := body["response_format"].(map[string]any); !ok || format["type"] != "json_object" {
		t.Fatalf("response_format=%+v body=%+v", body["response_format"], body)
	}
}

func TestOpenAICompatibleModelHonorsParallelToolCapability(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test-model", Client: server.Client(),
		Profile: modelruntime.Profile{
			Name: "primary", Provider: modelruntime.ProviderKimi, Protocol: modelruntime.ProtocolOpenAIChat,
			Capabilities: modelruntime.Capabilities{ToolUse: true, ParallelToolCall: false},
		},
	}
	_, err := model.Generate(
		context.Background(),
		[]*schema.Message{schema.UserMessage("find router")},
		einomodel.WithTools([]*schema.ToolInfo{{Name: "search_workspace"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := body["parallel_tool_calls"].(bool); !ok || got {
		t.Fatalf("parallel_tool_calls=%+v body=%+v", body["parallel_tool_calls"], body)
	}
}

func TestOpenAICompatibleModelHonorsImageInputCapability(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)

	imageURL := "data:image/png;base64,AAAA"
	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test-model", Client: server.Client(),
		Profile: modelruntime.Profile{
			Name:         "primary",
			Provider:     modelruntime.ProviderKimi,
			Protocol:     modelruntime.ProtocolOpenAIChat,
			Capabilities: modelruntime.Capabilities{ImageInput: false},
		},
	}
	_, err := model.Generate(context.Background(), []*schema.Message{{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "Inspect this evidence."},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &imageURL},
				Detail:            schema.ImageURLDetailHigh,
			}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	message := messages[0].(map[string]any)
	if message["content"] != "Inspect this evidence." {
		t.Fatalf("image_input=false must strip image parts: %+v", message["content"])
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
	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test",
		MaxAttempts: 1, Client: server.Client(),
	}
	_, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("test")})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	retryable, ok := err.(interface{ RetryAfter() time.Duration })
	if !ok || retryable.RetryAfter() != 90*time.Second {
		t.Fatalf("err=%T %v", err, err)
	}
}

// TestOpenAICompatibleModelRetriesTransportFailure covers the spec scene where a
// call fails before any response arrives: a dropped connection or an elapsed
// per-attempt timeout is retried, and the caller sees one successful call.
func TestOpenAICompatibleModelRetriesTransportFailure(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			hijacked, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = hijacked.Close()
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second"}}]}`))
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test",
		MaxAttempts: 3, RetryBackoff: time.Millisecond, Client: server.Client(),
	}
	msg, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "second" || atomic.LoadInt32(&attempts) != 2 {
		t.Fatalf("content=%q attempts=%d", msg.Content, atomic.LoadInt32(&attempts))
	}
}

// TestOpenAICompatibleModelStopsAtAttemptBudget keeps the retry bounded: a
// provider that stays broken costs the declared attempts and no more.
func TestOpenAICompatibleModelStopsAtAttemptBudget(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"overloaded"}`))
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test",
		MaxAttempts: 3, RetryBackoff: time.Millisecond, Client: server.Client(),
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err == nil {
		t.Fatal("expected the exhausted attempt budget to fail the call")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts=%d, want the declared 3", got)
	}
}

// TestOpenAICompatibleModelDoesNotRetryDeterministicFailure covers the spec
// scene where a wrong key or a malformed request costs one round trip.
func TestOpenAICompatibleModelDoesNotRetryDeterministicFailure(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"refused"}`))
		}))
		model := &OpenAICompatibleModel{
			APIKey: "key", BaseURL: server.URL, Model: "test",
			MaxAttempts: 3, RetryBackoff: time.Millisecond, Client: server.Client(),
		}
		_, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")})
		server.Close()
		if err == nil {
			t.Fatalf("HTTP %d must fail the call", status)
		}
		if got := atomic.LoadInt32(&attempts); got != 1 {
			t.Fatalf("HTTP %d attempts=%d, want 1", status, got)
		}
	}
}

// TestOpenAICompatibleModelStopsWaitingWhenCallerDeadlinePasses covers the spec
// scene where `Retry-After` outlives the caller: the wait ends with the caller's
// deadline instead of holding the run open, and no further request is sent.
func TestOpenAICompatibleModelStopsWaitingWhenCallerDeadlinePasses(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test",
		MaxAttempts: 3, RetryBackoff: time.Millisecond, Client: server.Client(),
	}
	started := time.Now()
	if _, err := model.Generate(ctx, []*schema.Message{schema.UserMessage("hello")}); err == nil {
		t.Fatal("expected the rate limit to fail the call")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("call waited %s, it must not outlive the caller deadline", elapsed)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts=%d, want 1", got)
	}
}

// TestOpenAICompatibleModelBoundsEachAttemptByProfileTimeout proves the profile
// timeout bounds an attempt even when the caller injects its own HTTP client.
func TestOpenAICompatibleModelBoundsEachAttemptByProfileTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "test",
		Timeout: 30 * time.Millisecond, MaxAttempts: 1,
		Client: &http.Client{Transport: server.Client().Transport},
	}
	started := time.Now()
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err == nil {
		t.Fatal("expected the elapsed attempt timeout to fail the call")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("attempt took %s, the profile timeout did not bound it", elapsed)
	}
}

// TestOpenAICompatibleModelSendsProfileReasoning covers the spec scene where a
// declared reasoning effort must reach the wire on every calling path.
func TestOpenAICompatibleModelSendsProfileReasoning(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(server.Close)

	model := &OpenAICompatibleModel{
		APIKey: "key", BaseURL: server.URL, Model: "k3-256k", Client: server.Client(),
		Profile: modelruntime.Profile{
			Provider:     modelruntime.ProviderKimi,
			Protocol:     modelruntime.ProtocolOpenAIChat,
			Reasoning:    modelruntime.ReasoningConfig{Mode: modelruntime.ReasoningProviderDefault, Effort: "high"},
			Capabilities: modelruntime.Capabilities{ToolUse: true, Thinking: true},
		},
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("body=%+v, want the profile thinking field on the wire", body)
	}
	if thinking["type"] != "enabled" || thinking["effort"] != "high" {
		t.Fatalf("thinking=%+v", thinking)
	}
}

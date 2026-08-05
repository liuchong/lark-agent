package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoctorNativeToolsVerifiesToolCallAndToolResult(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if call == 1 {
			if choice, ok := body["tool_choice"]; ok {
				t.Fatalf("doctor must not force tool_choice: %v", choice)
			}
			_, _ = w.Write([]byte(`{
				"id":"req_first",
				"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{
					"id":"call_1","type":"function","function":{"name":"doctor_echo","arguments":"{\"text\":\"ok\"}"}
				}]}}],
				"usage":{"total_tokens":5}
			}`))
			return
		}
		messages := body["messages"].([]any)
		toolMessage := messages[len(messages)-1].(map[string]any)
		if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_1" {
			t.Fatalf("tool message=%+v", toolMessage)
		}
		_, _ = w.Write([]byte(`{
			"id":"req_final",
			"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"ok\":true}"}}],
			"usage":{"total_tokens":7}
		}`))
	}))
	t.Cleanup(server.Close)
	model := &OpenAICompatibleModel{
		APIKey:  "key",
		BaseURL: server.URL,
		Model:   "test",
		Client:  server.Client(),
	}
	result, err := DoctorNativeTools(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolCallName != "doctor_echo" || result.FirstRequestID != "req_first" ||
		result.FinalRequestID != "req_final" || result.TotalTokens != 12 {
		t.Fatalf("result=%+v", result)
	}
}

package runtime

import (
	"context"
	"encoding/json"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/internal/apperr"
)

// ProviderDoctorResult proves the provider accepted native tool request and
// tool-result message wire formats.
type ProviderDoctorResult struct {
	ToolCallName   string `json:"tool_call_name"`
	FirstRequestID string `json:"first_request_id,omitempty"`
	FinalRequestID string `json:"final_request_id,omitempty"`
	TotalTokens    int    `json:"total_tokens,omitempty"`
}

// DoctorNativeTools executes a minimal two-request native tool round trip.
func DoctorNativeTools(ctx context.Context, model *OpenAICompatibleModel) (ProviderDoctorResult, error) {
	tool := &schema.ToolInfo{
		Name: "doctor_echo",
		Desc: "Echo one string to verify native tool calling.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"text": {Type: schema.String, Required: true},
		}),
	}
	messages := []*schema.Message{
		schema.SystemMessage("Call doctor_echo exactly once with text \"ok\"."),
		schema.UserMessage("Verify native tool calling."),
	}
	first, err := model.Generate(ctx, messages,
		einomodel.WithTools([]*schema.ToolInfo{tool}),
		einomodel.WithToolChoice(schema.ToolChoiceForced))
	if err != nil {
		return ProviderDoctorResult{}, err
	}
	if first == nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "doctor_echo" {
		return ProviderDoctorResult{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "provider did not return the required doctor_echo tool call")
	}
	var arguments struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(first.ToolCalls[0].Function.Arguments), &arguments); err != nil {
		return ProviderDoctorResult{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "provider returned invalid doctor_echo arguments").WithCause(err)
	}
	if arguments.Text == "" {
		return ProviderDoctorResult{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "provider returned empty doctor_echo text")
	}
	messages = append(messages, first, schema.ToolMessage(
		`{"ok":true,"echo":"`+arguments.Text+`"}`,
		first.ToolCalls[0].ID,
		schema.WithToolName("doctor_echo"),
	))
	final, err := model.Generate(ctx, messages,
		einomodel.WithTools([]*schema.ToolInfo{tool}),
		einomodel.WithToolChoice(schema.ToolChoiceForbidden))
	if err != nil {
		return ProviderDoctorResult{}, err
	}
	if final == nil || len(final.ToolCalls) != 0 {
		return ProviderDoctorResult{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "provider did not accept the native tool result message")
	}
	result := ProviderDoctorResult{ToolCallName: "doctor_echo"}
	result.FirstRequestID = requestID(first)
	result.FinalRequestID = requestID(final)
	for _, message := range []*schema.Message{first, final} {
		if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
			result.TotalTokens += message.ResponseMeta.Usage.TotalTokens
		}
	}
	return result, nil
}

func requestID(message *schema.Message) string {
	if message == nil || message.Extra == nil {
		return ""
	}
	value, _ := message.Extra["request_id"].(string)
	return value
}

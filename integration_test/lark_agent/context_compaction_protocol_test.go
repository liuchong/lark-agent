package larkagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
)

type protocolCheckingModel struct {
	calls           int
	maxContextBytes int
}

func (m *protocolCheckingModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		largeArguments := fmt.Sprintf(
			`{"path":"sample-project/sample-module","padding":%q}`,
			strings.Repeat("x", 12*1024),
		)
		return schema.AssistantMessage("", []schema.ToolCall{
			integrationToolCall("list", "list_workspace", largeArguments),
			integrationToolCall("search", "search_workspace", `{"query":"SampleRequest"}`),
		}), nil
	}
	if err := validateModelToolProtocol(input); err != nil {
		return nil, err
	}
	if got := integrationMessageBytes(input); got > m.maxContextBytes {
		return nil, fmt.Errorf("model-visible context bytes=%d exceeds %d", got, m.maxContextBytes)
	}
	for _, message := range input {
		if message == nil {
			continue
		}
		for _, call := range message.ToolCalls {
			if !json.Valid([]byte(call.Function.Arguments)) {
				return nil, fmt.Errorf("tool call %q has invalid compacted arguments", call.ID)
			}
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
		"submit",
		"submit_decision",
		`{
			"decision":"reply",
			"relevance_confidence":0.96,
			"reply_confidence":0.92,
			"risk":"low",
			"reply_text":"我完成了有边界的代码调查：目录读取成功，符号搜索失败原因已明确记录，没有据此编造实现结论。",
			"reason":"压缩后仍保留完整工具调用证据"
		}`,
	)}), nil
}

func (m *protocolCheckingModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func validateModelToolProtocol(messages []*schema.Message) error {
	pending := map[string]bool{}
	for index, message := range messages {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.Assistant:
			if len(pending) != 0 {
				return fmt.Errorf("assistant message %d interrupted pending tool calls: %+v", index, pending)
			}
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
		case schema.Tool:
			if !pending[message.ToolCallID] {
				return fmt.Errorf("orphaned tool result %q at message %d", message.ToolCallID, index)
			}
			delete(pending, message.ToolCallID)
		default:
			if len(pending) != 0 {
				return fmt.Errorf("role %q at message %d interrupted pending tool calls: %+v", message.Role, index, pending)
			}
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("missing tool results: %+v", pending)
	}
	return nil
}

func integrationMessageBytes(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		total += len(message.Content) + len(message.ReasoningContent)
		if data, err := json.Marshal(message.UserInputMultiContent); err == nil {
			total += len(data)
		}
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return total
}

func TestContextCompactionPreservesMultipleToolCallProtocol(t *testing.T) {
	model := &protocolCheckingModel{maxContextBytes: 4 * 1024}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "list_workspace"},
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{
					Content: strings.Repeat("sample-module bounded directory entry\n", 500),
				}, nil
			},
		},
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "search_workspace"},
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{}, errors.New("path-bounded search rejected this query")
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}

	decision, _, err := (agentruntime.AgentLoop{
		Model:             model,
		Tools:             registry,
		MaxTurns:          3,
		MaxToolBytes:      4 * 1024,
		MaxContextBytes:   4 * 1024,
		ContextCompaction: 0.80,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner", Name: "测试负责人"},
		Event: domain.NormalizedEvent{
			MessageID: "om_parallel_compaction",
			SenderID:  "ou_owner",
			Content:   "请核对当前实现，不确定的地方不要编造。",
		},
		WorkKind: domain.WorkKindDirectMention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || decision.Kind != domain.DecisionReply {
		t.Fatalf("model calls=%d decision=%+v", model.calls, decision)
	}
}

type irreducibleProtocolModel struct {
	calls int
}

func (m *irreducibleProtocolModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	if m.calls > 1 {
		return nil, errors.New("oversized protocol was sent to the model")
	}
	return schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
		strings.Repeat("call-id-", 1024),
		"list_workspace",
		`{"path":"sample-project/sample-module"}`,
	)}), nil
}

func (m *irreducibleProtocolModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestIrreducibleToolProtocolFailsBeforeNextModelRequest(t *testing.T) {
	model := &irreducibleProtocolModel{}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "list_workspace"},
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{Content: "bounded result"}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = (agentruntime.AgentLoop{
		Model:             model,
		Tools:             registry,
		MaxTurns:          3,
		MaxContextBytes:   4 * 1024,
		ContextCompaction: 0.80,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner", Name: "测试负责人"},
		Event: domain.NormalizedEvent{
			MessageID: "om_irreducible_protocol",
			SenderID:  "ou_owner",
			Content:   "请核对当前实现。",
		},
		WorkKind: domain.WorkKindDirectMention,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot preserve the latest complete tool protocol unit") {
		t.Fatalf("err=%v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls=%d, oversized second request must stay local", model.calls)
	}
}

type oldCheckpointOverflowModel struct {
	calls int
}

func (m *oldCheckpointOverflowModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	switch m.calls {
	case 1:
		calls := make([]schema.ToolCall, 0, 8)
		for index := range 8 {
			calls = append(calls, integrationToolCall(
				fmt.Sprintf("%s-%d", strings.Repeat("binding", 8), index),
				"list_workspace",
				fmt.Sprintf(`{"path":"entry-%d"}`, index),
			))
		}
		return schema.AssistantMessage("", calls), nil
	case 2:
		return schema.AssistantMessage("I should have used submit_decision.", nil), nil
	default:
		return nil, errors.New("incomplete checkpoint was sent to the model")
	}
}

func (m *oldCheckpointOverflowModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestIncompleteOldToolCheckpointFailsBeforeProviderRequest(t *testing.T) {
	model := &oldCheckpointOverflowModel{}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "list_workspace"},
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{Content: "ok"}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = (agentruntime.AgentLoop{
		Model:             model,
		Tools:             registry,
		MaxTurns:          4,
		MaxContextBytes:   4 * 1024,
		ContextCompaction: 0.80,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner", Name: "测试负责人"},
		Event: domain.NormalizedEvent{
			MessageID: "om_old_checkpoint_overflow",
			SenderID:  "ou_owner",
			Content:   "请核对旧的并行调用。",
		},
		WorkKind: domain.WorkKindDirectMention,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot preserve") {
		t.Fatalf("err=%v", err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls=%d, incomplete checkpoint must remain local", model.calls)
	}
}

type accumulatedArgumentsModel struct {
	calls           int
	maxContextBytes int
}

func (m *accumulatedArgumentsModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		calls := make([]schema.ToolCall, 0, 16)
		for index := range 16 {
			calls = append(calls, integrationToolCall(
				fmt.Sprintf("moderate-%d", index),
				"list_workspace",
				fmt.Sprintf(
					`{"path":"entry-%d","padding":%q}`,
					index,
					strings.Repeat("x", 240),
				),
			))
		}
		return schema.AssistantMessage("", calls), nil
	}
	if err := validateModelToolProtocol(input); err != nil {
		return nil, err
	}
	if got := integrationMessageBytes(input); got > m.maxContextBytes {
		return nil, fmt.Errorf("model-visible context bytes=%d exceeds %d", got, m.maxContextBytes)
	}
	for _, message := range input {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if len(call.Function.Arguments) > 160 ||
				!json.Valid([]byte(call.Function.Arguments)) {
				return nil, fmt.Errorf(
					"tool call %q arguments were not cumulatively bounded: %q",
					call.ID,
					call.Function.Arguments,
				)
			}
		}
	}
	return schema.AssistantMessage("", []schema.ToolCall{integrationToolCall(
		"submit",
		"submit_decision",
		`{
			"decision":"reply",
			"relevance_confidence":0.96,
			"reply_confidence":0.92,
			"risk":"low",
			"reply_text":"我完成了有边界的并行检查，累计参数已安全压缩，工具结果和调用编号仍保持对应。",
			"reason":"累计中等参数压缩后正常收敛"
		}`,
	)}), nil
}

func (m *accumulatedArgumentsModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestAccumulatedModerateToolArgumentsCompactWithinAgentLoop(t *testing.T) {
	model := &accumulatedArgumentsModel{maxContextBytes: 8 * 1024}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "list_workspace"},
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{Content: "ok"}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}

	decision, _, err := (agentruntime.AgentLoop{
		Model:             model,
		Tools:             registry,
		MaxTurns:          3,
		MaxToolCalls:      16,
		MaxContextBytes:   8 * 1024,
		ContextCompaction: 0.80,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner", Name: "测试负责人"},
		Event: domain.NormalizedEvent{
			MessageID: "om_accumulated_arguments",
			SenderID:  "ou_owner",
			Content:   "请并行核对这些入口。",
		},
		WorkKind: domain.WorkKindDirectMention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || decision.Kind != domain.DecisionReply {
		t.Fatalf("model calls=%d decision=%+v", model.calls, decision)
	}
}

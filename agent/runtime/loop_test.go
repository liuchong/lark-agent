package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
)

type scriptedModel struct {
	responses []*schema.Message
	calls     int
	inputs    [][]*schema.Message
}

func (m *scriptedModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if m.calls >= len(m.responses) {
		return nil, errors.New("unexpected model call")
	}
	response := m.responses[m.calls]
	m.calls++
	return response, nil
}

func (m *scriptedModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestAgentLoopSearchesReadsAndSubmitsDecision(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_plan", "submit_investigation_plan", `{
			"question":"这个接口如何防高频攻击？",
			"entry_points":["router.go"],
			"symbols":["pagination"],
			"tools":["search_workspace","read_workspace"],
			"stop_conditions":["找到路由实现和防护逻辑"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_search", "search_workspace", `{"query":"pagination"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_read", "read_workspace", `{"path":"router.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.92,
			"risk":"low",
			"reply_text":"接口有分页上限和索引，但没有独立限频。",
			"reason":"代码证据充分",
			"source_refs":[{"relative_path":"router.go","digest":"sha256:test","kind":"workspace_file"}]
		}`)}),
	}}
	var called []string
	registry, err := agenttools.NewRegistry(
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			called = append(called, "search_workspace")
			return agenttools.Execution{Content: `{"matches":["router.go"]}`}, nil
		}),
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			called = append(called, "read_workspace")
			return agenttools.Execution{
				Content: `POST("/sample/items")`,
				Sources: []domain.SourceRef{{RelativePath: "router.go", Digest: "sha256:test", Kind: "workspace_file"}},
			}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 6}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{MessageID: "om_1", Content: "这个接口如何防高频攻击？"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || decision.ReplyText == "" {
		t.Fatalf("decision=%+v", decision)
	}
	if len(called) != 2 || called[0] != "search_workspace" || called[1] != "read_workspace" {
		t.Fatalf("called=%+v", called)
	}
	if len(trajectory) != 8 || model.calls != 4 {
		t.Fatalf("trajectory=%d model calls=%d", len(trajectory), model.calls)
	}
	for i, input := range model.inputs {
		want := fmt.Sprintf("Current model turn: %d of 6", i+1)
		if !messagesContain(input, want) {
			t.Fatalf("model call %d missing %q: %+v", i+1, want, input)
		}
		if !messagesContain(input, "The hard model-turn limit for this run is 6") {
			t.Fatalf("model call %d missing total budget", i+1)
		}
		if countMessagesContaining(input, "Current model turn:") != 1 {
			t.Fatalf("model call %d accumulated progress messages: %+v", i+1, input)
		}
	}
	lastInput := model.inputs[3]
	if lastInput[len(lastInput)-1].Role != schema.System ||
		!strings.Contains(lastInput[len(lastInput)-1].Content, "Remaining model turns after this request: 2") {
		t.Fatalf("last progress input=%+v", lastInput[len(lastInput)-1])
	}
	if lastInput[len(lastInput)-2].Role != schema.Tool || lastInput[len(lastInput)-2].ToolCallID != "call_read" {
		t.Fatalf("last input=%+v", lastInput)
	}
}

func TestAgentLoopRejectsPlainTextWithoutSubmit(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{{Role: schema.Assistant, Content: "直接回复"}}}
	registry, err := agenttools.NewRegistry(SubmitDecisionDefinition())
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 1}
	if _, _, err := loop.Decide(context.Background(), agentcontext.Bundle{}); err == nil {
		t.Fatal("accepted plain assistant text without submit_decision")
	}
}

func TestAgentLoopAllowsEvidenceBackedNotifyForDirectOwnerMention(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_context", "get_lark_context", `{
			"chat_id":"oc_backend",
			"message_id":"om_direct_question"
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_notify", "submit_decision", `{
			"decision":"notify",
			"relevance_confidence":0.92,
			"risk":"medium",
			"reason":"same-chat evidence is insufficient for a safe sender-facing answer",
			"owner_action":"confirm backend rate limiting"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("get_lark_context", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: `{"messages":[{"content":"no implementation detail"}]}`}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 4}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		WorkKind: domain.WorkKindDirectMention,
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_direct_question",
			ChatID:    "oc_backend",
			SenderID:  "ou_other",
			Content:   "@Owner 这个接口如何防高频攻击？",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionNotify || model.calls != 2 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if len(trajectory) < 2 {
		t.Fatalf("trajectory=%+v", trajectory)
	}
}

func TestTerminalDecisionRejectsAssistantNotifyWhenOwnerIsAlsoMentioned(t *testing.T) {
	err := validateTerminalDecision(agentcontext.Bundle{
		WorkKind: domain.WorkKindSimpleQuestion,
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			Mentions: []domain.Mention{
				{OpenID: "ou_assistant"},
				{OpenID: "ou_owner"},
			},
		},
	}, domain.Decision{Kind: domain.DecisionNotify})
	if err == nil {
		t.Fatal("assistant request that also mentions owner finished as notify")
	}
}

func TestAgentLoopRecoversPlainTextAfterRejectedTerminalDecision(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_notify", "submit_decision", `{
			"decision":"notify",
			"relevance_confidence":0.92,
			"risk":"medium",
			"reason":"owner request cannot finish as notify only",
			"owner_action":"confirm backend rate limiting"
		}`)}),
		{Role: schema.Assistant, Content: "我先收到这个问题了，会让Owner确认。"},
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_reply", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.9,
			"risk":"low",
			"reply_text":"目前没有足够代码证据确认线上是否已有独立限频，需要继续检查生产实现。",
			"reason":"owner request receives a truthful answer"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(SubmitDecisionDefinition())
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 5}
	decision, _, err := loop.Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_direct_question",
			SenderID:  "ou_owner",
			Content:   "这个接口如何防高频攻击？",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 3 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if !messagesContain(model.inputs[2], "Plain assistant text is not accepted") {
		t.Fatalf("third model input missing plain-text correction: %+v", model.inputs[2])
	}
}

func TestAgentLoopExhaustsRepeatedSourceLessWorkspaceSearches(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"如果做示例状态变更通知，需要同步示例客户端 吗？",
			"entry_points":["penalty status change"],
			"symbols":["SDK callback","sample notification"],
			"tools":["search_workspace"],
			"stop_conditions":["找到回调定义或连续搜索无证据"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search_1", "search_workspace", `{"query":"penalty callback"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search_2", "search_workspace", `{"query":"penalty status change"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search_3", "search_workspace", `{"query":"sample state change sample notification"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search_4", "search_workspace", `{"query":"another broad search"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.94,
			"reply_confidence":0.86,
			"risk":"low",
			"reply_text":"我已查了相关工作区入口，但连续三次搜索仍未找到可引用的生产实现。目前无法确认示例状态变更通知是否需要新增 示例客户端回调类型和示例通知，需要测试负责人继续核对具体入口。",
			"owner_action":"确认示例状态变更通知是否需要新增 示例客户端回调类型和示例通知，并同步给提问人。",
			"reason":"repeated broad workspace searches produced no source; reply with unknowns and owner confirmation boundary"
		}`)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			searchCalls++
			return agenttools.Execution{Content: `{"results":null,"truncated":true,"files_scanned":2000}`}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 8}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_direct_question",
			Content:   "@Owner 如果做示例状态变更通知，需要同步示例客户端 吗？",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || searchCalls != 3 {
		t.Fatalf("decision=%+v searchCalls=%d", decision, searchCalls)
	}
	if !trajectoryContains(trajectory, "search_workspace is exhausted for this work item") {
		t.Fatalf("trajectory missing exhausted search feedback: %+v", trajectory)
	}
}

func TestAgentLoopRequiresInvestigationPlanBeforeBroadCodingSearch(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search_before_plan", "search_workspace", `{"query":"sample items pagination"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"POST /api/sample/items 是否直接访问 SampleDB",
			"entry_points":["routes","sample items service"],
			"symbols":["pagination"],
			"tools":["search_code_symbols","read_workspace"],
			"stop_conditions":["找到路由和数据库访问实现，或明确没有索引证据"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search_after_plan", "search_workspace", `{"query":"sample items sampledb"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.94,
			"reply_confidence":0.86,
			"risk":"low",
			"reply_text":"我会先按路由和 service 查证；目前还没有足够代码证据确认是否每次直连 SampleDB。",
			"reason":"investigation plan accepted before broad search"
		}`)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			searchCalls++
			return agenttools.Execution{Content: `{"results":[]}`}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 6}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{MessageID: "om_code", Content: "@Owner POST /api/sample/items 每次都会直接访问 SampleDB 吗？"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || searchCalls != 1 {
		t.Fatalf("decision=%+v searchCalls=%d", decision, searchCalls)
	}
	if !trajectoryContains(trajectory, "coding investigation requires submit_investigation_plan before broad workspace search") {
		t.Fatalf("trajectory missing plan gate feedback: %+v", trajectory)
	}
}

func TestAgentLoopRejectsRepeatedNoProgressLarkContext(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("ctx_1", "get_lark_context", `{"chat_id":"oc_1","message_id":"om_1"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("ctx_2", "get_lark_context", `{"chat_id":"oc_1","message_id":"om_1"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.94,
			"reply_confidence":0.86,
			"risk":"low",
			"reply_text":"我没有拿到新的上下文，只能基于当前消息先说明未知点并请Owner确认。",
			"reason":"repeated Lark context produced no progress"
		}`)}),
	}}
	larkCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("get_lark_context", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			larkCalls++
			return agenttools.Execution{Content: `{"messages":[],"no_new_context":true}`}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 4}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || larkCalls != 1 {
		t.Fatalf("decision=%+v larkCalls=%d", decision, larkCalls)
	}
	if !trajectoryContains(trajectory, "get_lark_context returned no new context") {
		t.Fatalf("trajectory missing no-progress feedback: %+v", trajectory)
	}
}

func TestAgentLoopSoftHandlesToolBudget(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("large_read", "read_workspace", `{"path":"large.log"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.91,
			"reply_confidence":0.86,
			"risk":"low",
			"reply_text":"我查到大文件输出已经被压缩，当前只能确认 large.log 中有相关线索；具体结论还需要按更小范围继续读对应源码。",
			"reason":"tool output budget forced evidence summary"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: strings.Repeat("very large evidence\n", 200)}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 4, MaxToolBytes: 256, MaxTotalBytes: 400}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 2 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if len(trajectory) == 0 || !messagesContain(model.inputs[1], "Tool output budget is near or above the configured limit") {
		t.Fatalf("second model input missing soft-budget prompt: %+v", model.inputs[1])
	}
}

func TestAgentLoopForcesTerminalBeforeTurnExhaustion(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("search", "search_workspace", `{"query":"broad query"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.91,
			"reply_confidence":0.86,
			"risk":"low",
			"reply_text":"目前只查到一个候选文件，还没有完整证据；需要Owner继续确认具体实现路径。",
			"reason":"remaining turn budget forced terminal decision with unknowns"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: `{"results":[{"source":{"relative_path":"router.go","digest":"sha256:test","kind":"workspace_file"},"snippet":"route"}]}`}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 2}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 2 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if len(trajectory) == 0 || !messagesContain(model.inputs[1], "You are at the final model turn") {
		t.Fatalf("second model input missing final-turn convergence prompt: %+v", model.inputs[1])
	}
}

func TestAgentLoopReservesFinalCodingTurnsAfterCitableEvidence(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"GetType 对 .JPG 返回什么",
			"entry_points":["content_type.go"],
			"symbols":["GetType"],
			"tools":["read_workspace"],
			"stop_conditions":["读到 GetType 定义"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read", "read_workspace", `{"path":"content_type.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("extra", "search_workspace", `{"query":"unrelated call sites"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.92,
			"risk":"low",
			"reply_text":"结论：.JPG 返回 image/jpeg。依据：content_type.go 的 GetType 会去掉前导点并做小写映射。未知/下一步：没有。",
			"reason":"exact function definition answers the requested behavior",
			"source_refs":[{"relative_path":"content_type.go","digest":"sha256:test","kind":"workspace_file"}]
		}`)}),
	}}
	extraSearches := 0
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{
				Content: `func GetType(s string) string { return "image/jpeg" }`,
				Sources: []domain.SourceRef{{RelativePath: "content_type.go", Digest: "sha256:test", Kind: "workspace_file"}},
			}, nil
		}),
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			extraSearches++
			return agenttools.Execution{Content: `{"results":[]}`}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 4}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		Event:    domain.NormalizedEvent{MessageID: "om_exact", Content: "请检查 GetType 对 .JPG 返回什么"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if extraSearches != 0 {
		t.Fatalf("extra searches executed=%d", extraSearches)
	}
	if !messagesContain(model.inputs[2], "Citable workspace evidence is now available") {
		t.Fatalf("third model input missing evidence convergence prompt: %+v", model.inputs[2])
	}
	if !trajectoryContains(trajectory, "final coding turns are reserved for submit_decision") {
		t.Fatalf("trajectory missing terminal-only rejection: %+v", trajectory)
	}
}

func TestCompactMessagesPreservesRecentTurnsWithinBudget(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("初始上下文", 2000)),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("old", "read_workspace", `{"path":"old.go"}`)}),
		schema.ToolMessage(strings.Repeat("旧工具结果", 2000), "old"),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("recent", "search_workspace", `{"query":"recent"}`)}),
		schema.ToolMessage("recent evidence", "recent"),
	}
	compacted := compactMessages(messages, 12*1024)
	if messageBytes(compacted) > 12*1024 {
		t.Fatalf("compacted bytes=%d", messageBytes(compacted))
	}
	if compacted[len(compacted)-1].Content != "recent evidence" {
		t.Fatalf("recent result changed: %q", compacted[len(compacted)-1].Content)
	}
	for _, message := range compacted {
		if !utf8.ValidString(message.Content) {
			t.Fatal("compaction produced invalid UTF-8")
		}
	}
}

func TestClipToolContentKeepsValidJSON(t *testing.T) {
	clipped := clipToolContent(`{"ok":true,"content":"`+strings.Repeat("证据", 1000)+`"}`, 512)
	if len(clipped) > 512 {
		t.Fatalf("clipped bytes=%d", len(clipped))
	}
	if !json.Valid([]byte(clipped)) {
		t.Fatalf("invalid JSON: %q", clipped)
	}
}

func toolCall(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func testTool(name string, execute func(context.Context, json.RawMessage) (agenttools.Execution, error)) agenttools.Definition {
	return agenttools.Definition{
		Info:             &schema.ToolInfo{Name: name},
		NonOwnerReadOnly: true,
		Execute:          execute,
	}
}

func TestKindSpecificTurnBudgetsAreEnforced(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("one", nil),
		schema.AssistantMessage("two", nil),
		schema.AssistantMessage("must not be reached", nil),
	}}
	registry, err := agenttools.NewRegistry(SubmitDecisionDefinition())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 150,
		SimpleMaxTurns: 2, CodingMaxTurns: 20, GoalMaxTurns: 150,
	}).Decide(context.Background(), agentcontext.Bundle{
		WorkKind: domain.WorkKindSimpleQuestion,
		Event:    domain.NormalizedEvent{MessageID: "om_simple", Content: "hello"},
	})
	if err == nil || model.calls != 2 {
		t.Fatalf("calls=%d err=%v", model.calls, err)
	}
	loop := AgentLoop{MaxTurns: 150, SimpleMaxTurns: 2, CodingMaxTurns: 20, GoalMaxTurns: 150}
	if got := loop.maxTurnsForWorkKind(domain.WorkKindCodingQuestion); got != 20 {
		t.Fatalf("coding max turns=%d", got)
	}
	if got := loop.maxTurnsForWorkKind(domain.WorkKindCodingGoal); got != 150 {
		t.Fatalf("goal max turns=%d", got)
	}
}

func TestToolCallAndNoProgressBudgetsForceConvergence(t *testing.T) {
	var responses []*schema.Message
	for index := 1; index <= 5; index++ {
		responses = append(responses, schema.AssistantMessage("", []schema.ToolCall{
			toolCall(fmt.Sprintf("call_%d", index), "probe", `{"query":"same"}`),
		}))
	}
	responses = append(responses, schema.AssistantMessage("", []schema.ToolCall{
		toolCall("submit", "submit_decision", `{
			"decision":"ignore",
			"relevance_confidence":0,
			"risk":"low",
			"reason":"bounded convergence"
		}`),
	}))
	model := &scriptedModel{responses: responses}
	executions := 0
	registry, err := agenttools.NewRegistry(
		testTool("probe", func(context.Context, json.RawMessage) (agenttools.Execution, error) {
			executions++
			return agenttools.Execution{Content: `{"same":true}`}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 20,
		CodingMaxTurns: 20, MaxToolCalls: 10, MaxNoProgress: 3, MaxRepeatedCalls: 100,
	}).Decide(context.Background(), agentcontext.Bundle{
		WorkKind: domain.WorkKindCodingQuestion,
		Event:    domain.NormalizedEvent{MessageID: "om_budget", Content: "investigate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if executions != 3 || decision.Kind != domain.DecisionIgnore {
		t.Fatalf("executions=%d decision=%+v", executions, decision)
	}
}

func messagesContain(messages []*schema.Message, text string) bool {
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func countMessagesContaining(messages []*schema.Message, text string) int {
	count := 0
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, text) {
			count++
		}
	}
	return count
}

func trajectoryContains(messages []*schema.Message, text string) bool {
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

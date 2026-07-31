package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type scriptedModel struct {
	responses []*schema.Message
	calls     int
	inputs    [][]*schema.Message
	toolNames [][]string
}

func (m *scriptedModel) Generate(_ context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	options := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(options.Tools))
	for _, tool := range options.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
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
		usedToolCalls := []int{0, 0, 1, 2}
		want = fmt.Sprintf(
			"Tool-call budget: %d of 16 investigation calls used, %d remaining",
			usedToolCalls[i],
			16-usedToolCalls[i],
		)
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

func TestAgentLoopMakesInheritedExactScopeActionableAndHidesUnscopedTools(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "sample-org")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(workspaceRoot, "sample-project/sample-module/sample-client/request.kt")
	if err := os.MkdirAll(filepath.Dir(requestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"核对 Sample-Client 示例事件请求和本地收敛",
			"entry_points":["sample-client/request.kt","sample-client/listener.kt"],
			"symbols":["sampleContent","onSampleEvent"],
			"tools":["search_workspace","read_workspace"],
			"stop_conditions":["读到请求结构和编辑事件"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall(
			"hidden-rules",
			"read_workspace_rules",
			`{}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall(
			"read",
			"read_workspace",
			`{"path":"sample-client/request.kt"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"evidence_status":"verified",
			"reply_text":"结论：sampleContent 是字符串消息 JSON；本地通过 onSampleEvent 收敛。依据：已读取请求生产文件。未知/下一步：没有。",
			"reason":"exact scoped production read",
			"source_refs":[{"relative_path":"sample-project/sample-module/sample-client/request.kt","digest":"sha256:scoped","kind":"workspace_file"}]
		}`)}),
	}}
	var readPath string
	hiddenRuleCalls := 0
	noOp := func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
		return agenttools.Execution{Content: `{"ok":true}`}, nil
	}
	registry, err := agenttools.NewRegistry(
		testTool("explore_workspace", noOp),
		testTool("search_code_symbols", noOp),
		testTool("trace_code_path", noOp),
		testTool("shell", noOp),
		testTool("read_workspace_rules", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			hiddenRuleCalls++
			return agenttools.Execution{Content: "must not execute"}, nil
		}),
		testTool("list_skills", noOp),
		testTool("load_skill", noOp),
		testTool("list_workspace", noOp),
		testTool("search_workspace", noOp),
		testTool("read_workspace", func(_ context.Context, raw json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return agenttools.Execution{}, err
			}
			readPath = input.Path
			return agenttools.Execution{
				Content: "sampleContent string; onSampleEvent",
				Sources: []domain.SourceRef{{
					RelativePath: input.Path,
					Digest:       "sha256:scoped",
					Kind:         "workspace_file",
				}},
			}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 5,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Environment: agentcontext.EnvironmentSnapshot{
			WorkspaceRoot:     workspaceRoot,
			WorkspaceRealRoot: workspaceRoot,
			Directory: []agentcontext.DirectoryEntry{{
				Path: "sample-project",
				Kind: "dir",
			}},
		},
		Event: domain.NormalizedEvent{
			MessageID: "om_follow_up",
			SenderID:  "ou_owner",
			Content:   "结合上一条，完整核对请求结构和本地收敛。",
		},
		Conversation: []domain.NormalizedEvent{{
			MessageID: "om_scope",
			SenderID:  "ou_owner",
			Content:   "这次只看 sample-org/sample-project/sample-module。",
		}},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply {
		t.Fatalf("decision=%+v", decision)
	}
	if readPath != "sample-project/sample-module/sample-client/request.kt" {
		t.Fatalf("read path=%q", readPath)
	}
	if hiddenRuleCalls != 0 {
		t.Fatalf("hidden exact-scope tool executed %d times", hiddenRuleCalls)
	}
	if !trajectoryContains(trajectory, "cannot guarantee exact workspace scope sample-project/sample-module") {
		t.Fatalf("hidden tool rejection missing from trajectory: %+v", trajectory)
	}
	if len(model.toolNames) == 0 {
		t.Fatal("model tool catalog was not captured")
	}
	catalog := strings.Join(model.toolNames[0], ",")
	for _, forbidden := range []string{
		"explore_workspace", "search_code_symbols", "trace_code_path", "shell",
		"read_workspace_rules", "list_skills", "load_skill",
	} {
		if strings.Contains(catalog, forbidden) {
			t.Fatalf("exact-scope tool catalog exposes %s: %v", forbidden, model.toolNames[0])
		}
	}
	for _, allowed := range []string{
		"list_workspace", "search_workspace", "read_workspace",
		"submit_investigation_plan", "submit_decision",
	} {
		if !strings.Contains(catalog, allowed) {
			t.Fatalf("exact-scope tool catalog missing %s: %v", allowed, model.toolNames[0])
		}
	}
	for _, want := range []string{
		"Exact coding workspace scope: sample-project/sample-module",
		"already a readable subtree inside the configured workspace root",
		"at most two bounded locating searches",
	} {
		if !messagesContain(model.inputs[0], want) {
			t.Fatalf("initial model input missing %q: %+v", want, model.inputs[0])
		}
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

func TestAgentLoopRejectsNotifyForDelegatedCodingQuestion(t *testing.T) {
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
		schema.AssistantMessage("", []schema.ToolCall{toolCall(
			"search",
			"search_code_symbols",
			`{"query":"rate limiting","max_results":5}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_reply", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.92,
			"reply_confidence":0.9,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_text":"目前没有足够代码证据确认这个接口如何防高频攻击。",
			"reason":"bounded code search produced no authoritative production read"
		}`)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("get_lark_context", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: `{"messages":[{"content":"no implementation detail"}]}`}, nil
		}),
		testTool("search_code_symbols", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			searchCalls++
			return agenttools.Execution{Content: `{"matches":[]}`}, nil
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
	if decision.Kind != domain.DecisionReply || model.calls != 4 || searchCalls != 1 {
		t.Fatalf("decision=%+v calls=%d search_calls=%d", decision, model.calls, searchCalls)
	}
	if len(trajectory) < 2 || !trajectoryContains(trajectory, "coding question cannot finish as notify") {
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

func TestTerminalDecisionRejectsNotifyOnlyForDelegatedPrivateMessage(t *testing.T) {
	err := validateTerminalDecision(agentcontext.Bundle{
		WorkKind: domain.WorkKindDirectMention,
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			ChatType: "p2p",
			SenderID: "ou_teammate",
		},
	}, domain.Decision{Kind: domain.DecisionNotify})
	if err == nil || !strings.Contains(err.Error(), "delegated work cannot finish") {
		t.Fatalf("err=%v", err)
	}
}

func TestTerminalDecisionRejectsSilentOutcomesForUnansweredDelegatedWork(t *testing.T) {
	bundles := map[string]agentcontext.Bundle{
		"group_mention": {
			WorkKind: domain.WorkKindDirectMention,
			User:     agentcontext.UserProfile{OpenID: "ou_owner"},
			Event: domain.NormalizedEvent{
				ChatType: "group",
				SenderID: "ou_teammate",
				Content:  "@测试负责人 这里的示例视觉层级、示例分隔样式和示例内容密度都需要优化",
				Mentions: []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}},
			},
		},
		"human_private_message": {
			WorkKind: domain.WorkKindDirectMention,
			User:     agentcontext.UserProfile{OpenID: "ou_owner"},
			Event: domain.NormalizedEvent{
				ChatType: "p2p",
				SenderID: "ou_teammate",
				Content:  "能否确认这里的字体和示例内容密度怎么调整？",
			},
		},
	}
	for name, bundle := range bundles {
		for _, kind := range []domain.DecisionKind{
			domain.DecisionIgnore,
			domain.DecisionRecord,
			domain.DecisionNotify,
		} {
			t.Run(name+"/"+string(kind), func(t *testing.T) {
				err := validateTerminalDecision(bundle, domain.Decision{Kind: kind})
				if err == nil || !strings.Contains(err.Error(), "delegated work cannot finish") {
					t.Fatalf("kind=%s err=%v", kind, err)
				}
			})
		}
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
		schema.AssistantMessage("", []schema.ToolCall{toolCall(
			"search",
			"search_code_symbols",
			`{"query":"rate limiting","max_results":5}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("call_reply", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.9,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_text":"目前没有足够代码证据确认线上是否已有独立限频，需要继续检查生产实现。",
			"reason":"owner request receives a truthful answer"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("search_code_symbols", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: `{"matches":[]}`}, nil
		}),
		SubmitDecisionDefinition(),
	)
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
	if decision.Kind != domain.DecisionReply || model.calls != 4 {
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
			"evidence_status":"insufficient",
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
			"evidence_status":"insufficient",
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

func TestAgentLoopKeepsBoundedReadsAvailableUntilMultiFieldQuestionIsAnswered(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"SampleRequest 的 sampleContent 结构和本地收敛回调分别是什么",
			"entry_points":["sample-project/sample-module/sample-client/request.go","sample-project/sample-module/sample-client/listener.go"],
			"symbols":["SampleRequest","onSampleEvent"],
			"tools":["read_workspace"],
			"stop_conditions":["读到请求结构","读到本地示例事件回调"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read-request", "read_workspace", `{"path":"sample-project/sample-module/sample-client/request.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read-listener", "read_workspace", `{"path":"sample-project/sample-module/sample-client/listener.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.92,
			"risk":"low",
			"reply_text":"结论：sampleContent 是文本消息 JSON，本地通过 onSampleEvent 收敛。依据：请求定义和监听器实现。未知/下一步：没有。",
			"reason":"both requested fields now have production evidence",
			"source_refs":[
				{"relative_path":"sample-project/sample-module/sample-client/request.go","digest":"sha256:request","kind":"workspace_file"},
				{"relative_path":"sample-project/sample-module/sample-client/listener.go","digest":"sha256:listener","kind":"workspace_file"}
			]
		}`)}),
	}}
	readPaths := make([]string, 0, 2)
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, raw json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return agenttools.Execution{}, err
			}
			readPaths = append(readPaths, input.Path)
			digest := "sha256:request"
			content := "type SampleRequest struct { SampleContent string } // " +
				`{"content":"sample value"}`
			if strings.HasSuffix(input.Path, "listener.go") {
				digest = "sha256:listener"
				content = `func onSampleEvent(message Message) { updateLocal(message) }`
			}
			return agenttools.Execution{
				Content: content,
				Sources: []domain.SourceRef{{RelativePath: input.Path, Digest: digest, Kind: "workspace_file"}},
			}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	loop := AgentLoop{Model: model, Tools: registry, MaxTurns: 8}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_multi_field",
			Content:   "请检查 sample-org/sample-project/sample-module 里 SampleRequest 的 sampleContent 结构和本地收敛回调",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if got, want := strings.Join(readPaths, ","), "sample-project/sample-module/sample-client/request.go,sample-project/sample-module/sample-client/listener.go"; got != want {
		t.Fatalf("read paths=%q want=%q", got, want)
	}
	if !messagesContain(model.inputs[2], "Citable workspace evidence is now available") {
		t.Fatalf("third model input missing evidence convergence prompt: %+v", model.inputs[2])
	}
	if trajectoryContains(trajectory, "coding evidence is complete; submit_decision is required now") {
		t.Fatalf("partial evidence incorrectly forced terminal-only mode: %+v", trajectory)
	}
}

func TestCanonicalizeDecisionSourcesUsesUniqueObservedIdentity(t *testing.T) {
	recorded := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/SampleRequest.java",
		Digest:       "sha256:fb39caf1b13bea28",
		Kind:         "workspace_file",
	}
	allowed := map[string]bool{sourceKey(recorded): true}
	observed := map[string]map[string]domain.SourceRef{}
	recordObservedSource(observed, recorded)
	decision := domain.Decision{Sources: []domain.SourceRef{{
		RelativePath: recorded.RelativePath,
		Digest:       "fb39caf1b13bea28cce16d2744a964e1445808b2a3be0861414de47f85d7ead7",
		Kind:         recorded.Kind,
	}}}

	decision = canonicalizeDecisionSources(decision, allowed, observed)

	if len(decision.Sources) != 1 || decision.Sources[0] != recorded {
		t.Fatalf("sources=%+v want %+v", decision.Sources, recorded)
	}
	if err := validateDecisionSources(decision, allowed); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalizeDecisionSourcesDoesNotGuessBetweenObservedVersions(t *testing.T) {
	first := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/SampleRequest.java",
		Digest:       "sha256:first",
		Kind:         "workspace_file",
	}
	second := first
	second.Digest = "sha256:second"
	allowed := map[string]bool{
		sourceKey(first):  true,
		sourceKey(second): true,
	}
	observed := map[string]map[string]domain.SourceRef{}
	recordObservedSource(observed, first)
	recordObservedSource(observed, second)
	decision := domain.Decision{Sources: []domain.SourceRef{{
		RelativePath: first.RelativePath,
		Digest:       "sha256:tool-result",
		Kind:         first.Kind,
	}}}

	decision = canonicalizeDecisionSources(decision, allowed, observed)

	if decision.Sources[0].Digest != "sha256:tool-result" {
		t.Fatalf("ambiguous source was rewritten: %+v", decision.Sources[0])
	}
	if err := validateDecisionSources(decision, allowed); err == nil {
		t.Fatal("ambiguous source should remain unavailable")
	}
}

func TestAgentLoopReservesFinalTwoTurnsAfterCitableEvidence(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"确认 GetType 的直接返回值",
			"entry_points":["content_type.go"],
			"symbols":["GetType"],
			"tools":["read_workspace"],
			"stop_conditions":["读到 GetType 定义"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read", "read_workspace", `{"path":"content_type.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("late-search", "search_workspace", `{"query":"unrelated history"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.92,
			"risk":"low",
			"reply_text":"结论：GetType 直接返回 image/jpeg。依据：content_type.go 的函数定义。未知/下一步：没有。",
			"reason":"the final two turns were reserved for a source-backed decision",
			"source_refs":[{"relative_path":"content_type.go","digest":"sha256:type","kind":"workspace_file"}]
		}`)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{
				Content: `func GetType(string) string { return "image/jpeg" }`,
				Sources: []domain.SourceRef{{
					RelativePath: "content_type.go",
					Digest:       "sha256:type",
					Kind:         "workspace_file",
				}},
			}, nil
		}),
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
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event:    domain.NormalizedEvent{MessageID: "om_final_two", Content: "确认 GetType 的直接返回值"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 || searchCalls != 0 {
		t.Fatalf("decision=%+v modelCalls=%d searchCalls=%d", decision, model.calls, searchCalls)
	}
	if !trajectoryContains(trajectory, "remaining turn") {
		t.Fatalf("trajectory missing final-two-turn rejection: %+v", trajectory)
	}
}

func TestAgentLoopPreservesEvidenceForGroundingCorrection(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"确认示例事件通知、回调和字段",
			"entry_points":["sample-client/SampleEvent.java"],
			"symbols":["onSampleEvent","sampleFlag","sampleTimestamp","sampleVersion"],
			"tools":["read_workspace"],
			"stop_conditions":["读到通知编号、回调和字段"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read", "read_workspace", `{"path":"sample-client/SampleEvent.java"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("unsupported", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：WebSocket eventCode 为 9001，回调 onSampleEvent 更新 sampleFlag、sampleTimestamp 和 sampleVersion。依据：sample-client/SampleEvent.java。未知/下一步：没有。",
			"reason":"authoritative read completed",
			"source_refs":[{"relative_path":"sample-client/SampleEvent.java","digest":"sha256:sample-event","kind":"workspace_file"}]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("downgrade", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"insufficient",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"上下文已经压缩，无法再次核验。",
			"reason":"the cited evidence was lost after compaction",
			"source_refs":[{"relative_path":"sample-client/SampleEvent.java","digest":"sha256:sample-event","kind":"workspace_file"}]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("corrected", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：WebSocket 通知编号为 9001，回调 onSampleEvent 更新 sampleFlag、sampleTimestamp 和 sampleVersion。依据：sample-client/SampleEvent.java。未知/下一步：没有。",
			"reason":"removed the unsupported identifier while preserving cited facts",
			"source_refs":[{"relative_path":"sample-client/SampleEvent.java","digest":"sha256:sample-event","kind":"workspace_file"}]
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{
				Content: "WebSocket notification 9001 fires onSampleEvent and updates sampleFlag, sampleTimestamp, and sampleVersion.",
				Sources: []domain.SourceRef{{
					RelativePath: "sample-client/SampleEvent.java",
					Digest:       "sha256:sample-event",
					Kind:         "workspace_file",
				}},
			}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_grounding_correction",
			Content:   "检查当前项目的示例事件通知编号、回调和字段",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply ||
		decision.EvidenceStatus != domain.EvidenceVerified ||
		model.calls != 5 ||
		!strings.Contains(decision.ReplyText, "通知编号为 9001") {
		t.Fatalf("decision=%+v modelCalls=%d trajectory=%+v", decision, model.calls, trajectory)
	}
	if !trajectoryContains(trajectory, "must not downgrade") {
		t.Fatalf("trajectory missing evidence-preserving correction rejection: %+v", trajectory)
	}
	if !messagesContain(model.inputs[4], "Only submit_decision is available now") {
		t.Fatalf("correction turn exposed non-terminal tools: %+v", model.inputs[4])
	}
	if !messagesContain(model.inputs[4], "Current model turn: 5 of 5") {
		t.Fatalf("correction turn did not report its expanded total budget: %+v", model.inputs[4])
	}
}

func TestAgentLoopAllowsGenuineInsufficientAfterGroundingFailure(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"确认通知使用的协议字段名",
			"entry_points":["sample-client/SampleEvent.java"],
			"symbols":["eventCode"],
			"tools":["read_workspace"],
			"stop_conditions":["读到协议字段名或确认来源未定义"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read", "read_workspace", `{"path":"sample-client/SampleEvent.java"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("unsupported", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：协议字段 eventCode 的值为 9001。依据：sample-client/SampleEvent.java。未知/下一步：没有。",
			"reason":"assumed a field name from the notification number",
			"source_refs":[{"relative_path":"sample-client/SampleEvent.java","digest":"sha256:sample-event","kind":"workspace_file"}]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("insufficient", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"insufficient",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"当前来源只确认通知编号 9001，没有定义承载该编号的协议字段名。",
			"reason":"the authoritative source names the notification number but does not define the requested protocol field",
			"source_refs":[{"relative_path":"sample-client/SampleEvent.java","digest":"sha256:sample-event","kind":"workspace_file"}]
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{
				Content: "WebSocket notification 9001 triggers message edit convergence.",
				Sources: []domain.SourceRef{{
					RelativePath: "sample-client/SampleEvent.java",
					Digest:       "sha256:sample-event",
					Kind:         "workspace_file",
				}},
			}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_genuine_gap",
			Content:   "检查当前项目里通知 9001 使用的协议字段名",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.EvidenceStatus != domain.EvidenceInsufficient || model.calls != 4 {
		t.Fatalf("decision=%+v modelCalls=%d", decision, model.calls)
	}
}

func TestAgentLoopReservesPenultimateTurnForStructuralEvidenceRead(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"确认 sampleContent 字符串的 JSON 具体格式",
			"entry_points":["request.go","docs/guide.md"],
			"symbols":["sampleContent"],
			"tools":["read_workspace"],
			"stop_conditions":["读到字段声明和具体 JSON 示例"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-request", "read_workspace", `{"path":"request.go"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-guide", "read_workspace", `{"path":"docs/guide.md"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：sampleContent 是字符串形式的 JSON，具体为 {\"content\":\"sample value\"}。依据：request.go 的字段声明和 docs/guide.md 的当前示例。未知/下一步：没有。",
			"reason":"the reserved evidence-completion read supplied the concrete shape",
			"source_refs":[
				{"relative_path":"request.go","digest":"sha256:request","kind":"workspace_file"},
				{"relative_path":"docs/guide.md","digest":"sha256:guide","kind":"workspace_file"}
			]
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, arguments json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return agenttools.Execution{}, err
			}
			switch input.Path {
			case "request.go":
				return agenttools.Execution{
					Content: `type SampleRequest struct { sampleContent string }
var unrelatedPayload = {"other":"value"}`,
					Sources: []domain.SourceRef{{
						RelativePath: "request.go",
						Digest:       "sha256:request",
						Kind:         "workspace_file",
					}},
				}, nil
			case "docs/guide.md":
				return agenttools.Execution{
					Content: `sampleContent example: {"content":"sample value"}`,
					Sources: []domain.SourceRef{{
						RelativePath: "docs/guide.md",
						Digest:       "sha256:guide",
						Kind:         "workspace_file",
					}},
				}, nil
			default:
				return agenttools.Execution{}, fmt.Errorf("unexpected path %q", input.Path)
			}
		}),
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			t.Fatal("broad search must not be exposed during structural evidence completion")
			return agenttools.Execution{}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_structural_final_two",
			Content:   "sampleContent 字符串的 JSON 具体格式是什么？",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 {
		t.Fatalf("decision=%+v modelCalls=%d", decision, model.calls)
	}
	if got := strings.Join(model.toolNames[2], ","); got != "read_workspace" {
		t.Fatalf("penultimate tools=%q want=read_workspace", got)
	}
	if got := strings.Join(model.toolNames[3], ","); got != "submit_decision" {
		t.Fatalf("final tools=%q want=submit_decision", got)
	}
	if !messagesContain(model.inputs[2], "exactly one targeted read_workspace") {
		t.Fatalf("penultimate input missing evidence-completion prompt: %+v", model.inputs[2])
	}
}

func TestAgentLoopRecoversStructuralEvidenceBeforeEarlyInsufficientDecision(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"确认 sampleContent 字符串的 JSON 具体格式",
			"entry_points":["request.go"],
			"symbols":["sampleContent"],
			"tools":["read_workspace","search_workspace"],
			"stop_conditions":["读到字段声明和具体 JSON 示例"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-request", "read_workspace", `{"path":"request.go"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("early-insufficient", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"insufficient",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"当前证据不足。",
			"reason":"stop before bounded structural recovery"
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("search-structure", "search_workspace", `{"query":"sampleContent"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-guide", "read_workspace", `{"path":"docs/guide.md"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：sampleContent 是字符串形式的 JSON，具体为 {\"content\":\"sample value\"}。依据：request.go 的字段声明和 docs/guide.md 的当前示例。未知/下一步：没有。",
			"reason":"bounded structural recovery found and read the exact field example",
			"source_refs":[
				{"relative_path":"request.go","digest":"sha256:request","kind":"workspace_file"},
				{"relative_path":"docs/guide.md","digest":"sha256:guide","kind":"workspace_file"}
			]
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, arguments json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return agenttools.Execution{}, err
			}
			switch input.Path {
			case "request.go":
				return agenttools.Execution{
					Content: `type SampleRequest struct { sampleContent string }`,
					Sources: []domain.SourceRef{{
						RelativePath: "request.go",
						Digest:       "sha256:request",
						Kind:         "workspace_file",
					}},
				}, nil
			case "docs/guide.md":
				return agenttools.Execution{
					Content: `sampleContent example: {"content":"sample value"}`,
					Sources: []domain.SourceRef{{
						RelativePath: "docs/guide.md",
						Digest:       "sha256:guide",
						Kind:         "workspace_file",
					}},
				}, nil
			default:
				return agenttools.Execution{}, fmt.Errorf("unexpected path %q", input.Path)
			}
		}),
		testTool("search_workspace", func(_ context.Context, arguments json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return agenttools.Execution{}, err
			}
			if input.Query != "sampleContent" {
				return agenttools.Execution{}, fmt.Errorf("unexpected query %q", input.Query)
			}
			return agenttools.Execution{Content: `{
				"results":[
					{
						"source":{"relative_path":"request.go","digest":"sha256:request","kind":"workspace_search"},
						"snippet":"private String sampleContent;"
					},
					{
						"source":{"relative_path":"docs/guide.md","digest":"sha256:guide-search","kind":"workspace_search"},
						"snippet":"sampleContent example: {\"content\":\"sample value\"}"
					},
					{
						"source":{"relative_path":"docs/unrelated.md","digest":"sha256:unrelated","kind":"workspace_search"},
						"snippet":"otherPayload example: {\"wrong\":true}"
					}
				],
				"truncated":false,
				"files_scanned":12,
				"directories_scanned":3
			}`}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 7,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_structural_early_recovery",
			Content:   "sampleContent 字符串的 JSON 具体格式是什么？",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 6 {
		t.Fatalf("decision=%+v modelCalls=%d", decision, model.calls)
	}
	if got := strings.Join(model.toolNames[2], ","); got != "search_workspace" {
		t.Fatalf("structural search tools=%q want=search_workspace", got)
	}
	if got := strings.Join(model.toolNames[3], ","); got != "search_workspace" {
		t.Fatalf("structural retry tools=%q want=search_workspace", got)
	}
	if got := strings.Join(model.toolNames[4], ","); got != "read_workspace" {
		t.Fatalf("structural read tools=%q want=read_workspace", got)
	}
	if !messagesContain(model.inputs[2], "exact field-name search") {
		t.Fatalf("structural search prompt missing: %+v", model.inputs[2])
	}
	if !trajectoryContains(trajectory, "tool submit_decision is not available") {
		t.Fatalf("early insufficient decision was not rejected: %+v", trajectory)
	}
	readPrompt := model.inputs[4][len(model.inputs[4])-1].Content
	if !strings.Contains(readPrompt, "docs/guide.md") ||
		strings.Contains(readPrompt, "docs/unrelated.md") {
		t.Fatalf("structural read candidates are not filtered: %s", readPrompt)
	}
}

func TestAgentLoopStructuralRecoveryDoesNotRequireEarlierPlan(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-request", "read_workspace", `{"path":"request.go"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("search-structure", "search_workspace", `{"query":"sampleContent"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-guide", "read_workspace", `{"path":"docs/guide.md"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：sampleContent 是字符串形式的 JSON，具体为 {\"content\":\"sample value\"}。依据：request.go 的字段声明和 docs/guide.md 的当前示例。未知/下一步：没有。",
			"reason":"runtime-selected structural recovery completed without a prior broad-search plan",
			"source_refs":[
				{"relative_path":"request.go","digest":"sha256:request","kind":"workspace_file"},
				{"relative_path":"docs/guide.md","digest":"sha256:guide","kind":"workspace_file"}
			]
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, arguments json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return agenttools.Execution{}, err
			}
			switch input.Path {
			case "request.go":
				return agenttools.Execution{
					Content: "type SampleRequest struct { sampleContent string }",
					Sources: []domain.SourceRef{{
						RelativePath: "request.go",
						Digest:       "sha256:request",
						Kind:         "workspace_file",
					}},
				}, nil
			case "docs/guide.md":
				return agenttools.Execution{
					Content: `sampleContent example: {"content":"sample value"}`,
					Sources: []domain.SourceRef{{
						RelativePath: "docs/guide.md",
						Digest:       "sha256:guide",
						Kind:         "workspace_file",
					}},
				}, nil
			default:
				return agenttools.Execution{}, fmt.Errorf("unexpected path %q", input.Path)
			}
		}),
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: `{"results":[{
				"source":{"relative_path":"docs/guide.md","digest":"sha256:guide-search","kind":"workspace_search"},
				"snippet":"sampleContent example: {\"content\":\"sample value\"}"
			}]}`}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 5,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_structural_without_plan",
			Content:   "sampleContent 字符串的 JSON 具体格式是什么？",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply {
		t.Fatalf("decision=%+v", decision)
	}
	if trajectoryContains(trajectory, "requires submit_investigation_plan") {
		t.Fatalf("runtime-selected recovery was blocked by plan gate: %+v", trajectory)
	}
}

func TestAgentLoopStructuralRecoveryBypassesGenericSourceLessSearchLimit(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("plan", "submit_investigation_plan", `{
			"question":"确认 sampleContent 字符串的 JSON 具体格式",
			"entry_points":["request.go"],
			"symbols":["sampleContent"],
			"tools":["read_workspace","search_workspace"],
			"stop_conditions":["读到字段声明和具体 JSON 示例"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("missing-1", "search_workspace", `{"query":"missingOne"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-1", "read_workspace", `{"path":"one.go"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("missing-2", "search_workspace", `{"query":"missingTwo"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-2", "read_workspace", `{"path":"two.go"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("missing-3", "search_workspace", `{"query":"missingThree"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-request", "read_workspace", `{"path":"request.go"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("search-structure", "search_workspace", `{"query":"sampleContent"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("read-guide", "read_workspace", `{"path":"docs/guide.md"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"evidence_status":"verified",
			"relevance_confidence":0.98,
			"reply_confidence":0.96,
			"risk":"low",
			"reply_text":"结论：sampleContent 是字符串形式的 JSON，具体为 {\"content\":\"sample value\"}。依据：request.go 的字段声明和 docs/guide.md 的当前示例。未知/下一步：没有。",
			"reason":"dedicated structural recovery remained available",
			"source_refs":[
				{"relative_path":"request.go","digest":"sha256:request","kind":"workspace_file"},
				{"relative_path":"docs/guide.md","digest":"sha256:guide","kind":"workspace_file"}
			]
		}`)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("search_workspace", func(_ context.Context, arguments json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return agenttools.Execution{}, err
			}
			searchCalls++
			if input.Query != "sampleContent" {
				return agenttools.Execution{
					Content: fmt.Sprintf(`{"results":[],"query":%q}`, input.Query),
				}, nil
			}
			return agenttools.Execution{
				Content: `{"results":[{
					"source":{"relative_path":"docs/guide.md","digest":"sha256:guide-search","kind":"workspace_search"},
					"snippet":"sampleContent example: {\"content\":\"sample value\"}"
				}]}`,
				Sources: []domain.SourceRef{{
					RelativePath: "docs/guide.md",
					Digest:       "sha256:guide-search",
					Kind:         "workspace_search",
				}},
			}, nil
		}),
		testTool("read_workspace", func(_ context.Context, arguments json.RawMessage) (agenttools.Execution, error) {
			var input struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return agenttools.Execution{}, err
			}
			content := map[string]string{
				"one.go":        "package sample\nconst one = 1",
				"two.go":        "package sample\nconst two = 2",
				"request.go":    "type SampleRequest struct { sampleContent string }",
				"docs/guide.md": `sampleContent example: {"content":"sample value"}`,
			}[input.Path]
			if content == "" {
				return agenttools.Execution{}, fmt.Errorf("unexpected path %q", input.Path)
			}
			digest := map[string]string{
				"one.go":        "sha256:one",
				"two.go":        "sha256:two",
				"request.go":    "sha256:request",
				"docs/guide.md": "sha256:guide",
			}[input.Path]
			return agenttools.Execution{
				Content: content,
				Sources: []domain.SourceRef{{
					RelativePath: input.Path,
					Digest:       digest,
					Kind:         "workspace_file",
				}},
			}, nil
		}),
		SubmitInvestigationPlanDefinition(),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 11, MaxToolCalls: 12,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_structural_after_generic_search_limit",
			Content:   "sampleContent 字符串的 JSON 具体格式是什么？",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply || searchCalls != 4 {
		t.Fatalf("decision=%+v searchCalls=%d", decision, searchCalls)
	}
	if trajectoryContains(
		trajectory,
		"search_workspace is exhausted for this work item",
	) {
		t.Fatalf("dedicated structural recovery was blocked: %+v", trajectory)
	}
}

func TestAgentLoopTerminalOnlyPromptAllowsOneCorrection(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read_1", "read_workspace", `{"path":"account.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read_over_budget", "read_workspace", `{"path":"error.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("ignored_old_tool", "search_workspace", `{"query":"10005"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.94,
			"reply_confidence":0.88,
			"risk":"low",
			"reply_text":"我核对了删号请求实现，当前能确认请求会携带账号标识；10005 的服务端含义仍需对照错误码定义，不能直接猜。",
			"reason":"bounded investigation completed with explicit unknown"
		}`)}),
	}}
	readCalls := 0
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			readCalls++
			return agenttools.Execution{Content: "delete account request"}, nil
		}),
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			t.Fatal("terminal-only old tool was executed")
			return agenttools.Execution{}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 20, MaxToolCalls: 1,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{MessageID: "om_terminal_correction", Content: "@Owner 看看删号 10005"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 || readCalls != 1 {
		t.Fatalf("decision=%+v modelCalls=%d readCalls=%d", decision, model.calls, readCalls)
	}
	for _, inputIndex := range []int{2, 3} {
		if !messagesContain(model.inputs[inputIndex], "Only submit_decision is available now") {
			t.Fatalf("model input %d missing terminal-only system prompt: %+v", inputIndex, model.inputs[inputIndex])
		}
	}
}

func TestTerminalOnlyPromptCarriesFailureAndSafeDowngradeChoices(t *testing.T) {
	got := terminalOnlyPrompt(2, 3, terminalRepairContext{
		LastFailure:     "quality_gate: reply omitted the remaining unknown",
		CompletedChecks: []string{"read_workspace service/message.go"},
		Unknowns:        []string{"production rollout state"},
	})
	for _, want := range []string{
		"quality_gate",
		"read_workspace service/message.go",
		"production rollout state",
		"complete",
		"partial",
		"clarification",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("terminal prompt missing %q: %s", want, got)
		}
	}
}

func TestAgentLoopStopsAfterThreeIgnoredTerminalOnlyAttempts(t *testing.T) {
	model := &scriptedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read_1", "read_workspace", `{"path":"account.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("read_over_budget", "read_workspace", `{"path":"error.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("ignored_1", "search_workspace", `{"query":"10005 one"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("ignored_2", "search_workspace", `{"query":"10005 two"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("ignored_3", "search_workspace", `{"query":"10005 three"}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		testTool("read_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			return agenttools.Execution{Content: "delete account request"}, nil
		}),
		testTool("search_workspace", func(_ context.Context, _ json.RawMessage) (agenttools.Execution, error) {
			t.Fatal("terminal-only old tool was executed")
			return agenttools.Execution{}, nil
		}),
		SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = (AgentLoop{
		Model: model, Tools: registry, MaxTurns: 20, MaxToolCalls: 1,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{MessageID: "om_terminal_failure", Content: "@Owner 看看删号 10005"},
	})
	if err == nil || !strings.Contains(err.Error(), "terminal decision after 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	problem, ok := errs.ProblemOf(err)
	if !ok || problem.Subtype != errs.SubtypeModelNonConvergence {
		t.Fatalf("problem=%+v", problem)
	}
	if model.calls != 4 {
		t.Fatalf("model calls=%d", model.calls)
	}
}

func TestCompactMessagesCreatesEvidenceCheckpointWithinBudget(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("初始上下文", 2000)),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("old", "read_workspace", `{"path":"old.go"}`)}),
		schema.ToolMessage(
			`{"ok":true,"content":"`+strings.Repeat("旧工具结果", 2000)+`","sources":[{"relative_path":"agent/runtime/loop.go","digest":"sha256:abc","kind":"production"}],"receipt":{"action":"read"}}`,
			"old",
			schema.WithToolName("read_workspace"),
		),
		schema.AssistantMessage("", []schema.ToolCall{toolCall("recent", "search_workspace", `{"query":"recent"}`)}),
		schema.ToolMessage("recent evidence", "recent"),
	}
	result := compactMessages(messages, 12*1024, 0.80)
	if messageBytes(result.Messages) > 12*1024 {
		t.Fatalf("compacted bytes=%d", messageBytes(result.Messages))
	}
	if !result.Compacted || result.ReplacedMessages == 0 {
		t.Fatalf("result=%+v", result)
	}
	if result.Messages[len(result.Messages)-1].Content != "recent evidence" {
		t.Fatalf("recent result changed: %q", result.Messages[len(result.Messages)-1].Content)
	}
	var checkpoint string
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "context_checkpoint") {
			checkpoint = message.Content
		}
		if !utf8.ValidString(message.Content) {
			t.Fatal("compaction produced invalid UTF-8")
		}
	}
	for _, want := range []string{"agent/runtime/loop.go", "sha256:abc", "read_workspace"} {
		if !strings.Contains(checkpoint, want) {
			t.Fatalf("checkpoint missing %q: %s", want, checkpoint)
		}
	}
}

func TestCompactMessagesPreservesParallelToolProtocolUnit(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("请核对当前实现", 500)),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("list", "list_workspace", `{"path":"sample-project/sample-module"}`),
			toolCall("search", "search_code_symbols", `{"query":"SampleRequest"}`),
		}),
		schema.ToolMessage(
			strings.Repeat("bounded directory listing\n", 500),
			"list",
			schema.WithToolName("list_workspace"),
		),
		schema.ToolMessage(
			`{"ok":false,"error":"exact repository scope requires path-bounded tools"}`,
			"search",
			schema.WithToolName("search_code_symbols"),
		),
	}

	result := compactMessages(messages, 4*1024, 0.80)
	if !result.Compacted {
		t.Fatalf("result=%+v", result)
	}
	assertValidToolProtocol(t, result.Messages)
	if !messageHasToolCalls(result.Messages, "list", "search") {
		t.Fatalf("parallel assistant tool call was not preserved: %+v", result.Messages)
	}
}

func TestCompactMessagesCheckpointPreservesEveryParallelCall(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("核对两个入口", 400)),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("first", "read_workspace", `{"path":"first.go"}`),
			toolCall("second", "read_workspace", `{"path":"second.go"}`),
		}),
		schema.ToolMessage(
			`{"ok":true,"content":"first evidence"}`,
			"first",
			schema.WithToolName("read_workspace"),
		),
		schema.ToolMessage(
			`{"ok":true,"content":"second evidence"}`,
			"second",
			schema.WithToolName("read_workspace"),
		),
		schema.AssistantMessage("recent plain-text correction", nil),
		schema.SystemMessage("plain assistant text is not accepted"),
	}

	result := compactMessages(messages, 2*1024, 0.80)
	var checkpoint string
	for _, message := range result.Messages {
		if message != nil && strings.Contains(message.Content, "context_checkpoint") {
			checkpoint = message.Content
		}
	}
	for _, want := range []string{
		`"tool_call_id":"first"`,
		`"tool_call_id":"second"`,
		`first.go`,
		`second.go`,
	} {
		if !strings.Contains(checkpoint, want) {
			t.Fatalf("checkpoint missing %q: %s", want, checkpoint)
		}
	}
}

func TestCompactMessagesBoundsOversizedToolArguments(t *testing.T) {
	arguments := fmt.Sprintf(`{"path":"sample-project/sample-module","padding":%q}`, strings.Repeat("x", 12*1024))
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("请核对当前实现"),
		schema.AssistantMessage("", []schema.ToolCall{
			toolCall("large", "list_workspace", arguments),
		}),
		schema.ToolMessage("bounded result", "large", schema.WithToolName("list_workspace")),
	}

	result := compactMessages(messages, 2*1024, 0.80)
	if got := messageBytes(result.Messages); got > 2*1024 {
		t.Fatalf("compacted bytes=%d", got)
	}
	var compactedArguments string
	for _, message := range result.Messages {
		if message != nil && len(message.ToolCalls) == 1 {
			compactedArguments = message.ToolCalls[0].Function.Arguments
		}
	}
	if !json.Valid([]byte(compactedArguments)) ||
		!strings.Contains(compactedArguments, `"compacted":true`) ||
		!strings.Contains(compactedArguments, `"digest"`) {
		t.Fatalf("compacted arguments=%q", compactedArguments)
	}
}

func TestCompactMessagesBoundsAccumulatedModerateToolArguments(t *testing.T) {
	calls := make([]schema.ToolCall, 0, 16)
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("核对并行入口", 300)),
	}
	for index := range 16 {
		callID := fmt.Sprintf("moderate-%d", index)
		arguments := fmt.Sprintf(
			`{"path":"entry-%d.go","padding":%q}`,
			index,
			strings.Repeat("x", 240),
		)
		calls = append(calls, toolCall(callID, "read_workspace", arguments))
	}
	messages = append(messages, schema.AssistantMessage("", calls))
	for index := range 16 {
		callID := fmt.Sprintf("moderate-%d", index)
		messages = append(messages, schema.ToolMessage(
			"ok",
			callID,
			schema.WithToolName("read_workspace"),
		))
	}

	result := compactMessages(messages, 4*1024, 0.80)
	if result.Overflow || messageBytes(result.Messages) > 4*1024 {
		t.Fatalf("result=%+v bytes=%d", result, messageBytes(result.Messages))
	}
	assertValidToolProtocol(t, result.Messages)
	for _, message := range result.Messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if len(call.Function.Arguments) > 160 ||
				!json.Valid([]byte(call.Function.Arguments)) {
				t.Fatalf("call %q arguments were not bounded: %q", call.ID, call.Function.Arguments)
			}
		}
	}
}

func TestCompactMessagesRejectsCheckpointThatCannotPreserveBindings(t *testing.T) {
	calls := make([]schema.ToolCall, 0, 12)
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage(strings.Repeat("核对旧并行调用", 800)),
	}
	for index := range 12 {
		callID := fmt.Sprintf("%s-%d", strings.Repeat("binding", 20), index)
		calls = append(calls, toolCall(
			callID,
			"read_workspace",
			fmt.Sprintf(`{"path":"entry-%d.go"}`, index),
		))
	}
	messages = append(messages, schema.AssistantMessage("", calls))
	for _, call := range calls {
		messages = append(messages, schema.ToolMessage(
			"ok",
			call.ID,
			schema.WithToolName(call.Function.Name),
		))
	}
	messages = append(
		messages,
		schema.AssistantMessage("recent correction", nil),
		schema.SystemMessage("submit a structured decision"),
	)

	result := compactMessages(messages, 4*1024, 0.80)
	if !result.Overflow {
		t.Fatalf("checkpoint silently discarded bindings: %+v", result)
	}
}

func assertValidToolProtocol(t *testing.T, messages []*schema.Message) {
	t.Helper()
	pending := map[string]bool{}
	for index, message := range messages {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.Assistant:
			if len(pending) != 0 {
				t.Fatalf("assistant message %d arrived before tool results completed: %+v", index, pending)
			}
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
		case schema.Tool:
			if !pending[message.ToolCallID] {
				t.Fatalf("tool result %q at %d has no pending assistant call", message.ToolCallID, index)
			}
			delete(pending, message.ToolCallID)
		default:
			if len(pending) != 0 {
				t.Fatalf("message role %q at %d interrupted tool results: %+v", message.Role, index, pending)
			}
		}
	}
	if len(pending) != 0 {
		t.Fatalf("missing tool results for calls: %+v", pending)
	}
}

func messageHasToolCalls(messages []*schema.Message, ids ...string) bool {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		found := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			found[call.ID] = true
		}
		all := true
		for id := range want {
			all = all && found[id]
		}
		if all {
			return true
		}
	}
	return false
}

func TestMultimodalPayloadCountsTowardBudgetAndExpiresAfterFirstTurn(
	t *testing.T,
) {
	imageURL := "data:image/png;base64," + strings.Repeat("A", 4096)
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "请分析图片"},
				{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{URL: &imageURL},
					},
				},
			},
		},
	}
	if got := messageBytes(messages); got < len(imageURL) {
		t.Fatalf("multimodal payload bytes=%d, want at least %d", got, len(imageURL))
	}
	expired, removed := expireEphemeralImages(messages)
	if removed != 1 {
		t.Fatalf("removed images=%d", removed)
	}
	expiredText := expired[1].UserInputMultiContent[1].Text
	if messageBytes(expired) >= messageBytes(messages) ||
		!strings.Contains(expiredText, "ephemeral image") {
		t.Fatalf(
			"expired bytes=%d original=%d messages=%+v",
			messageBytes(expired),
			messageBytes(messages),
			expired,
		)
	}
	sourceURL := messages[1].UserInputMultiContent[1].Image.URL
	if sourceURL == nil || *sourceURL != imageURL {
		t.Fatalf("source messages were mutated: %+v", messages[1])
	}
}

func TestModelBudgetPromptIncludesTurnAndContextUrgency(t *testing.T) {
	got := modelRunProgressPrompt(runBudget{
		CurrentTurn:      121,
		MaxTurns:         150,
		ToolCalls:        12,
		MaxToolCalls:     16,
		ContextBytes:     54_400,
		MaxContextBytes:  65_536,
		Compacted:        true,
		ReplacedMessages: 8,
	})
	for _, want := range []string{
		"121", "150", "29",
		"12", "16", "4 remaining",
		"54400", "65536", "11136",
		"83%", "8", "urgent",
	} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Fatalf("budget prompt missing %q: %s", want, got)
		}
	}
}

func TestModelBudgetPromptTreatsNearlyExhaustedToolCallsAsUrgent(t *testing.T) {
	got := modelRunProgressPrompt(runBudget{
		CurrentTurn:     1,
		MaxTurns:        150,
		ToolCalls:       15,
		MaxToolCalls:    16,
		ContextBytes:    4_096,
		MaxContextBytes: 65_536,
	})
	for _, want := range []string{
		"15 of 16 investigation calls used",
		"1 remaining",
		"Urgency: urgent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("budget prompt missing %q: %s", want, got)
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
		SimpleMaxTurns: 20, MaxToolCalls: 10, MaxNoProgress: 3, MaxRepeatedCalls: 100,
	}).Decide(context.Background(), agentcontext.Bundle{
		WorkKind: domain.WorkKindSimpleQuestion,
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

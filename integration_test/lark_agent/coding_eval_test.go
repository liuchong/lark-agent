package larkagent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/agent/workspace"
)

type codingEvalModel struct {
	responses []*schema.Message
	calls     int
	inputs    [][]*schema.Message
}

func (m *codingEvalModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if m.calls >= len(m.responses) {
		return nil, errors.New("unexpected model call")
	}
	response := m.responses[m.calls]
	m.calls++
	return response, nil
}

func (m *codingEvalModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

func TestCodingQuestionReplayUsesPlanCodeSearchReadAndSources(t *testing.T) {
	root := t.TempDir()
	source := `package service

func register() {
	router.POST("/api/sample/items", handler)
}

func handler() {
	sampleStore.Collection("sample_items").Find()
}`
	if err := os.MkdirAll(filepath.Join(root, "service"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "router.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(source))
	digest := fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:8]))
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(append(
		agenttools.CodeIndexDefinitions(scope, nil),
		append(agenttools.WorkspaceDefinitions(scope),
			agentruntime.SubmitInvestigationPlanDefinition(),
			agentruntime.SubmitDecisionDefinition(),
		)...,
	)...)
	if err != nil {
		t.Fatal(err)
	}
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("plan", "submit_investigation_plan", `{
			"question":"POST /api/sample/items 是否每次直接访问 SampleDB",
			"entry_points":["service/router.go"],
			"symbols":["sample items handler"],
			"tools":["search_code_symbols","read_workspace"],
			"stop_conditions":["找到 handler 和数据库访问代码"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("search", "search_code_symbols", `{"query":"sample/items SampleDB","max_results":5}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("read", "read_workspace", `{"path":"service/router.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.9,
			"risk":"low",
			"reply_text":"结论：从当前代码看，这个 handler 会查询 SampleDB。依据：service/router.go 里注册了 /api/sample/items，并在 handler 中调用 sample store lookup。未知/下一步：还需要继续确认外层是否有缓存或限流。",
			"owner_action":"确认外层缓存或限流是否已覆盖 sample items。",
			"reason":"coding eval evidence is source backed",
			"source_refs":[{"relative_path":"service/router.go","digest":"`+digest+`","kind":"workspace_file"}]
		}`)}),
	}}
	loop := agentruntime.AgentLoop{Model: model, Tools: registry, MaxTurns: 8}
	decision, _, err := loop.Decide(context.Background(), agentcontext.Bundle{
		Event: domain.NormalizedEvent{MessageID: "om_eval", Content: "@Owner POST /api/sample/items 每次都会直接访问 SampleDB 吗？"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || len(decision.Sources) != 1 {
		t.Fatalf("decision=%+v", decision)
	}
	if !strings.Contains(decision.ReplyText, "结论") || !strings.Contains(decision.ReplyText, "依据") || !strings.Contains(decision.ReplyText, "未知/下一步") {
		t.Fatalf("reply_text=%q", decision.ReplyText)
	}
	if model.calls != 4 {
		t.Fatalf("model calls=%d", model.calls)
	}
}

func TestCodingQuestionCollectsAllRealProjectFactsBeforeConverging(t *testing.T) {
	root := t.TempDir()
	sources := map[string]string{
		"sample-project/sample-module/sample-client/modify_request.kt": `data class SampleRequest(
    val sampleContent: String,
    val expectedEditVersion: Long,
)`,
		"sample-project/sample-module/go/internal/item_flow/api.go": `func SampleOperation(req SampleRequest) (*SampleChangeResponse, error) {
    return server.SampleOperation(req)
}`,
		"sample-project/sample-module/sample-client/message_listener.kt": `fun onSampleEvent(message: Message) {
    localMessages.update(message.clientMsgID, message.sampleVersion)
}`,
	}
	digests := make(map[string]string, len(sources))
	for path, source := range sources {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(source))
		digests[path] = fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:8]))
	}
	decoy := filepath.Join(root, "Sample-Module", "message_manager.java")
	if err := os.MkdirAll(filepath.Dir(decoy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, []byte("class MessageManager {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	definitions := append(
		agenttools.WorkspaceDefinitions(scope),
		agentruntime.SubmitInvestigationPlanDefinition(),
		agentruntime.SubmitDecisionDefinition(),
	)
	definitions = append(agenttools.CodeIndexDefinitions(scope, nil), definitions...)
	registry, err := agenttools.NewRegistry(definitions...)
	if err != nil {
		t.Fatal(err)
	}
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("wrong-plan", "submit_investigation_plan", `{
			"question":"核对消息修改行为",
			"entry_points":["Sample-Module/sample-client"],
			"symbols":["SampleRequest"],
			"tools":["read_workspace"],
			"stop_conditions":["找到任意同名消息修改实现"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("plan", "submit_investigation_plan", `{
			"question":"核对 sample-project/sample-module 的请求结构、成功回调和本地收敛",
			"entry_points":[
				"sample-project/sample-module/sample-client/modify_request.kt",
				"sample-project/sample-module/go/internal/item_flow/api.go",
				"sample-project/sample-module/sample-client/message_listener.kt"
			],
			"symbols":["SampleRequest","SampleOperation","onSampleEvent"],
			"tools":["read_workspace"],
			"stop_conditions":["确认请求结构","确认成功回调语义","确认本地收敛事件"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"wrong-read",
			"read_workspace",
			`{"path":"Sample-Module/message_manager.java"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"wrong-symbol-search",
			"search_code_symbols",
			`{"query":"MessageManager","max_results":5}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read-request",
			"read_workspace",
			`{"path":"sample-project/sample-module/sample-client/modify_request.kt"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read-api",
			"read_workspace",
			`{"path":"sample-project/sample-module/go/internal/item_flow/api.go"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read-listener",
			"read_workspace",
			`{"path":"sample-project/sample-module/sample-client/message_listener.kt"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.99,
			"reply_confidence":0.95,
			"risk":"low",
			"reply_text":"结论：sampleContent 是字符串形式的消息 JSON；接口成功只表示服务端接受修改；本地状态由 onSampleEvent 推送收敛。依据：请求、接口与监听器三个生产文件。未知/下一步：没有。",
			"reason":"all requested project facts have authoritative reads",
			"source_refs":[
				{"relative_path":"sample-project/sample-module/sample-client/modify_request.kt","digest":"`+digests["sample-project/sample-module/sample-client/modify_request.kt"]+`","kind":"workspace_file"},
				{"relative_path":"sample-project/sample-module/go/internal/item_flow/api.go","digest":"`+digests["sample-project/sample-module/go/internal/item_flow/api.go"]+`","kind":"workspace_file"},
				{"relative_path":"sample-project/sample-module/sample-client/message_listener.kt","digest":"`+digests["sample-project/sample-module/sample-client/message_listener.kt"]+`","kind":"workspace_file"}
			]
		}`)}),
	}}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model: model, Tools: registry, MaxTurns: 12,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Environment: agentcontext.EnvironmentSnapshot{
			WorkspaceRoot:     "/workspace/sample-org",
			WorkspaceRealRoot: "/workspace/sample-org",
			Directory: []agentcontext.DirectoryEntry{{
				Path: "sample-project",
				Kind: "dir",
			}},
		},
		Event: domain.NormalizedEvent{
			MessageID: "om_real_project_multi_fact",
			SenderID:  "ou_owner",
			Content:   "结合上一条：Sample-Client SampleRequest 的 sampleContent 是什么结构，成功回调代表什么，本地如何收敛？",
		},
		Conversation: []domain.NormalizedEvent{
			{
				MessageID: "om_scope",
				SenderID:  "ou_owner",
				Content:   "这次只看 sample-org/sample-project/sample-module，不要看同名的旧项目。",
			},
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || len(decision.Sources) != 3 {
		t.Fatalf("decision=%+v", decision)
	}
	for _, want := range []string{"字符串形式的消息 JSON", "服务端接受修改", "onSampleEvent"} {
		if !strings.Contains(decision.ReplyText, want) {
			t.Fatalf("reply=%q missing=%q", decision.ReplyText, want)
		}
	}
	if codingEvalTrajectoryContains(trajectory, "coding evidence is complete; submit_decision is required now") {
		t.Fatalf("multi-field investigation was closed after partial evidence: %+v", trajectory)
	}
	for _, want := range []string{
		"exact workspace scope sample-project/sample-module",
		"Sample-Module/sample-client",
		"Sample-Module/message_manager.java",
		"search_code_symbols cannot guarantee exact workspace scope",
	} {
		if !codingEvalTrajectoryContains(trajectory, want) {
			t.Fatalf("trajectory missing exact-scope rejection %q: %+v", want, trajectory)
		}
	}
}

func TestContextualIntentBackendHandoffInvestigatesSampleEventInsteadOfNetwork(t *testing.T) {
	root := t.TempDir()
	apiSource := `package api

func (m *MessageApi) SampleOperation(c *gin.Context) {
	a2r.Call(c, msg.MsgClient.SampleOperation, m.Client)
}`
	rpcSource := `package msg

func (m *msgServer) SampleOperation(ctx context.Context, req *samplepb.SampleOperationReq) (*samplepb.SampleChangeResp, error) {
	if req == nil {
		return nil, errs.ErrArgs.WrapMsg("request is nil")
	}
	if _, err := m.ensureConversationAccess(ctx, actorID, req.TargetID); err != nil {
		return nil, err
	}
	sampleVersion, err := m.SampleStore.ApplySampleChange(ctx, req.TargetID, req.Seq, actorID, req.SampleContent, sampleContent.eventCode, sampleContent.sampleIDs, req.ExpectedVersion, now)
	if err != nil {
		return nil, err
	}
	return &samplepb.SampleChangeResp{ModifiedTime: now, EditVersion: sampleVersion}, nil
}`
	for path, source := range map[string]string{
		"internal/api/msg.go":        apiSource,
		"internal/rpc/msg/modify.go": rpcSource,
	} {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digestFor := func(source string) string {
		sum := sha256.Sum256([]byte(source))
		return fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:8]))
	}
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(append(
		agenttools.CodeIndexDefinitions(scope, nil),
		append(
			agenttools.WorkspaceDefinitions(scope),
			agentruntime.SubmitInvestigationPlanDefinition(),
			agentruntime.SubmitDecisionDefinition(),
		)...,
	)...)
	if err != nil {
		t.Fatal(err)
	}
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("plan", "submit_investigation_plan", `{
			"question":"生产环境示例事件返回 1408 SampleEventDisabled 的原因是什么",
			"entry_points":["internal/api/msg.go"],
			"symbols":["MessageApi.SampleOperation","msgServer.SampleOperation"],
			"tools":["search_code_symbols","read_workspace"],
			"stop_conditions":["确认当前 HTTP 和 RPC 路径并区分部署版本未知"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"SampleOperation SampleEventDisabled","max_results":10}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read-api",
			"read_workspace",
			`{"path":"internal/api/msg.go"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read-rpc",
			"read_workspace",
			`{"path":"internal/rpc/msg/modify.go"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.99,
			"reply_confidence":0.94,
			"risk":"low",
			"reply_text":"智能助手已检查当前示例事件入口和 RPC 实现：HTTP 的 SampleOperation 已转发到 msg.MsgClient.SampleOperation，RPC 会校验会话后写入消息内容，当前源码不是直接返回 1408 SampleEventDisabled。结合群里后续提到生产环境尚未上线，初步判断应先核对生产部署版本；现有证据不能把问题归因于发起人的电脑网络。以上源码结论和部署版本待核对项已通知测试负责人。",
			"owner_action":"核对生产环境部署版本是否包含示例事件 HTTP 转发改动。",
			"reason":"bounded current production source contradicts the literal network interpretation",
			"source_refs":[
				{"relative_path":"internal/api/msg.go","digest":"`+digestFor(apiSource)+`","kind":"workspace_file"},
				{"relative_path":"internal/rpc/msg/modify.go","digest":"`+digestFor(rpcSource)+`","kind":"workspace_file"}
			]
		}`)}),
	}}
	bundle := agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner", Name: "测试负责人"},
		Event: domain.NormalizedEvent{
			MessageID: "om_handoff",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 你看看吧，我电脑断线了",
		},
		Conversation: []domain.NormalizedEvent{
			{MessageID: "om_issue", SenderName: "仇亚颖", Content: "示例事件的后台服务还没上线么"},
			{MessageID: "om_mention", SenderName: "许嘉诺", Content: "@测试负责人 是不是开关没打开"},
			{MessageID: "om_image", SenderName: "仇亚颖", Content: "[图片：1408 SampleEventDisabled message edit is temporarily unavailable]"},
			{MessageID: "om_handoff", SenderName: "许嘉诺", Content: "@测试负责人 你看看吧，我电脑断线了"},
		},
		WorkKind:    domain.WorkKindDirectMention,
		TaskClass:   domain.TaskClassCoding,
		TaskSummary: "调查生产环境示例事件返回 1408 SampleEventDisabled",
	}
	decision, _, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 8,
	}).Decide(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || len(decision.Sources) != 2 {
		t.Fatalf("decision=%+v", decision)
	}
	for _, want := range []string{
		"HTTP 的 SampleOperation 已转发",
		"生产部署版本",
		"不能把问题归因于发起人的电脑网络",
		"已通知测试负责人",
	} {
		if !strings.Contains(decision.ReplyText, want) {
			t.Fatalf("reply=%q missing=%q", decision.ReplyText, want)
		}
	}
}

func TestIndependentVerifiableBasicQuestionReturnsExactCalculation(t *testing.T) {
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"submit",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.99,
				"reply_confidence":0.99,
				"risk":"low",
				"reply_text":"17 × 23 = 391。",
				"reason":"direct arithmetic calculation"
			}`,
		)}),
	}}
	registry, err := agenttools.NewRegistry(
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 3,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_basic_calculation",
			SenderID:  "ou_owner",
			Content:   "17 × 23 等于多少？",
		},
		WorkKind: domain.WorkKindSimpleQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply ||
		decision.ReplyText != "17 × 23 = 391。" {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestFalsePremiseTrapDoesNotInventMissingProductionSymbol(t *testing.T) {
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"plan",
			"submit_investigation_plan",
			`{
				"question":"解释 NormalizeContentType 为什么固定返回 image/avif",
				"symbols":["NormalizeContentType"],
				"tools":["search_code_symbols"],
				"stop_conditions":["确认符号是否存在"]
			}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"NormalizeContentType","max_results":5}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"submit",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.99,
				"reply_confidence":0.90,
				"risk":"low",
				"evidence_status":"insufficient",
				"reply_text":"没有找到 NormalizeContentType，无法确认它存在或具有题目描述的返回行为。",
				"reason":"the named production symbol was not found"
			}`,
		)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(
				context.Context,
				json.RawMessage,
			) (agenttools.Execution, error) {
				searchCalls++
				return agenttools.Execution{Content: `{"matches":[]}`}, nil
			},
		},
		agentruntime.SubmitInvestigationPlanDefinition(),
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_false_premise",
			SenderID:  "ou_owner",
			Content:   "NormalizeContentType 固定返回 image/avif，解释一下原因。",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply ||
		searchCalls != 1 ||
		!strings.Contains(decision.ReplyText, "当前证据不足") ||
		!strings.Contains(decision.ReplyText, "不会据此推测") {
		t.Fatalf("decision=%+v search_calls=%d", decision, searchCalls)
	}
}

func TestFalsePremiseRecoversFromParallelPolicyErrorsAndMalformedPlan(t *testing.T) {
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("symbol", "search_code_symbols", `{"query":"ThisFunctionDefinitelyDoesNotExist20260730"}`),
			codingEvalToolCall("shell_1", "shell", `{"command":"grep -rn ThisFunctionDefinitelyDoesNotExist20260730 Sample-Module"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("shell_2", "shell", `{"command":"grep -rn ThisFunctionDefinitelyDoesNotExist20260730 Sample-Module --include=*.go"}`),
			codingEvalToolCall("search_before_plan", "search_workspace", `{"query":"ThisFunctionDefinitelyDoesNotExist20260730","path":"Sample-Module"}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("bad_plan", "submit_investigation_plan", `{
				"plan":"Search the Sample-Module tree for the exact symbol and stop after one bounded fallback."
			}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("plan", "submit_investigation_plan", `{
				"question":"Does ThisFunctionDefinitelyDoesNotExist20260730 exist?",
				"entry_points":["Sample-Module"],
				"symbols":["ThisFunctionDefinitelyDoesNotExist20260730"],
				"tools":["search_workspace"],
				"stop_conditions":["One exact bounded search returns no matches"]
			}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("search", "search_workspace", `{
					"query":"ThisFunctionDefinitelyDoesNotExist20260730",
					"path":"Sample-Module"
				}`),
			codingEvalToolCall("explore", "explore_workspace", `{
					"focus":"Sample-Module exact symbol",
					"queries":[
						"ThisFunctionDefinitelyDoesNotExist20260730",
						"func ThisFunctionDefinitelyDoesNotExist20260730",
						"DefinitelyDoesNotExist20260730"
					]
				}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("submit", "submit_decision", `{
				"decision":"reply",
				"relevance_confidence":0.99,
				"reply_confidence":0.92,
				"risk":"low",
				"evidence_status":"insufficient",
				"reply_text":"没有找到 ThisFunctionDefinitelyDoesNotExist20260730。我检查了当前 Workspace 的 Sample-Module 子目录，并对该完整名称做了精确搜索；搜索没有返回匹配项，因此不会推测它的实现。",
				"reason":"the exact bounded workspace search returned no matches"
			}`),
		}),
	}}
	searchCalls := 0
	exploreCalls := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{Content: `{
					"index_available":false,
					"query":"ThisFunctionDefinitelyDoesNotExist20260730",
					"fallback":{
						"results":null,
						"truncated":true,
						"files_scanned":2000,
						"directories_scanned":451
					}
				}`}, nil
			},
		},
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "shell"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{}, errors.New(
					"unbounded shell search is not allowed; use bounded code-search tools",
				)
			},
		},
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_workspace"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				searchCalls++
				return agenttools.Execution{Content: `{
					"results":[],
					"truncated":false,
					"files_scanned":124,
					"directories_scanned":31
				}`}, nil
			},
		},
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "explore_workspace"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				exploreCalls++
				return agenttools.Execution{Content: `{
					"queries":[
						{
							"query":"ThisFunctionDefinitelyDoesNotExist20260730",
							"results":[],
							"truncated":true,
							"files_scanned":1200,
							"directories_scanned":239
						},
						{
							"query":"func ThisFunctionDefinitelyDoesNotExist20260730",
							"results":[],
							"truncated":true,
							"files_scanned":1200,
							"directories_scanned":239
						},
						{
							"query":"DefinitelyDoesNotExist20260730",
							"results":[],
							"truncated":true,
							"files_scanned":1200,
							"directories_scanned":239
						}
					]
				}`}, nil
			},
		},
		agentruntime.SubmitInvestigationPlanDefinition(),
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:            model,
		Tools:            registry,
		MaxTurns:         10,
		CodingMaxTurns:   10,
		MaxNoProgress:    3,
		MaxRepeatedCalls: 100,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_false_premise_recovery",
			SenderID:  "ou_owner",
			Content:   "请在 Sample-Module 中核对 ThisFunctionDefinitelyDoesNotExist20260730 是否存在，不要推测。",
		},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply ||
		model.calls != 6 ||
		searchCalls != 1 ||
		exploreCalls != 1 {
		t.Fatalf(
			"decision=%+v model_calls=%d search_calls=%d explore_calls=%d",
			decision,
			model.calls,
			searchCalls,
			exploreCalls,
		)
	}
	for _, want := range []string{
		"在本次有界检查范围内没有找到匹配项",
		"ThisFunctionDefinitelyDoesNotExist20260730",
		"扫描了 124 个文件",
		"扫描了 1200 个文件",
		"扫描完整",
	} {
		if !strings.Contains(decision.ReplyText, want) {
			t.Fatalf("reply missing %q: %s", want, decision.ReplyText)
		}
	}
	for _, want := range []string{
		"unbounded shell search is not allowed",
		"coding investigation requires submit_investigation_plan",
		"question, entry_points, symbols, tools, and stop_conditions",
	} {
		if !codingEvalTrajectoryContains(trajectory, want) {
			t.Fatalf("trajectory missing %q: %+v", want, trajectory)
		}
	}
}

func TestCodingQuestionWithCompleteEvidenceIsPromptedToConvergeBeforeFinalTurns(t *testing.T) {
	root := t.TempDir()
	source := `package content_type

func GetType(value string) string {
	return "image/jpeg"
}`
	if err := os.WriteFile(filepath.Join(root, "content_type.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(source))
	digest := fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:8]))
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(append(
		agenttools.CodeIndexDefinitions(scope, nil),
		append(agenttools.WorkspaceDefinitions(scope),
			agentruntime.SubmitInvestigationPlanDefinition(),
			agentruntime.SubmitDecisionDefinition(),
		)...,
	)...)
	if err != nil {
		t.Fatal(err)
	}
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("plan", "submit_investigation_plan", `{
			"question":"GetType 的直接返回行为",
			"entry_points":["content_type.go"],
			"symbols":["GetType"],
			"tools":["read_workspace"],
			"stop_conditions":["读取 GetType 定义"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("candidate", "search_code_symbols", `{"query":"GetType","max_results":5}`)}),
		schema.AssistantMessage("", []schema.ToolCall{
			codingEvalToolCall("read", "read_workspace", `{"path":"content_type.go"}`),
			codingEvalToolCall("late-search", "search_code_symbols", `{"query":"unrelated history","max_results":5}`),
		}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.9,
			"risk":"low",
			"reply_text":"结论：GetType 直接返回 image/jpeg。依据：content_type.go 的函数定义。未知/下一步：没有。",
			"reason":"the exact definition is sufficient for direct behavior",
			"source_refs":[{"relative_path":"content_type.go","digest":"`+digest+`","kind":"workspace_file"}]
		}`)}),
	}}
	loop := agentruntime.AgentLoop{Model: model, Tools: registry, MaxTurns: 4}
	decision, trajectory, err := loop.Decide(context.Background(), agentcontext.Bundle{
		Event:    domain.NormalizedEvent{MessageID: "om_converge", Content: "请检查 GetType 的直接返回行为"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply {
		t.Fatalf("decision=%+v", decision)
	}
	if model.calls != 4 ||
		!codingEvalMessagesContain(model.inputs[3], "Citable workspace evidence is now available") {
		t.Fatalf("model calls=%d fourth input=%+v", model.calls, model.inputs[3])
	}
	if !codingEvalTrajectoryContains(trajectory, "remaining turn") {
		t.Fatalf("late search was not rejected to preserve a correction turn: %+v", trajectory)
	}
}

func TestCodingQuestionMemorySourceDoesNotSkipFreshProductionRead(t *testing.T) {
	root := t.TempDir()
	source := `package content_type

func GetType(value string) string {
	return "image/jpeg"
}`
	if err := os.WriteFile(filepath.Join(root, "content_type.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(source))
	digest := fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:8]))
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttools.NewRegistry(append(
		agenttools.WorkspaceDefinitions(scope),
		agentruntime.SubmitDecisionDefinition(),
	)...)
	if err != nil {
		t.Fatal(err)
	}
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read",
			"read_workspace",
			`{"path":"content_type.go"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.95,
			"reply_confidence":0.9,
			"risk":"low",
			"reply_text":"结论：GetType 返回 image/jpeg。依据：本轮读取了 content_type.go。",
			"reason":"fresh production read completed",
			"source_refs":[{"relative_path":"content_type.go","digest":"`+digest+`","kind":"workspace_file"}]
		}`)}),
	}}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 2,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event:    domain.NormalizedEvent{MessageID: "om_memory", Content: "请检查 GetType 的返回值"},
		WorkKind: domain.WorkKindCodingQuestion,
		Sources: []domain.SourceRef{{
			RelativePath: "memory/manual",
			Digest:       "sha256:memory",
			Kind:         "manual",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 2 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if codingEvalTrajectoryContains(trajectory, "coding evidence is complete; submit_decision is required now") {
		t.Fatalf("memory source incorrectly blocked fresh production read: %+v", trajectory)
	}
}

func TestCodingQuestionSearchCandidateDoesNotBlockAuthoritativeRead(t *testing.T) {
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"GetType","max_results":5}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("premature", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.95,
			"risk":"low",
			"reply_text":"结论：GetType 未命中时需要返回 image/png。依据：代码搜索定位到 content_type.go。",
			"reason":"search candidate was treated as enough",
			"source_refs":[{"relative_path":"content_type.go","digest":"sha256:content","kind":"workspace_file"}]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"read",
			"read_workspace",
			`{"path":"content_type.go"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.95,
			"risk":"low",
			"reply_text":"结论：GetType 未命中时返回空字符串。依据：本轮精读了 content_type.go 的完整函数体。",
			"reason":"authoritative production read completed",
			"source_refs":[{"relative_path":"content_type.go","digest":"sha256:content","kind":"workspace_file"}]
		}`)}),
	}}
	readCalls := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{
					Content: `{"matches":[{"path":"content_type.go","symbol":"GetType"}]}`,
					Sources: []domain.SourceRef{{
						RelativePath: "content_type.go",
						Digest:       "sha256:content",
						Kind:         "workspace_file",
					}},
				}, nil
			},
		},
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "read_workspace"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				readCalls++
				return agenttools.Execution{
					Content: `func GetType(value string) string { return "" }`,
					Sources: []domain.SourceRef{{
						RelativePath: "content_type.go",
						Digest:       "sha256:content",
						Kind:         "workspace_file",
					}},
				}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event:    domain.NormalizedEvent{MessageID: "om_search_candidate", SenderID: "ou_owner", Content: "请检查生产源码 GetType 未命中时返回什么"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 || readCalls != 1 {
		t.Fatalf("decision=%+v model_calls=%d read_calls=%d", decision, model.calls, readCalls)
	}
	if !codingEvalTrajectoryContains(trajectory, "authoritative production read") {
		t.Fatalf("premature search-only decision was not rejected: %+v", trajectory)
	}
}

func TestCodingQuestionInsufficientStatusCanonicalizesMixedClaim(t *testing.T) {
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"NormalizeContentType","max_results":5}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.95,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_text":"没有找到 NormalizeContentType，所以它一定会回退到 GetType。",
			"reason":"bounded exact search did not find the named symbol"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{Content: `{"matches":[]}`}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 2,
	}).Decide(context.Background(), agentcontext.Bundle{
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event:    domain.NormalizedEvent{MessageID: "om_insufficient_mixed", SenderID: "ou_owner", Content: "请检查生产源码是否存在 NormalizeContentType"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	want := "结论：当前证据不足，不能确认你询问的代码事实。\n依据：已完成有界的工作区代码定位，但没有取得足以支撑确定结论的生产源码证据。\n未知/下一步：相关符号是否存在及实际行为仍未核实，我不会据此推测。"
	if decision.ReplyText != want {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestCodingQuestionInsufficientStatusRequiresActualCodeInvestigation(t *testing.T) {
	insufficient := func(id string) *schema.Message {
		return schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(id, "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.95,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_text":"无法确认 NormalizeContentType 是否存在。",
			"reason":"insufficient evidence"
		}`)})
	}
	model := &codingEvalModel{responses: []*schema.Message{
		insufficient("premature"),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"NormalizeContentType","max_results":5}`,
		)}),
		insufficient("submit"),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				searchCalls++
				return agenttools.Execution{Content: `{"matches":[]}`}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 3,
	}).Decide(context.Background(), agentcontext.Bundle{
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event:    domain.NormalizedEvent{MessageID: "om_insufficient_without_work", SenderID: "ou_owner", Content: "请检查生产源码是否存在 NormalizeContentType"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	want := "结论：当前证据不足，不能确认你询问的代码事实。\n依据：已完成有界的工作区代码定位，但没有取得足以支撑确定结论的生产源码证据。\n未知/下一步：相关符号是否存在及实际行为仍未核实，我不会据此推测。"
	if decision.ReplyText != want || model.calls != 3 || searchCalls != 1 {
		t.Fatalf("decision=%+v model_calls=%d search_calls=%d", decision, model.calls, searchCalls)
	}
	if !codingEvalTrajectoryContains(trajectory, "requires at least one successful workspace code investigation") {
		t.Fatalf("premature insufficient decision was not rejected: %+v", trajectory)
	}
}

func TestCodingQuestionCannotDisappearIntoApproval(t *testing.T) {
	approval := func(id string) *schema.Message {
		return schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(id, "submit_decision", `{
			"decision":"request_approval",
			"relevance_confidence":0.98,
			"risk":"medium",
			"evidence_status":"insufficient",
			"reply_text":"没有找到 NormalizeContentType，需要测试负责人确认。",
			"reason":"avoid answering the coding question"
		}`)})
	}
	model := &codingEvalModel{responses: []*schema.Message{
		approval("premature_approval"),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"NormalizeContentType","max_results":5}`,
		)}),
		approval("approval_after_search"),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.95,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_text":"无法确认 NormalizeContentType 是否存在。",
			"reason":"bounded exact search produced no authoritative production read"
		}`)}),
	}}
	searchCalls := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				searchCalls++
				return agenttools.Execution{Content: `{"matches":[]}`}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event:    domain.NormalizedEvent{MessageID: "om_coding_approval", SenderID: "ou_owner", Content: "请检查生产源码是否存在 NormalizeContentType"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 4 || searchCalls != 1 {
		t.Fatalf("decision=%+v model_calls=%d search_calls=%d", decision, model.calls, searchCalls)
	}
	if !codingEvalTrajectoryContains(trajectory, "coding question cannot finish as request_approval") {
		t.Fatalf("approval bypass was not rejected: %+v", trajectory)
	}
}

func TestCodingQuestionCannotFinishWithoutReply(t *testing.T) {
	terminal := func(id, decision string) *schema.Message {
		return schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(id, "submit_decision", fmt.Sprintf(`{
			"decision":%q,
			"relevance_confidence":0.98,
			"risk":"low",
			"reason":"avoid sender-facing coding answer"
		}`, decision))})
	}
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall(
			"search",
			"search_code_symbols",
			`{"query":"NormalizeContentType","max_results":5}`,
		)}),
		terminal("record", "record"),
		terminal("notify", "notify"),
		terminal("ignore", "ignore"),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("submit", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.95,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_text":"无法确认 NormalizeContentType 是否存在。",
			"reason":"bounded exact search produced no authoritative production read"
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "search_code_symbols"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{Content: `{"matches":[]}`}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:         model,
		Tools:         registry,
		MaxTurns:      5,
		MaxNoProgress: 5,
	}).Decide(context.Background(), agentcontext.Bundle{
		User:     agentcontext.UserProfile{OpenID: "ou_owner"},
		Event:    domain.NormalizedEvent{MessageID: "om_coding_non_reply", SenderID: "ou_owner", Content: "请检查生产源码是否存在 NormalizeContentType"},
		WorkKind: domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatalf("err=%v trajectory=%+v", err, trajectory)
	}
	if decision.Kind != domain.DecisionReply || model.calls != 5 {
		t.Fatalf("decision=%+v model_calls=%d", decision, model.calls)
	}
	for kind, rejection := range map[string]string{
		"record": "coding question cannot finish as record",
		"notify": "cannot finish as notify only",
		"ignore": "coding question cannot finish as ignore",
	} {
		if !codingEvalTrajectoryContains(trajectory, rejection) {
			t.Fatalf("%s bypass was not rejected: %+v", kind, trajectory)
		}
	}
}

func TestReplyWithoutConfidenceIsRepairedInsteadOfApproved(t *testing.T) {
	model := &codingEvalModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("missing", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"risk":"low",
			"reply_text":"结论：函数不存在。依据：已完成精确符号搜索。",
			"reason":"bounded exact search found no symbol",
			"source_refs":[{"relative_path":"content_type.go","digest":"sha256:content","kind":"workspace_file"}]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("repaired", "submit_decision", `{
			"decision":"reply",
			"relevance_confidence":0.98,
			"reply_confidence":0.94,
			"risk":"low",
			"reply_text":"结论：函数不存在。依据：已完成精确符号搜索。",
			"reason":"bounded exact search found no symbol",
			"source_refs":[{"relative_path":"content_type.go","digest":"sha256:content","kind":"workspace_file"}]
		}`)}),
	}}
	registry, err := agenttools.NewRegistry(agentruntime.SubmitDecisionDefinition())
	if err != nil {
		t.Fatal(err)
	}
	decision, trajectory, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 2,
	}).Decide(context.Background(), agentcontext.Bundle{
		Event:    domain.NormalizedEvent{MessageID: "om_missing_confidence", Content: "请直接回复这条结论"},
		WorkKind: domain.WorkKindDirectMention,
		Sources: []domain.SourceRef{{
			RelativePath: "content_type.go",
			Digest:       "sha256:content",
			Kind:         "workspace_file",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || decision.Confidence != 0.94 || model.calls != 2 {
		t.Fatalf("decision=%+v calls=%d", decision, model.calls)
	}
	if !codingEvalTrajectoryContains(trajectory, "missing reply_confidence") {
		t.Fatalf("trajectory did not reject missing confidence: %+v", trajectory)
	}
}

func codingEvalToolCall(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func codingEvalTrajectoryContains(messages []*schema.Message, substring string) bool {
	for _, message := range messages {
		if message != nil && strings.Contains(message.Content, substring) {
			return true
		}
	}
	return false
}

func codingEvalMessagesContain(messages []*schema.Message, substring string) bool {
	return codingEvalTrajectoryContains(messages, substring)
}

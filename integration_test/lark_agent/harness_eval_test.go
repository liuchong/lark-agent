package larkagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentruntime "github.com/liuchong/lark-agent/agent/runtime"
	agenttools "github.com/liuchong/lark-agent/agent/tools"
)

type harnessCase struct {
	Name            string              `json:"name"`
	ExpectedOutcome domain.ReplyOutcome `json:"expected_outcome"`
	MaxModelCalls   int                 `json:"max_model_calls"`
	MaxToolCalls    int                 `json:"max_tool_calls"`
}

func TestHarnessEvalFixtureCatalogIsBoundedAndTyped(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "harness_cases", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []harnessCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 4 {
		t.Fatalf("cases=%+v", cases)
	}
	for _, testCase := range cases {
		if testCase.Name == "" || testCase.MaxModelCalls <= 0 || testCase.MaxToolCalls < 0 {
			t.Fatalf("invalid case=%+v", testCase)
		}
		switch testCase.ExpectedOutcome {
		case domain.ReplyOutcomeComplete,
			domain.ReplyOutcomePartial,
			domain.ReplyOutcomeClarification:
		default:
			t.Fatalf("invalid expected outcome in %+v", testCase)
		}
	}
}

func TestHarnessEvalPreservesUsefulPartialWithoutFixedPhrasing(t *testing.T) {
	model := &responseQualityModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"context",
			"get_lark_context",
			`{"chat_id":"oc_eval","message_id":"om_eval","limit":8}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"submit",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.98,
				"reply_confidence":0.9,
				"risk":"low",
				"evidence_status":"insufficient",
				"reply_outcome":"partial",
				"progress":{
					"completed_checks":["伪造的完成项"],
					"initial_finding":"目标消息要求核对发布状态",
					"unknowns":["当前生产版本"],
					"next_step":"提供发布记录或版本号"
				},
				"reply_text":"目标消息要求核对发布状态；当前生产版本没有足够证据，请提供发布记录或版本号。",
				"owner_action":"核对生产版本。",
				"reason":"bounded context produced a useful partial result"
			}`,
		)}),
	}}
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "get_lark_context"},
			NonOwnerReadOnly: true,
			SameChatArgument: "chat_id",
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				return agenttools.Execution{
					Content: `{"messages":[{"content":"请核对发布状态"}]}`,
					Sources: []domain.SourceRef{{
						RelativePath: "lark/om_eval",
						Digest:       "sha256:context",
						Kind:         "lark_message",
					}},
				}, nil
			},
		},
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
			MessageID: "om_eval",
			ChatID:    "oc_eval",
			SenderID:  "ou_sender",
			Content:   "@Owner 请核对发布状态",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		},
		TaskSummary: "@Owner 请核对发布状态",
		TaskClass:   domain.TaskClassInvestigation,
		WorkKind:    domain.WorkKindSimpleQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.ReplyOutcome != domain.ReplyOutcomePartial ||
		decision.Progress.InitialFinding == "" ||
		len(decision.Progress.CompletedChecks) != 1 ||
		decision.Progress.CompletedChecks[0] != "get_lark_context" ||
		model.calls != 2 {
		t.Fatalf("decision=%+v modelCalls=%d", decision, model.calls)
	}
}

func TestHarnessEvalClarificationDoesNotRequireFakeCodeRead(t *testing.T) {
	model := &responseQualityModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"submit",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.98,
				"reply_confidence":0.9,
				"risk":"low",
				"evidence_status":"insufficient",
				"reply_outcome":"clarification",
				"progress":{
					"completed_checks":["伪造代码读取"],
					"unknowns":["报错内容和代码路径"],
					"next_step":"补充报错文本或工作区相对路径"
				},
				"reply_text":"src/service.go 已经把结果序列化成 JSON，请补充报错内容。",
				"reason":"required input is missing"
			}`,
		)}),
	}}
	registry, err := agenttools.NewRegistry(agentruntime.SubmitDecisionDefinition())
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
			MessageID: "om_clarification_eval",
			ChatID:    "oc_eval",
			SenderID:  "ou_sender",
			Content:   "@Owner 看一下这个报错",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		},
		TaskClass: domain.TaskClassCoding,
		WorkKind:  domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.ReplyOutcome != domain.ReplyOutcomeClarification ||
		len(decision.Progress.CompletedChecks) != 0 ||
		strings.Contains(decision.ReplyText, "已经把结果序列化") ||
		!strings.Contains(decision.ReplyText, "仍未知") ||
		model.calls != 1 {
		t.Fatalf("decision=%+v modelCalls=%d", decision, model.calls)
	}
}

func TestHarnessEvalRepeatedStableResultStopsMechanicalToolCalls(t *testing.T) {
	model := &responseQualityModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("probe_1", "probe", `{"query":"same"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("probe_2", "probe", `{ "query": "same" }`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("probe_3", "probe", `{"query":"same"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("probe_4", "probe", `{"query":"same"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"submit",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.98,
				"reply_confidence":0.9,
				"risk":"low",
				"evidence_status":"insufficient",
				"reply_outcome":"partial",
				"progress":{
					"completed_checks":["重复探针返回同一结果"],
					"unknowns":["目标状态"],
					"next_step":"提供新的证据源"
				},
				"reply_text":"现有探针重复返回同一结果，目标状态仍未知。",
				"reason":"unchanged conditions require partial convergence"
			}`,
		)}),
	}}
	executions := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "probe"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				executions++
				return agenttools.Execution{Content: `{"same":true}`}, nil
			},
		},
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (agentruntime.AgentLoop{
		Model:            model,
		Tools:            registry,
		MaxTurns:         10,
		SimpleMaxTurns:   10,
		MaxNoProgress:    3,
		MaxRepeatedCalls: 3,
		MaxToolCalls:     10,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_repeat_eval",
			SenderID:  "ou_owner",
			Content:   "检查目标状态",
		},
		WorkKind: domain.WorkKindSimpleQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.ReplyOutcome != domain.ReplyOutcomePartial ||
		executions != 3 ||
		model.calls != 5 {
		t.Fatalf("decision=%+v executions=%d modelCalls=%d", decision, executions, model.calls)
	}
}

func TestHarnessEvalUnsupportedGroundingCannotEscapeAsVerified(t *testing.T) {
	model := &responseQualityModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"read",
			"read_workspace",
			`{"path":"service.go"}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"unsupported",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.98,
				"reply_confidence":0.9,
				"risk":"low",
				"evidence_status":"verified",
				"reply_outcome":"partial",
				"progress":{
					"completed_checks":["读取 service.go"],
					"initial_finding":"处理入口存在",
					"unknowns":["部署配置"],
					"next_step":"核对部署配置"
				},
				"reply_text":"结论：missingHandler 已接入。依据：service.go。未知：部署配置。",
				"reason":"unsupported identifier",
				"source_refs":[{
					"relative_path":"service.go",
					"digest":"sha256:service",
					"kind":"workspace_file"
				}]
			}`,
		)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall(
			"safe_partial",
			"submit_decision",
			`{
				"decision":"reply",
				"relevance_confidence":0.98,
				"reply_confidence":0.9,
				"risk":"low",
				"evidence_status":"insufficient",
				"reply_outcome":"partial",
				"progress":{
					"completed_checks":["读取 service.go"],
					"initial_finding":"现有读取不足以支持原断言",
					"unknowns":["实际处理入口"],
					"next_step":"提供准确符号或入口路径"
				},
				"reply_text":"现有读取不足以确认实际处理入口。",
				"reason":"converged without unsupported claim"
			}`,
		)}),
	}}
	executions := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info: &schema.ToolInfo{Name: "read_workspace"},
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				executions++
				return agenttools.Execution{
					Content: `func currentHandler() {}`,
					Sources: []domain.SourceRef{{
						RelativePath: "service.go",
						Digest:       "sha256:service",
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
	decision, _, err := (agentruntime.AgentLoop{
		Model:    model,
		Tools:    registry,
		MaxTurns: 4,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_grounding_eval",
			SenderID:  "ou_owner",
			Content:   "核对代码里的实际处理入口",
		},
		TaskClass: domain.TaskClassCoding,
		WorkKind:  domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.EvidenceStatus != domain.EvidenceInsufficient ||
		decision.ReplyOutcome != domain.ReplyOutcomePartial ||
		executions != 1 ||
		model.calls != 3 {
		t.Fatalf("decision=%+v executions=%d modelCalls=%d", decision, executions, model.calls)
	}
}

func TestHarnessEvalTerminalFinalizerPreservesPartialForNonConvergedCodingRun(t *testing.T) {
	model := &responseQualityModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("plan", "submit_investigation_plan", `{
			"question":"示例状态变更通知是否需要同步 示例客户端回调和示例通知",
			"entry_points":["sdk","message"],
			"symbols":["sample state change","callback"],
			"tools":["search_workspace"],
			"stop_conditions":["找到生产实现或确认搜索无证据"]
		}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("search", "search_workspace", `{"query":"sample state change callback"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("explore", "explore_workspace", `{"query":"sample state change"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("list", "list_workspace", `{"path":"."}`)}),
		schema.AssistantMessage("", []schema.ToolCall{responseQualityToolCall("read", "read_workspace", `{"path":"sample-module/callback.go"}`)}),
	}}
	finalizer := &responseQualityModel{responses: []*schema.Message{{
		Role: schema.Assistant,
		Content: `{
			"decision":"reply",
			"relevance_confidence":0.96,
			"reply_confidence":0.88,
			"risk":"low",
			"evidence_status":"insufficient",
			"reply_outcome":"partial",
			"progress":{
				"completed_checks":["search_workspace"],
				"initial_finding":"已搜索示例状态变更通知和 示例客户端回调关键词，但没有可引用生产实现。",
				"unknowns":["是否已有 示例客户端回调类型","是否需要同步示例通知"],
				"next_step":"提供更精确入口路径，或由 Owner 继续核对示例客户端和通知模块"
			},
			"reply_text":"我已搜索示例状态变更通知和 示例客户端回调关键词，但没有找到可引用的生产实现。因此目前无法确认是否已有 示例客户端回调类型或是否需要同步示例通知；下一步需要更精确入口路径，或由 Owner 继续核对示例客户端和通知模块。",
			"owner_action":"核对示例客户端和通知模块入口。",
			"reason":"terminal finalizer produced bounded partial result from retained search receipts"
		}`,
	}}}
	searches := 0
	registry, err := agenttools.NewRegistry(
		agenttools.Definition{
			Info:             &schema.ToolInfo{Name: "search_workspace"},
			NonOwnerReadOnly: true,
			Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
				searches++
				return agenttools.Execution{Content: `{"results":[],"query":"sample state change callback"}`}, nil
			},
		},
		agenttools.Definition{Info: &schema.ToolInfo{Name: "explore_workspace"}, NonOwnerReadOnly: true, Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
			t.Fatal("terminal-only tool should not execute")
			return agenttools.Execution{}, nil
		}},
		agenttools.Definition{Info: &schema.ToolInfo{Name: "list_workspace"}, NonOwnerReadOnly: true, Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
			t.Fatal("terminal-only tool should not execute")
			return agenttools.Execution{}, nil
		}},
		agenttools.Definition{Info: &schema.ToolInfo{Name: "read_workspace"}, NonOwnerReadOnly: true, Execute: func(context.Context, json.RawMessage) (agenttools.Execution, error) {
			t.Fatal("terminal-only tool should not execute")
			return agenttools.Execution{}, nil
		}},
		agentruntime.SubmitInvestigationPlanDefinition(),
		agentruntime.SubmitDecisionDefinition(),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := (agentruntime.AgentLoop{
		Model: model, TerminalFinalizer: finalizer, Tools: registry, MaxTurns: 8, MaxToolCalls: 1,
	}).Decide(context.Background(), agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Event: domain.NormalizedEvent{
			MessageID: "om_5680_eval",
			ChatID:    "oc_backend",
			SenderID:  "ou_sender",
			Content:   "@Owner 示例状态变更通知需要同步 示例客户端回调和示例通知吗？",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		},
		TaskClass: domain.TaskClassCoding,
		WorkKind:  domain.WorkKindCodingQuestion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.ReplyOutcome != domain.ReplyOutcomePartial ||
		decision.EvidenceStatus != domain.EvidenceInsufficient ||
		searches != 1 ||
		finalizer.calls != 1 ||
		model.calls != 5 {
		t.Fatalf("decision=%+v searches=%d modelCalls=%d finalizerCalls=%d", decision, searches, model.calls, finalizer.calls)
	}
}

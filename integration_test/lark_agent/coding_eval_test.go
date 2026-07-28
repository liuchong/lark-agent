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

func TestCodingQuestionWithEvidenceConvergesBeforeFinalTurns(t *testing.T) {
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
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("read", "read_workspace", `{"path":"content_type.go"}`)}),
		schema.AssistantMessage("", []schema.ToolCall{codingEvalToolCall("waste", "search_code_symbols", `{"query":"unrelated history","max_results":5}`)}),
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
	loop := agentruntime.AgentLoop{Model: model, Tools: registry, MaxTurns: 8}
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
	if !codingEvalTrajectoryContains(trajectory, "coding evidence is complete; submit_decision is required now") {
		t.Fatalf("trajectory did not reject the extra search: %+v", trajectory)
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

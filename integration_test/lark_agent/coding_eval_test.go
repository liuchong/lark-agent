package larkagent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		Event:    domain.NormalizedEvent{MessageID: "om_missing_confidence", Content: "这个函数存在吗？"},
		WorkKind: domain.WorkKindCodingQuestion,
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

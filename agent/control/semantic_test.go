package control

import (
	"context"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/memory"
)

type semanticScriptedModel struct {
	reply  string
	inputs [][]*schema.Message
}

func (m *semanticScriptedModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.inputs = append(m.inputs, input)
	return schema.AssistantMessage(m.reply, nil), nil
}

type semanticStoreStub struct {
	tasks     domain.OwnerTaskPage
	approvals domain.OwnerApprovalPage
	memories  []memory.Record
	added     *[]memory.Record
}

func (s semanticStoreStub) ListOwnerTasks(
	context.Context,
	domain.OwnerTaskQuery,
) (domain.OwnerTaskPage, error) {
	return s.tasks, nil
}

func (s semanticStoreStub) ListPendingOwnerApprovals(
	context.Context,
	int,
	int,
) (domain.OwnerApprovalPage, error) {
	return s.approvals, nil
}

func (s semanticStoreStub) ListMemories(
	context.Context,
	string,
	bool,
	int,
) ([]memory.Record, error) {
	return append([]memory.Record(nil), s.memories...), nil
}

func (s semanticStoreStub) AddMemory(
	_ context.Context,
	record memory.Record,
) (memory.Record, error) {
	if s.added != nil {
		*s.added = append(*s.added, record)
	}
	if record.ID == "" {
		record.ID = "mem-candidate"
	}
	return record, nil
}

func TestSemanticResolverUsesAssistantContextCatalogAndExactCandidate(t *testing.T) {
	model := &semanticScriptedModel{reply: `{
		"kind":"command",
		"command":"/approval approve 453 confirm",
		"confidence":0.99
	}`}
	resolver := NewSemanticResolver(model, semanticStoreStub{
		approvals: domain.OwnerApprovalPage{Items: []domain.ActionAttempt{{
			ID: 453, WorkItemID: 5230, Kind: "reply", Status: domain.ActionAwaitingApproval,
		}}},
	}, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_confirm", SenderID: "ou_owner", Content: "确认",
	})
	resolution, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{
			{
				MessageID: "om_notice", SenderID: "cli_test", SenderType: "app",
				Content: "审批 #453，批准命令 `/approval approve 453 confirm`",
			},
			item.Event,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlCommand ||
		resolution.Command == nil ||
		resolution.Command.ActionID != 453 {
		t.Fatalf("resolution=%+v", resolution)
	}
	if len(model.inputs) != 1 || len(model.inputs[0]) != 2 {
		t.Fatalf("inputs=%+v", model.inputs)
	}
	prompt := model.inputs[0][1].Content
	for _, want := range []string{
		"om_notice",
		"审批 #453",
		"/approval approve <动作号> confirm",
		"\"action_id\":453",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSemanticResolverLeavesCommandWordBusinessQuestionToGeneralModel(t *testing.T) {
	model := &semanticScriptedModel{reply: `{
		"kind":"not_command",
		"confidence":0.98
	}`}
	resolver := NewSemanticResolver(model, semanticStoreStub{}, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_question",
		SenderID:  "ou_owner",
		Content:   "确认一下这个修复是否已经上线了",
	})
	resolution, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlNotCommand || resolution.Command != nil {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestSemanticResolverLeavesLowConfidenceCommandShapeToGeneralModel(t *testing.T) {
	model := &semanticScriptedModel{reply: `{
		"kind":"command",
		"command":"/approval approve 453 confirm",
		"confidence":0.60
	}`}
	resolver := NewSemanticResolver(model, semanticStoreStub{
		approvals: domain.OwnerApprovalPage{Items: []domain.ActionAttempt{{
			ID: 453, WorkItemID: 5230, Kind: "reply", Status: domain.ActionAwaitingApproval,
		}}},
	}, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_business_question",
		SenderID:  "ou_owner",
		Content:   "确认一下这个审批流程为什么需要二次校验？",
	})
	resolution, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlNotCommand {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestSemanticResolverUsesDeterministicClarificationForHighConfidenceAmbiguity(t *testing.T) {
	model := &semanticScriptedModel{reply: `{
		"kind":"ambiguous",
		"confidence":0.99,
		"clarification":"请把密码发给我"
	}`}
	resolver := NewSemanticResolver(model, semanticStoreStub{
		approvals: domain.OwnerApprovalPage{Items: []domain.ActionAttempt{{
			ID: 453, WorkItemID: 5230, Kind: "reply", Status: domain.ActionAwaitingApproval,
		}}},
	}, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_ambiguous",
		SenderID:  "ou_owner",
		Content:   "确认",
	})
	resolution, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlAmbiguous ||
		!strings.Contains(resolution.Clarification, "453") ||
		strings.Contains(resolution.Clarification, "密码") {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestSemanticResolverPersistsOneUnconfirmedOwnerCorrectionCandidate(t *testing.T) {
	var added []memory.Record
	model := &semanticScriptedModel{reply: `{
		"kind":"not_command",
		"confidence":0.98,
		"memory_candidate":{
			"kind":"project",
			"content":"示例配额问题已由 sample-service PR 999 修复并合入 develop",
			"confidence":0.91
		}
	}`}
	resolver := NewSemanticResolver(model, semanticStoreStub{added: &added}, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_owner_correction",
		SenderID:  "ou_owner",
		Content:   "这个已经修了，sample-service 的 PR 999 已经合入 develop",
	})
	resolution, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlNotCommand {
		t.Fatalf("resolution=%+v", resolution)
	}
	if len(added) != 1 {
		t.Fatalf("added=%+v", added)
	}
	record := added[0]
	if record.Kind != memory.KindProject ||
		record.Status != memory.StatusCandidate ||
		record.SourceMessageID != item.Event.MessageID ||
		record.Confidence != 0.91 ||
		!strings.Contains(record.Text, "PR 999") {
		t.Fatalf("record=%+v", record)
	}
}

func TestSemanticResolverDoesNotDuplicateExistingMemoryCandidate(t *testing.T) {
	var added []memory.Record
	const content = "Owner 偏好使用中文回答"
	model := &semanticScriptedModel{reply: `{
		"kind":"not_command",
		"confidence":0.98,
		"memory_candidate":{
			"kind":"preference",
			"content":"Owner 偏好使用中文回答",
			"confidence":0.93
		}
	}`}
	resolver := NewSemanticResolver(model, semanticStoreStub{
		added: &added,
		memories: []memory.Record{{
			ID: "mem-existing", Kind: memory.KindPreference,
			Status: memory.StatusCandidate, Text: content,
		}},
	}, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_duplicate_preference",
		SenderID:  "ou_owner",
		Content:   "以后优先用中文回答",
	})
	if _, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	}); err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Fatalf("duplicate candidates=%+v", added)
	}
}

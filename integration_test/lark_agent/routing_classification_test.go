package larkagent_test

import (
	"context"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/router"
)

func TestWorkspaceProductionEntryInvestigationRoutesAsCodingQuestion(t *testing.T) {
	const (
		ownerID = "ou_owner"
		botID   = "ou_assistant"
		prompt  = "【lark-agent 验收】请检查 Workspace 内示例文件预览上传与审核相关的生产入口，只做只读调查，简要给出结论、依据和仍未知项。"
	)
	r := router.New(router.Config{
		OwnerOpenID:      ownerID,
		AssistantOpenIDs: []string{botID},
		AssistantNames:   []string{"测试负责人的智能助手"},
	})

	tests := []struct {
		name  string
		event domain.NormalizedEvent
	}{
		{
			name: "assistant private chat",
			event: domain.NormalizedEvent{
				MessageID:     "om_private",
				ChatID:        "oc_private",
				ChatType:      "p2p",
				ChatPartnerID: botID,
				SenderID:      ownerID,
				Content:       prompt,
			},
		},
		{
			name: "assistant group mention",
			event: domain.NormalizedEvent{
				MessageID: "om_group",
				ChatID:    "oc_group",
				ChatType:  "group",
				SenderID:  ownerID,
				Mentions:  []domain.Mention{{OpenID: botID, Name: "测试负责人的智能助手"}},
				Content:   "@_user_1 " + prompt,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := r.Route(context.Background(), domain.NewWorkItem(tt.event))
			if err != nil {
				t.Fatal(err)
			}
			if decision.WorkKind != domain.WorkKindCodingQuestion ||
				decision.Priority != domain.PriorityCodingQuestion {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestWorkspaceBusinessInvestigationStaysSimpleQuestion(t *testing.T) {
	const (
		ownerID = "ou_owner"
		botID   = "ou_assistant"
	)
	r := router.New(router.Config{
		OwnerOpenID:      ownerID,
		AssistantOpenIDs: []string{botID},
		AssistantNames:   []string{"测试负责人的智能助手"},
	})
	decision, err := r.Route(context.Background(), domain.NewWorkItem(domain.NormalizedEvent{
		MessageID:     "om_business",
		ChatID:        "oc_private",
		ChatType:      "p2p",
		ChatPartnerID: botID,
		SenderID:      ownerID,
		Content:       "请检查 Workspace 内本周销售数据并给出业务结论",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if decision.WorkKind != domain.WorkKindSimpleQuestion ||
		decision.Priority != domain.PrioritySimpleQuestion {
		t.Fatalf("decision=%+v", decision)
	}
}

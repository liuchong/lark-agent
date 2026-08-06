package replymatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
)

type scriptedModel struct {
	reply  string
	err    error
	inputs [][]*schema.Message
}

func (m *scriptedModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.inputs = append(m.inputs, input)
	if m.err != nil {
		return nil, m.err
	}
	return schema.AssistantMessage(m.reply, nil), nil
}

func TestResolverMatchesOnlyTheAnsweredPendingTarget(t *testing.T) {
	base := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_question_a",
		"result":"answered",
		"matched_owner_message_ids":["om_owner_answer"],
		"confidence":0.97,
		"reason":"The owner directly provided the requested release date."
	}`}
	resolver := New(model, "ou_owner")
	resolution, err := resolver.Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_question_a", ChatID: "oc_group", SenderID: "ou_a",
			Content: "发布日期是哪天？", CreatedAt: base,
		}),
		Pending: []domain.WorkItem{
			domain.NewWorkItem(domain.NormalizedEvent{
				MessageID: "om_question_a", ChatID: "oc_group", SenderID: "ou_a",
				Content: "发布日期是哪天？", CreatedAt: base,
			}),
			domain.NewWorkItem(domain.NormalizedEvent{
				MessageID: "om_question_b", ChatID: "oc_group", SenderID: "ou_b",
				Content: "负责人是谁？", CreatedAt: base.Add(10 * time.Second),
			}),
		},
		Messages: []domain.NormalizedEvent{
			{MessageID: "om_question_a", ChatID: "oc_group", SenderID: "ou_a", Content: "发布日期是哪天？", CreatedAt: base},
			{MessageID: "om_question_b", ChatID: "oc_group", SenderID: "ou_b", Content: "负责人是谁？", CreatedAt: base.Add(10 * time.Second)},
			{MessageID: "om_owner_answer", ChatID: "oc_group", SenderID: "ou_owner", Content: "发布日期是 8 月 5 日。", CreatedAt: base.Add(time.Minute)},
		},
		ContextCutoff: base.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultAnswered ||
		len(resolution.MatchedOwnerMessageIDs) != 1 ||
		resolution.MatchedOwnerMessageIDs[0] != "om_owner_answer" ||
		resolution.TargetMessageID != "om_question_a" {
		t.Fatalf("resolution=%+v", resolution)
	}
	if len(model.inputs) != 1 ||
		!strings.Contains(model.inputs[0][1].Content, "om_question_b") {
		t.Fatalf("resolver did not receive all pending targets: %+v", model.inputs)
	}
}

func TestResolverScopesOutOwnerReplyAfterNewerOtherSenderMention(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 9, 0, time.UTC)
	link := domain.NormalizedEvent{
		MessageID: "om_record_link", ChatID: "oc_group", ChatType: "topic_group",
		SenderID: "ou_requester", Content: "https://example.test/record/bug",
		CreatedAt: base,
	}
	target := domain.NormalizedEvent{
		MessageID: "om_fix_status", ChatID: "oc_group", ChatType: "topic_group",
		RootMessageID: link.MessageID, ReplyToMessageID: link.MessageID,
		SenderID: "ou_requester", Content: "@测试负责人 这个问题修复后改下状态哈",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base.Add(32 * time.Second),
	}
	newerRequest := domain.NormalizedEvent{
		MessageID: "om_newer_request", ChatID: "oc_group", ChatType: "topic_group",
		SenderID: "ou_other", Content: "另一个无关示例需求尚未完成 @测试负责人",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base.Add(8*time.Hour + 25*time.Minute),
	}
	newerAnswer := domain.NormalizedEvent{
		MessageID: "om_newer_answer", ChatID: "oc_group", ChatType: "topic_group",
		SenderID:  "ou_owner",
		Content:   "做了，我刚把代码合了，等会儿部署完了我跟你说",
		CreatedAt: base.Add(9*time.Hour + 2*time.Minute),
	}
	model := &scriptedModel{reply: `{
		"target_message_id":"om_fix_status",
		"result":"unanswered",
		"confidence":0.96,
		"reason":"the owner has not handled the linked issue status handoff",
		"target_intent":"handoff_status_request",
		"response_obligation_quote":"这个问题修复后改下状态哈",
		"task_summary":"locate the linked issue and update its status after verifying the fix",
		"task_class":"resource_handoff",
		"classification_confidence":0.96,
		"requires_progress":true
	}`}

	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(target),
		Messages: []domain.NormalizedEvent{
			link, target, newerRequest, newerAnswer,
		},
		ContextCutoff: base.Add(10 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultUnanswered ||
		resolution.TaskClass != domain.TaskClassResourceHandoff ||
		len(resolution.ContextMessages) != 2 {
		t.Fatalf("resolution=%+v", resolution)
	}
	if len(model.inputs) != 1 {
		t.Fatalf("model calls=%d", len(model.inputs))
	}
	prompt := model.inputs[0][1].Content
	if !strings.Contains(prompt, link.MessageID) ||
		!strings.Contains(prompt, target.MessageID) ||
		strings.Contains(prompt, newerRequest.MessageID) ||
		strings.Contains(prompt, newerAnswer.MessageID) ||
		strings.Contains(prompt, "无关示例需求") {
		t.Fatalf("prompt retained unrelated later segment: %s", prompt)
	}
}

func TestResolverRejectsOwnerAnswerFromNewerOtherSenderSegment(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 41, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_fix_status",
		"result":"answered",
		"matched_owner_message_ids":["om_newer_answer"],
		"confidence":0.92,
		"reason":"the owner said the code was merged"
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_fix_status", ChatID: "oc_group", ChatType: "group",
			SenderID: "ou_requester", Content: "@测试负责人 这个问题修复后改下状态哈",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_newer_request", ChatID: "oc_group", ChatType: "group",
				SenderID: "ou_other", Content: "另一个无关示例需求尚未完成 @测试负责人",
				Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
				CreatedAt: base.Add(8 * time.Hour),
			},
			{
				MessageID: "om_newer_answer", ChatID: "oc_group", ChatType: "group",
				SenderID: "ou_owner", Content: "做了，我刚把代码合了",
				CreatedAt: base.Add(9 * time.Hour),
			},
		},
		ContextCutoff: base.Add(10 * time.Hour),
	})
	if err == nil {
		t.Fatal("owner reply from a newer unrelated owner-mention segment was accepted")
	}
}

func TestResolverDoesNotTreatAnotherThreadMentionAsMainChatBoundary(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 41, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_target",
		"result":"answered",
		"matched_owner_message_ids":["om_owner_answer"],
		"confidence":0.96,
		"reason":"the owner explicitly confirmed the target release date"
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_target", ChatID: "oc_group", ChatType: "group",
			SenderID: "ou_requester", Content: "@测试负责人 发布日期是哪天？",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_target", ChatID: "oc_group", ChatType: "group",
				SenderID: "ou_requester", Content: "@测试负责人 发布日期是哪天？",
				Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
				CreatedAt: base,
			},
			{
				MessageID: "om_other_thread", ChatID: "oc_group", ChatType: "group",
				ThreadID: "omt_other", SenderID: "ou_other",
				Content:   "@测试负责人 看下另一个线程",
				Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
				CreatedAt: base.Add(time.Minute),
			},
			{
				MessageID: "om_owner_answer", ChatID: "oc_group", ChatType: "group",
				SenderID: "ou_owner", Content: "原问题的发布日期是 8 月 8 日。",
				CreatedAt: base.Add(2 * time.Minute),
			},
		},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultAnswered ||
		len(resolution.ContextMessages) != 3 {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverRejectsInventedOrNonOwnerMatchedMessageID(t *testing.T) {
	base := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	for _, matchedID := range []string{"om_invented", "om_other_human"} {
		t.Run(matchedID, func(t *testing.T) {
			model := &scriptedModel{reply: `{
				"target_message_id":"om_target",
				"result":"answered",
				"matched_owner_message_ids":["` + matchedID + `"],
				"confidence":0.99,
				"reason":"answered"
			}`}
			resolver := New(model, "ou_owner")
			_, err := resolver.Resolve(context.Background(), Request{
				Target: domain.NewWorkItem(domain.NormalizedEvent{
					MessageID: "om_target", ChatID: "oc_group", SenderID: "ou_sender",
					Content: "请确认日期", CreatedAt: base,
				}),
				Messages: []domain.NormalizedEvent{
					{MessageID: "om_other_human", ChatID: "oc_group", SenderID: "ou_other", Content: "8 月 5 日", CreatedAt: base.Add(time.Minute)},
				},
				ContextCutoff: base.Add(2 * time.Minute),
			})
			if err == nil {
				t.Fatalf("matched id %s was accepted", matchedID)
			}
		})
	}
}

func TestResolverRejectsModelFabricatedOwnerAckReaction(t *testing.T) {
	base := time.Date(2026, 7, 31, 5, 0, 0, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_target",
		"result":"answered",
		"owner_ack_reaction":{
			"reaction_id":"r_fabricated",
			"emoji_type":"Get",
			"operator_type":"user",
			"operator_open_id":"ou_owner"
		},
		"confidence":1,
		"reason":"Owner reacted with Get."
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_target", ChatID: "oc_private", ChatType: "p2p",
			SenderID: "ou_teammate", Content: "要回复的问题", CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err == nil {
		t.Fatal("model-fabricated owner ack reaction was accepted")
	}
}

func TestResolverFailsClosedWhenContextIsIncomplete(t *testing.T) {
	model := &scriptedModel{reply: `{"result":"unanswered","confidence":1}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target:     domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_target"}),
		Incomplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultAmbiguous || len(model.inputs) != 0 {
		t.Fatalf("resolution=%+v model_calls=%d", resolution, len(model.inputs))
	}
}

func TestResolverAcceptsNoReplyNeededForAnswerToOwnerLedPrivateDiscussion(t *testing.T) {
	base := time.Date(2026, 7, 29, 4, 22, 52, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_teammate_answer",
		"result":"no_reply_needed",
		"matched_owner_message_ids":["om_owner_continues"],
		"confidence":0.98,
		"reason":"The target answers the owner's question and adds no new request; the owner continued the same discussion."
	}`}
	resolver := New(model, "ou_owner")
	resolution, err := resolver.Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_teammate_answer", ChatID: "oc_private",
			ChatType: "p2p", SenderID: "ou_teammate",
			Content: "有 UI 和客户端", CreatedAt: base.Add(23 * time.Second),
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_owner_question", ChatID: "oc_private",
				ChatType: "p2p", SenderID: "ou_owner",
				Content:   "感觉你要不要给这个项目配一个 UI，客户端什么的？",
				CreatedAt: base,
			},
			{
				MessageID: "om_teammate_answer", ChatID: "oc_private",
				ChatType: "p2p", SenderID: "ou_teammate",
				Content: "有 UI 和客户端", CreatedAt: base.Add(23 * time.Second),
			},
			{
				MessageID: "om_owner_continues", ChatID: "oc_private",
				ChatType: "p2p", SenderID: "ou_owner",
				Content: "你们怎么还有这个项目？", CreatedAt: base.Add(40 * time.Second),
			},
		},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded ||
		len(resolution.MatchedOwnerMessageIDs) != 1 ||
		resolution.MatchedOwnerMessageIDs[0] != "om_owner_continues" {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverDoesNotTreatAttributiveNiKanDeAsOwnerRequest(t *testing.T) {
	base := time.Date(2026, 8, 6, 12, 54, 23, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_private_pr_ack",
		"result":"no_reply_needed",
		"confidence":0.96,
		"reason":"The target acknowledges that the screenshot was the PR the owner had viewed and adds no request."
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_private_pr_ack", ChatID: "oc_private", ChatType: "p2p",
			SenderID: "ou_teammate", SenderType: "user",
			Content:   "哦哦你看的 PR",
			CreatedAt: base,
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_owner_image", ChatID: "oc_private", ChatType: "p2p",
				SenderID: "ou_owner", SenderType: "user",
				Content: "[Image]", CreatedAt: base.Add(-time.Minute),
			},
			{
				MessageID: "om_private_pr_ack", ChatID: "oc_private", ChatType: "p2p",
				SenderID: "ou_teammate", SenderType: "user",
				Content: "哦哦你看的 PR", CreatedAt: base,
			},
		},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverKeepsImperativeNiKanRequestAsObligation(t *testing.T) {
	base := time.Date(2026, 8, 6, 12, 54, 23, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_private_pr_request",
		"result":"no_reply_needed",
		"confidence":0.96,
		"reason":"No reply is needed."
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_private_pr_request", ChatID: "oc_private", ChatType: "p2p",
			SenderID: "ou_teammate", SenderType: "user",
			Content:   "你看一下这个 PR",
			CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err == nil {
		t.Fatal("imperative PR review request accepted no_reply_needed")
	}
}

func TestResolverNormalizesPrivateAnswerWithoutTargetObligationToNoReplyNeeded(t *testing.T) {
	base := time.Date(2026, 7, 31, 4, 22, 52, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_private_answer",
		"result":"unanswered",
		"confidence":0.92,
		"reason":"The prior owner question mentioned a missing group member limit, but the target only reports that it was discussed without detailed design.",
		"target_intent":"answer",
		"task_summary":"investigate why the group member limit cannot be found",
		"task_class":"coding",
		"classification_confidence":0.90,
		"requires_progress":true
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_private_answer", ChatID: "oc_private", ChatType: "p2p",
			SenderID: "ou_teammate", SenderType: "user",
			Content:   "当时说要做但没设计详细交互，后面也没看到继续推进",
			CreatedAt: base.Add(2 * time.Minute),
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_owner_question", ChatID: "oc_private", ChatType: "p2p",
				SenderID:  "ou_owner",
				Content:   "那个群成员数量限制为什么找不到？",
				CreatedAt: base,
			},
			{
				MessageID: "om_private_answer", ChatID: "oc_private", ChatType: "p2p",
				SenderID: "ou_teammate", SenderType: "user",
				Content:   "当时说要做但没设计详细交互，后面也没看到继续推进",
				CreatedAt: base.Add(2 * time.Minute),
			},
		},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded ||
		resolution.TargetIntent != "answer" ||
		resolution.ResponseObligationQuote != "" ||
		resolution.RequiresProgress ||
		resolution.TaskSummary != "" {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverNormalizesPrivateDesignStatementWithoutExplicitAsk(t *testing.T) {
	base := time.Date(2026, 8, 4, 0, 32, 26, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_private_design",
		"result":"unanswered",
		"confidence":0.72,
		"reason":"The target states a product decision about a unified menu.",
		"target_intent":"communicate design decision",
		"response_obligation_quote":"目前不区分两种示例类型，统一放在一个入口里处理",
		"task_summary":"adjust sticker and GIF menu design",
		"task_class":"simple",
		"classification_confidence":0.72,
		"requires_progress":false
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_private_design", ChatID: "oc_private", ChatType: "p2p",
			SenderID: "ou_teammate", SenderType: "user",
			Content:   "我看了一下没问题，目前不区分两种示例类型，统一放在一个入口里处理",
			CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded ||
		resolution.ResponseObligationQuote != "" ||
		resolution.TaskSummary != "" {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverRequiresExactPrivateTargetObligationForUnanswered(t *testing.T) {
	base := time.Date(2026, 7, 31, 4, 22, 52, 0, time.UTC)
	t.Run("explicit request quote is accepted", func(t *testing.T) {
		model := &scriptedModel{reply: `{
			"target_message_id":"om_private_request",
			"result":"unanswered",
			"confidence":0.94,
			"reason":"The target asks the owner to investigate code.",
			"target_intent":"request",
			"response_obligation_quote":"你帮我查一下代码",
			"task_summary":"查代码确认群成员数量限制入口",
			"task_class":"coding",
			"classification_confidence":0.91,
			"requires_progress":true
		}`}
		resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
			Target: domain.NewWorkItem(domain.NormalizedEvent{
				MessageID: "om_private_request", ChatID: "oc_private", ChatType: "p2p",
				SenderID: "ou_teammate", SenderType: "user",
				Content:   "当时说要做但没设计详细交互，你帮我查一下代码",
				CreatedAt: base.Add(2 * time.Minute),
			}),
			ContextCutoff: base.Add(3 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Result != ResultUnanswered ||
			resolution.ResponseObligationQuote != "你帮我查一下代码" {
			t.Fatalf("resolution=%+v", resolution)
		}
	})

	t.Run("invented quote is rejected", func(t *testing.T) {
		model := &scriptedModel{reply: `{
			"target_message_id":"om_private_answer",
			"result":"unanswered",
			"confidence":0.94,
			"reason":"The target asks for code investigation.",
			"target_intent":"request",
			"response_obligation_quote":"你帮我查一下代码",
			"task_summary":"查代码确认群成员数量限制入口",
			"task_class":"coding",
			"classification_confidence":0.91,
			"requires_progress":true
		}`}
		_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
			Target: domain.NewWorkItem(domain.NormalizedEvent{
				MessageID: "om_private_answer", ChatID: "oc_private", ChatType: "p2p",
				SenderID: "ou_teammate", SenderType: "user",
				Content:   "当时说要做但没设计详细交互，后面也没看到继续推进",
				CreatedAt: base.Add(2 * time.Minute),
			}),
			ContextCutoff: base.Add(3 * time.Minute),
		})
		if err == nil {
			t.Fatal("invented private target obligation quote was accepted")
		}
	})
}

func TestResolverRejectsNoReplyNeededForExplicitGroupOwnerRequest(t *testing.T) {
	base := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_group_request",
		"result":"no_reply_needed",
		"confidence":0.99,
		"reason":"No reply is needed."
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_group_request", ChatID: "oc_group",
			ChatType: "group", SenderID: "ou_teammate",
			Content:   "@测试负责人 请确认发布日期",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err == nil {
		t.Fatal("explicit group owner request accepted no_reply_needed")
	}
}

func TestResolverRejectsNoReplyNeededForChineseGroupInvestigationRequest(t *testing.T) {
	base := time.Date(2026, 8, 4, 1, 20, 0, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_group_investigate",
		"result":"no_reply_needed",
		"confidence":0.99,
		"reason":"No reply is needed."
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_group_investigate", ChatID: "oc_group",
			ChatType: "group", SenderID: "ou_teammate",
			Content:   "@测试负责人 这个线上问题辛苦排查下",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err == nil {
		t.Fatal("Chinese group investigation request accepted no_reply_needed")
	}
}

func TestResolverRejectsNoReplyNeededForFixThenUpdateStatusRequest(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 41, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_group_fix_status",
		"result":"no_reply_needed",
		"confidence":0.90,
		"reason":"The target asks the owner to fix the linked issue and update its status.",
		"target_intent":"Request to fix the problem in the linked record and update its status after the fix."
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_group_fix_status", ChatID: "oc_group",
			ChatType: "group", SenderID: "ou_teammate",
			Content:   "@测试负责人 这个问题修复后改下状态哈",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err == nil {
		t.Fatal("fix then update status request accepted no_reply_needed")
	}
}

func TestResolverAllowsNoReplyNeededForCompletedStatusStatement(t *testing.T) {
	base := time.Date(2026, 8, 4, 1, 21, 0, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_private_status",
		"result":"ambiguous",
		"confidence":0.63,
		"reason":"The target is a completed status statement.",
		"target_intent":"informational completed status"
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_private_status", ChatID: "oc_private",
			ChatType: "p2p", SenderID: "ou_teammate",
			Content:   "已确认不用改，已经处理完",
			CreatedAt: base,
		}),
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverAcceptsNoReplyNeededForGroupOwnerSocialAcknowledgement(t *testing.T) {
	base := time.Date(2026, 8, 4, 1, 9, 39, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_group_ack",
		"result":"ambiguous",
		"confidence":0.55,
		"reason":"The target is a thumbs-up and compliment on the owner's image.",
		"target_intent":"social acknowledgement / compliment"
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_group_ack", ChatID: "oc_group",
			ChatType: "group", SenderID: "ou_teammate",
			Content:   "@测试负责人 [赞]这图有禅意啊",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_owner_image", ChatID: "oc_group",
				ChatType: "group", SenderID: "ou_owner",
				Content: "[图片]", CreatedAt: base.Add(-time.Minute),
			},
			{
				MessageID: "om_group_ack", ChatID: "oc_group",
				ChatType: "group", SenderID: "ou_teammate",
				Content:   "@测试负责人 [赞]这图有禅意啊",
				Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
				CreatedAt: base,
			},
		},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded ||
		resolution.RequiresProgress ||
		resolution.TaskSummary != "" {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverSuppressesOwnerAuthoredDelegatedTargetWithoutCallingModel(t *testing.T) {
	model := &scriptedModel{}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_owner_question", ChatID: "oc_private",
			ChatType: "p2p", SenderID: "ou_owner",
			Content: "感觉你要不要给这个项目配一个 UI？",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultNoReplyNeeded ||
		resolution.Confidence != 1 ||
		len(model.inputs) != 0 {
		t.Fatalf("resolution=%+v model_calls=%d", resolution, len(model.inputs))
	}
}

func TestResolverClassifiesContextualCodingHandoff(t *testing.T) {
	base := time.Date(2026, 7, 30, 2, 42, 0, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_handoff",
		"result":"unanswered",
		"confidence":0.98,
		"reason":"the owner has not handled the production sample-event handoff",
		"task_summary":"investigate why production message editing returns 1408 SampleEventDisabled",
		"task_class":"coding",
		"classification_confidence":0.97,
		"requires_progress":true
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_handoff", ChatID: "oc_rd", ChatType: "group",
			SenderID: "ou_sender", Content: "@测试负责人 你看看吧，我电脑断线了",
			Mentions: []domain.Mention{{OpenID: "ou_owner"}}, CreatedAt: base.Add(5 * time.Minute),
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_question", ChatID: "oc_rd", ChatType: "group",
				SenderID: "ou_a", Content: "示例事件的后台服务还没上线么",
				CreatedAt: base,
			},
			{
				MessageID: "om_image", ChatID: "oc_rd", ChatType: "group",
				SenderID: "ou_b", Content: "[图片: 1408 SampleEventDisabled]",
				CreatedAt: base.Add(4 * time.Minute),
			},
			{
				MessageID: "om_handoff", ChatID: "oc_rd", ChatType: "group",
				SenderID: "ou_sender", Content: "@测试负责人 你看看吧，我电脑断线了",
				Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
				CreatedAt: base.Add(5 * time.Minute),
			},
			{
				MessageID: "om_clarify", ChatID: "oc_rd", ChatType: "group",
				SenderID: "ou_a", Content: "对，是 prod 环境没上线",
				CreatedAt: base.Add(5*time.Minute + 20*time.Second),
			},
		},
		ContextCutoff: base.Add(8 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.TaskSummary != "investigate why production message editing returns 1408 SampleEventDisabled" ||
		resolution.TaskClass != domain.TaskClassCoding ||
		resolution.ClassificationConfidence != 0.97 ||
		!resolution.RequiresProgress {
		t.Fatalf("resolution=%+v", resolution)
	}
	if len(model.inputs) != 1 {
		t.Fatalf("model calls=%d", len(model.inputs))
	}
	prompt := model.inputs[0][1].Content
	for _, want := range []string{
		"示例事件的后台服务还没上线么",
		"1408 SampleEventDisabled",
		"我电脑断线了",
		"prod 环境没上线",
		"task_summary",
		"task_class",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("semantic prompt missing %q: %s", want, prompt)
		}
	}
}

func TestResolverClassifiesFixThenStatusRequestAsResourceHandoff(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 41, 0, time.UTC)
	model := &scriptedModel{reply: `{
		"target_message_id":"om_fix_status",
		"result":"unanswered",
		"confidence":0.96,
		"reason":"the owner has not handled the linked issue fix and status update request",
		"target_intent":"Request to fix the problem in the linked record and update its status after the fix.",
		"response_obligation_quote":"这个问题修复后改下状态哈",
		"task_summary":"Fix the linked record discussed alongside the group invite short-code API and update its status.",
		"task_class":"coding",
		"classification_confidence":0.94,
		"requires_progress":true
	}`}
	resolution, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_fix_status", ChatID: "oc_rd", ChatType: "group",
			SenderID: "ou_sender", Content: "@测试负责人 这个问题修复后改下状态哈",
			Mentions: []domain.Mention{{OpenID: "ou_owner"}}, CreatedAt: base,
		}),
		Messages: []domain.NormalizedEvent{
			{
				MessageID: "om_fix_status", ChatID: "oc_rd", ChatType: "group",
				SenderID: "ou_sender", Content: "@测试负责人 这个问题修复后改下状态哈",
				Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
				CreatedAt: base,
			},
			{
				MessageID: "om_later_api", ChatID: "oc_rd", ChatType: "group",
				SenderID: "ou_other", Content: "接口：/api/sample/perform_action",
				CreatedAt: base.Add(2 * time.Minute),
			},
		},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultUnanswered ||
		resolution.TaskClass != domain.TaskClassResourceHandoff ||
		resolution.RequiresProgress ||
		resolution.TaskSummary != "locate the referenced issue, verify its fix evidence, and update its workflow status" {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverFallsBackForExactRecordStatusHandoffOnModelOutage(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 9, 0, time.UTC)
	modelErr := errors.New("temporary semantic model outage")
	record := domain.NormalizedEvent{
		MessageID: "om_record", ChatID: "oc_rd", ChatType: "group",
		SenderID: "ou_sender", Content: "https://example.larksuite.com/record/shr_bug",
		ResourceURLs: []string{"https://example.larksuite.com/record/shr_bug"},
		CreatedAt:    base,
	}
	target := domain.NormalizedEvent{
		MessageID: "om_fix_status", ChatID: "oc_rd", ChatType: "group",
		RootMessageID: record.MessageID, ReplyToMessageID: record.MessageID,
		SenderID: "ou_sender", SenderType: "user",
		Content:   "@测试负责人 这个问题修复后改下状态哈",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base.Add(32 * time.Second),
	}
	resolution, err := New(
		&scriptedModel{err: modelErr},
		"ou_owner",
	).Resolve(context.Background(), Request{
		Target:        domain.NewWorkItem(target),
		Messages:      []domain.NormalizedEvent{record, target},
		ContextCutoff: base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Result != ResultUnanswered ||
		resolution.TaskClass != domain.TaskClassResourceHandoff ||
		resolution.ClassificationConfidence < 0.85 ||
		resolution.RequiresProgress ||
		resolution.TaskSummary != "locate the referenced issue, verify its fix evidence, and update its workflow status" ||
		len(resolution.ContextMessages) != 2 {
		t.Fatalf("resolution=%+v", resolution)
	}
}

func TestResolverDoesNotFallbackWithoutExactUnansweredRecordHandoff(t *testing.T) {
	base := time.Date(2026, 8, 6, 3, 59, 9, 0, time.UTC)
	record := domain.NormalizedEvent{
		MessageID: "om_record", ChatID: "oc_rd", ChatType: "group",
		SenderID: "ou_sender", Content: "https://example.larksuite.com/record/shr_bug",
		ResourceURLs: []string{"https://example.larksuite.com/record/shr_bug"},
		CreatedAt:    base,
	}
	target := domain.NormalizedEvent{
		MessageID: "om_fix_status", ChatID: "oc_rd", ChatType: "group",
		RootMessageID: record.MessageID, ReplyToMessageID: record.MessageID,
		SenderID: "ou_sender", SenderType: "user",
		Content:   "@测试负责人 这个问题修复后改下状态哈",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: base.Add(32 * time.Second),
	}
	ownerReply := domain.NormalizedEvent{
		MessageID: "om_owner_reply", ChatID: "oc_rd", ChatType: "group",
		RootMessageID: record.MessageID, ReplyToMessageID: target.MessageID,
		SenderID: "ou_owner", SenderType: "user", Content: "已处理并更新状态。",
		CreatedAt: base.Add(time.Minute),
	}
	for _, testCase := range []struct {
		name     string
		messages []domain.NormalizedEvent
	}{
		{
			name:     "record relation is absent",
			messages: []domain.NormalizedEvent{target},
		},
		{
			name:     "owner already replied substantively",
			messages: []domain.NormalizedEvent{record, target, ownerReply},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			modelErr := errors.New("temporary semantic model outage")
			_, err := New(
				&scriptedModel{err: modelErr},
				"ou_owner",
			).Resolve(context.Background(), Request{
				Target:        domain.NewWorkItem(target),
				Messages:      testCase.messages,
				ContextCutoff: base.Add(3 * time.Minute),
			})
			if !errors.Is(err, modelErr) {
				t.Fatalf("err=%v want model outage", err)
			}
		})
	}
}

func TestResolverRejectsMissingContextualTaskClassification(t *testing.T) {
	model := &scriptedModel{reply: `{
		"target_message_id":"om_handoff",
		"result":"unanswered",
		"confidence":0.98,
		"reason":"unanswered"
	}`}
	_, err := New(model, "ou_owner").Resolve(context.Background(), Request{
		Target: domain.NewWorkItem(domain.NormalizedEvent{
			MessageID: "om_handoff", ChatID: "oc_rd", ChatType: "group",
			SenderID: "ou_sender", Content: "@测试负责人 你看看吧",
			Mentions: []domain.Mention{{OpenID: "ou_owner"}},
		}),
	})
	if err == nil {
		t.Fatal("resolver accepted an unanswered target without contextual task classification")
	}
}

package replymatch

import (
	"context"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/domain"
)

type scriptedModel struct {
	reply  string
	inputs [][]*schema.Message
}

func (m *scriptedModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...einomodel.Option,
) (*schema.Message, error) {
	m.inputs = append(m.inputs, input)
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

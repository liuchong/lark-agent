package larkagent_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/app"
	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/poll"
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
	"github.com/liuchong/lark-agent/agent/taskrules"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

type semanticIntegrationModel struct {
	response  string
	responses []string
	err       error
	calls     int
}

func (m *semanticIntegrationModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	response := m.response
	if len(m.responses) > 0 {
		index := m.calls - 1
		if index >= len(m.responses) {
			index = len(m.responses) - 1
		}
		response = m.responses[index]
	}
	return schema.AssistantMessage(response, nil), nil
}

type semanticIntegrationResolver struct {
	store     *storage.Store
	matcher   *replymatch.Resolver
	messages  []domain.NormalizedEvent
	taskRules taskrules.Snapshot
}

type semanticSequenceResolver struct {
	resolutions []replymatch.Resolution
	calls       int
}

func (r *semanticSequenceResolver) Resolve(
	_ context.Context,
	item domain.WorkItem,
) (replymatch.Resolution, error) {
	index := r.calls
	r.calls++
	if index >= len(r.resolutions) {
		index = len(r.resolutions) - 1
	}
	resolution := r.resolutions[index]
	resolution.TargetMessageID = item.Event.MessageID
	resolution.ContextCutoff = time.Now().UTC()
	resolution.ContextMessages = []domain.NormalizedEvent{item.Event}
	return resolution, nil
}

func (r semanticIntegrationResolver) Resolve(
	ctx context.Context,
	item domain.WorkItem,
) (replymatch.Resolution, error) {
	pending, err := r.store.ListPendingDelegatedWork(item.Event.ChatID)
	if err != nil {
		return replymatch.Resolution{}, err
	}
	resolution, err := r.matcher.Resolve(ctx, replymatch.Request{
		Target:        item,
		Pending:       pending,
		Messages:      r.messages,
		ContextCutoff: time.Now().UTC(),
		TaskRules:     r.taskRules,
	})
	if err != nil {
		return replymatch.Resolution{}, err
	}
	if err := r.store.RecordOwnerReplyResolution(item.ID, resolution); err != nil {
		return replymatch.Resolution{}, err
	}
	return resolution, nil
}

type semanticIntegrationBuilder struct {
	calls int
	item  domain.WorkItem
}

func (b *semanticIntegrationBuilder) Build(
	item domain.WorkItem,
) (agentcontext.Bundle, error) {
	b.calls++
	b.item = item
	return agentcontext.Bundle{Event: item.Event, WorkKind: item.WorkKind}, nil
}

type semanticIntegrationDecider struct {
	calls int
}

func (d *semanticIntegrationDecider) Decide(
	context.Context,
	agentcontext.Bundle,
) (domain.Decision, error) {
	d.calls++
	return domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "我先核对了同一会话的后续讨论，目前没有看到测试负责人对这个具体问题的答复。",
		Reason:     "bounded same-chat investigation completed",
	}, nil
}

type semanticIntegrationReplyHandler struct {
	calls int
}

type semanticSequenceDecider struct {
	calls int
}

func (d *semanticSequenceDecider) Decide(
	context.Context,
	agentcontext.Bundle,
) (domain.Decision, error) {
	d.calls++
	return domain.Decision{
		Kind:        domain.DecisionReply,
		Relevance:   domain.RelevanceInferred,
		Confidence:  0.99,
		Risk:        domain.RiskLow,
		ReplyText:   "candidate reply " + string(rune('0'+d.calls)),
		OwnerAction: "owner notice " + string(rune('0'+d.calls)),
		Reason:      "resource handoff checked",
	}, nil
}

type semanticRetryReplyHandler struct {
	calls int
	texts []string
}

func (h *semanticRetryReplyHandler) Handle(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
) (reply.Result, error) {
	h.calls++
	h.texts = append(h.texts, decision.ReplyText)
	if h.calls == 1 {
		return reply.Result{}, errors.New("temporary sender-facing send failure")
	}
	return reply.Result{Action: domain.Action{Status: domain.ActionCompleted}}, nil
}

type semanticIntegrationNotifier struct {
	calls int
	texts []string
}

func (n *semanticIntegrationNotifier) HandleNotification(
	_ context.Context,
	_ domain.WorkItem,
	decision domain.Decision,
	_ string,
) error {
	n.calls++
	n.texts = append(n.texts, decision.OwnerAction)
	return nil
}

type semanticIntegrationProgress struct {
	beginCalls      int
	finalizingCalls int
	completeCalls   int
	blockCalls      int
}

func (p *semanticIntegrationProgress) Begin(
	context.Context,
	domain.WorkItem,
	replymatch.Resolution,
) error {
	p.beginCalls++
	return nil
}

func (p *semanticIntegrationProgress) Finalizing(
	context.Context,
	domain.WorkItem,
) error {
	p.finalizingCalls++
	return nil
}

func (p *semanticIntegrationProgress) Complete(
	context.Context,
	domain.WorkItem,
) error {
	p.completeCalls++
	return nil
}

func (p *semanticIntegrationProgress) Block(
	context.Context,
	domain.WorkItem,
	error,
) error {
	p.blockCalls++
	return nil
}

type ownerOutboundPrivatePollIM struct {
	now time.Time
}

func (f ownerOutboundPrivatePollIM) SearchChats(
	context.Context,
	serviceim.SearchChatsRequest,
) (serviceim.SearchChatsResult, error) {
	return serviceim.SearchChatsResult{}, nil
}

func (f ownerOutboundPrivatePollIM) SearchMessages(
	_ context.Context,
	req serviceim.SearchMessagesRequest,
) (serviceim.SearchMessagesResult, error) {
	if req.IncludeAtMe {
		return serviceim.SearchMessagesResult{}, nil
	}
	return serviceim.SearchMessagesResult{Items: []serviceim.Message{{
		MessageID:         "om_owner_question",
		ChatID:            "oc_human_private",
		ChatType:          "p2p",
		ChatPartnerOpenID: "ou_teammate",
		SenderOpenID:      "ou_owner",
		SenderType:        "user",
		Content:           "感觉你要不要给这个项目配一个 UI，客户端什么的？",
		CreateTime:        f.now.Format(time.RFC3339),
	}}}, nil
}

func (f ownerOutboundPrivatePollIM) BatchGetChats(
	context.Context,
	[]string,
) (map[string]serviceim.Chat, error) {
	return map[string]serviceim.Chat{
		"oc_human_private": {
			ChatID:          "oc_human_private",
			ChatMode:        "p2p",
			P2PTargetOpenID: "ou_teammate",
		},
	}, nil
}

func (h *semanticIntegrationReplyHandler) Handle(
	context.Context,
	domain.WorkItem,
	domain.Decision,
) (reply.Result, error) {
	h.calls++
	return reply.Result{Action: domain.Action{Status: domain.ActionCompleted}}, nil
}

func TestSemanticDelegatedReplyLifecycleAcrossGroupAndPrivateMessages(t *testing.T) {
	t.Run("owner semantic answer suppresses exact group target", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_group_target",
			MessageID: "om_group_target",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 发布日期是哪天？",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_group_target",
			"result":"answered",
			"matched_owner_message_ids":["om_owner_answer"],
			"confidence":0.98,
			"reason":"the owner supplied the requested release date"
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					item.Event,
					{
						MessageID: "om_owner_answer",
						ChatID:    "oc_any_group",
						SenderID:  "ou_owner",
						Content:   "发布日期是 8 月 5 日。",
						CreatedAt: base.Add(time.Minute),
					},
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "owner_semantically_replied" ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v builder=%d decider=%d replier=%d",
				result,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("moderate confidence owner answer with matched messages suppresses exact target", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:     domain.SourcePoll,
			EventID:    "poll:om_5805_shape",
			MessageID:  "om_5805_shape",
			ChatID:     "oc_private",
			ChatType:   "p2p",
			SenderID:   "ou_teammate",
			SenderType: "user",
			Content:    "但会想一下这个场景怎么做",
			CreatedAt:  base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_5805_shape",
			"result":"answered",
			"matched_owner_message_ids":["om_owner_followup_a","om_owner_followup_b"],
			"confidence":0.82,
			"reason":"the owner followed up with concrete design details for how this scenario would work",
			"target_intent":"continuation_of_owner_initiated_discussion"
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID:       "ou_owner",
				Mode:              domain.ModeAuto,
				PrivateReplyScope: domain.PrivateReplyScopeAll,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					item.Event,
					{
						MessageID: "om_owner_followup_a", ChatID: "oc_private",
						ChatType: "p2p", SenderID: "ou_owner",
						Content:   "这个场景我倾向加更多入口，每个入口对应特殊功能。",
						CreatedAt: base.Add(time.Minute),
					},
					{
						MessageID: "om_owner_followup_b", ChatID: "oc_private",
						ChatType: "p2p", SenderID: "ou_owner",
						Content:   "聊天页放一个 tendo 小按钮，点击带上上下文，长按出总结/帮我回复菜单。",
						CreatedAt: base.Add(2 * time.Minute),
					},
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "owner_semantically_replied" ||
			semanticModel.calls != 1 ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v semantic=%d builder=%d decider=%d replier=%d",
				result,
				semanticModel.calls,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("unanswered human private message enters reply workflow", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:     domain.SourcePoll,
			EventID:    "poll:om_private_target",
			MessageID:  "om_private_target",
			ChatID:     "oc_private",
			ChatType:   "p2p",
			SenderID:   "ou_teammate",
			SenderType: "user",
			Content:    "发布包现在可以用了么？",
			CreatedAt:  base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_private_target",
			"result":"unanswered",
			"confidence":0.97,
			"reason":"no owner-authored message answered this private request",
			"target_intent":"request",
			"response_obligation_quote":"发布包现在可以用了么？",
			"task_summary":"确认发布包当前是否可用",
			"task_class":"simple",
			"classification_confidence":0.97,
			"requires_progress":false
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID:       "ou_owner",
				Mode:              domain.ModeAuto,
				PrivateReplyScope: domain.PrivateReplyScopeAll,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{item.Event},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Relevance != domain.RelevancePrivateMessage ||
			builder.calls != 1 || decider.calls != 1 || replier.calls != 1 ||
			builder.item.TaskSummary != "确认发布包当前是否可用" ||
			builder.item.TaskClass != domain.TaskClassSimple ||
			builder.item.InvestigationActive {
			t.Fatalf(
				"result=%+v builder=%+v decider=%d replier=%d",
				result,
				builder,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("answer to owner led private discussion needs no synthetic reply", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:     domain.SourcePoll,
			EventID:    "poll:om_private_answer",
			MessageID:  "om_private_answer",
			ChatID:     "oc_private",
			ChatType:   "p2p",
			SenderID:   "ou_teammate",
			SenderType: "user",
			Content:    "有 UI 和客户端",
			CreatedAt:  base.Add(23 * time.Second),
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_private_answer",
			"result":"no_reply_needed",
			"matched_owner_message_ids":["om_owner_continues"],
			"confidence":0.98,
			"reason":"the target answers the owner's question, adds no request, and the owner continues the discussion"
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID:       "ou_owner",
				Mode:              domain.ModeAuto,
				PrivateReplyScope: domain.PrivateReplyScopeAll,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					{
						MessageID: "om_owner_question", ChatID: "oc_private",
						ChatType: "p2p", SenderID: "ou_owner",
						Content:   "感觉你要不要给这个项目配一个 UI，客户端什么的？",
						CreatedAt: base,
					},
					item.Event,
					{
						MessageID: "om_owner_continues", ChatID: "oc_private",
						ChatType: "p2p", SenderID: "ou_owner",
						Content:   "你们怎么还有这个项目？",
						CreatedAt: base.Add(40 * time.Second),
					},
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "delegated_reply_not_needed" ||
			semanticModel.calls != 1 ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v semantic=%d builder=%d decider=%d replier=%d",
				result,
				semanticModel.calls,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("attributive ni kan de private acknowledgement stays silent", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:     domain.SourcePoll,
			EventID:    "poll:om_private_pr_ack",
			MessageID:  "om_private_pr_ack",
			ChatID:     "oc_private",
			ChatType:   "p2p",
			SenderID:   "ou_teammate",
			SenderType: "user",
			Content:    "哦哦你看的 PR",
			CreatedAt:  base.Add(time.Minute),
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_private_pr_ack",
			"result":"no_reply_needed",
			"confidence":0.96,
			"reason":"the target acknowledges that the screenshot was the PR the owner viewed and adds no request"
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID:       "ou_owner",
				Mode:              domain.ModeAuto,
				PrivateReplyScope: domain.PrivateReplyScopeAll,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					{
						MessageID: "om_owner_image", ChatID: "oc_private",
						ChatType: "p2p", SenderID: "ou_owner", SenderType: "user",
						Content: "[Image]", CreatedAt: base,
					},
					item.Event,
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "delegated_reply_not_needed" ||
			semanticModel.calls != 1 ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v semantic=%d builder=%d decider=%d replier=%d",
				result,
				semanticModel.calls,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("private answer without target obligation cannot become investigation", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:     domain.SourcePoll,
			EventID:    "poll:om_5517_shape",
			MessageID:  "om_5517_shape",
			ChatID:     "oc_private",
			ChatType:   "p2p",
			SenderID:   "ou_teammate",
			SenderType: "user",
			Content:    "当时说要做但没设计详细交互，后面也没看到继续推进",
			CreatedAt:  base.Add(2 * time.Minute),
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_5517_shape",
			"result":"unanswered",
			"confidence":0.92,
			"reason":"the owner has not handled the remembered group member limit",
			"target_intent":"answer",
			"task_summary":"investigate why the group member limit cannot be found",
			"task_class":"coding",
			"classification_confidence":0.90,
			"requires_progress":true
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID:       "ou_owner",
				Mode:              domain.ModeAuto,
				PrivateReplyScope: domain.PrivateReplyScopeAll,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					{
						MessageID: "om_owner_question", ChatID: "oc_private",
						ChatType: "p2p", SenderID: "ou_owner",
						Content:   "那个群成员数量限制为什么找不到？",
						CreatedAt: base,
					},
					item.Event,
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "delegated_reply_not_needed" ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v builder=%d decider=%d replier=%d",
				result,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("private design statement without explicit ask needs no synthetic reply", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:     domain.SourcePoll,
			EventID:    "poll:om_private_design",
			MessageID:  "om_private_design",
			ChatID:     "oc_private",
			ChatType:   "p2p",
			SenderID:   "ou_teammate",
			SenderType: "user",
			Content:    "我看了一下没问题，目前不区分两种示例类型，统一放在一个入口里处理",
			CreatedAt:  base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_private_design",
			"result":"unanswered",
			"confidence":0.72,
			"reason":"the target communicates a design decision",
			"target_intent":"communicate design decision",
			"response_obligation_quote":"目前不区分两种示例类型，统一放在一个入口里处理",
			"task_summary":"adjust sticker and GIF menu design",
			"task_class":"simple",
			"classification_confidence":0.72,
			"requires_progress":false
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID:       "ou_owner",
				Mode:              domain.ModeAuto,
				PrivateReplyScope: domain.PrivateReplyScopeAll,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{item.Event},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "delegated_reply_not_needed" ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v builder=%d decider=%d replier=%d",
				result,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("group owner mention social acknowledgement needs no synthetic reply", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_group_ack",
			MessageID: "om_group_ack",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 [赞]这图有禅意啊",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_group_ack",
			"result":"ambiguous",
			"confidence":0.55,
			"reason":"the target is a thumbs-up and compliment on the owner's image",
			"target_intent":"social acknowledgement / compliment"
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					{
						MessageID: "om_owner_image", ChatID: "oc_any_group",
						ChatType: "group", SenderID: "ou_owner",
						Content: "[图片]", CreatedAt: base.Add(-time.Minute),
					},
					item.Event,
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "delegated_reply_not_needed" ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v builder=%d decider=%d replier=%d",
				result,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("group owner fix then status request cannot be swallowed as no reply", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_group_fix_status",
			MessageID: "om_group_fix_status",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 这个问题修复后改下状态哈",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_group_fix_status",
			"result":"no_reply_needed",
			"confidence":0.90,
			"reason":"The target asks the owner to fix the linked issue and update its status.",
			"target_intent":"Request to fix the problem in the linked record and update its status after the fix."
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{item.Event},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items, err := store.ListWorkItems()
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "owner_reply_resolution_failed" ||
			len(items) != 1 || items[0].Status != domain.StatusWaitingUser ||
			!items[0].NextAttemptAt.After(time.Now().UTC()) ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v items=%+v builder=%d decider=%d replier=%d",
				result,
				items,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("group owner fix then status request becomes resource handoff", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_group_fix_status_simple",
			MessageID: "om_group_fix_status_simple",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 这个问题修复后改下状态哈",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_group_fix_status_simple",
			"result":"unanswered",
			"confidence":0.96,
			"reason":"the owner has not handled the linked issue fix and status update request",
			"target_intent":"Request to fix the problem in the linked record and update its status after the fix.",
			"response_obligation_quote":"这个问题修复后改下状态哈",
			"task_summary":"investigate the group invite short code API and update the record status",
			"task_class":"coding",
			"classification_confidence":0.94,
			"requires_progress":true
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		progress := &semanticIntegrationProgress{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					item.Event,
					{
						MessageID: "om_later_api", ChatID: "oc_any_group",
						ChatType: "group", SenderID: "ou_other",
						Content:   "接口：/api/sample/perform_action",
						CreatedAt: base.Add(2 * time.Minute),
					},
				},
			}, 0.85, 30*time.Second),
			app.WithInvestigationProgressHandler(progress),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if builder.calls != 1 || builder.item.WorkKind != domain.WorkKindResourceHandoff ||
			builder.item.TaskClass != domain.TaskClassResourceHandoff ||
			builder.item.InvestigationActive ||
			decider.calls != 1 || replier.calls != 1 ||
			progress.beginCalls != 0 || progress.finalizingCalls != 0 ||
			progress.completeCalls != 0 || progress.blockCalls != 0 {
			t.Fatalf(
				"result=%+v builder=%d item=%+v decider=%d replier=%d progress=%+v",
				result,
				builder.calls,
				builder.item,
				decider.calls,
				replier.calls,
				progress,
			)
		}
		investigation, ok, err := store.GetDelegatedInvestigation(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("unexpected investigation=%+v", investigation)
		}
	})

	t.Run("exact record status handoff survives semantic model outage", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		record := domain.NormalizedEvent{
			Source: domain.SourcePoll, EventID: "poll:om_record_parent",
			MessageID: "om_record_parent", ChatID: "oc_any_group",
			ChatType: "group", SenderID: "ou_teammate", SenderType: "user",
			Content:      "https://example.larksuite.com/record/shrExampleRecordToken001",
			ResourceURLs: []string{"https://example.larksuite.com/record/shrExampleRecordToken001"},
			CreatedAt:    base,
		}
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source: domain.SourcePoll, EventID: "poll:om_status_model_outage",
			MessageID: "om_status_model_outage", ChatID: "oc_any_group",
			ChatType: "group", RootMessageID: record.MessageID,
			ReplyToMessageID: record.MessageID,
			SenderID:         "ou_teammate", SenderType: "user",
			Content:   "@测试负责人 这个问题修复后改下状态哈",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base.Add(32 * time.Second),
		})
		semanticModel := &semanticIntegrationModel{
			err: errors.New("temporary semantic model outage"),
		}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		progress := &semanticIntegrationProgress{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					record,
					item.Event,
				},
			}, 0.85, 30*time.Second),
			app.WithInvestigationProgressHandler(progress),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Kind != domain.DecisionReply ||
			semanticModel.calls != 1 ||
			builder.calls != 1 ||
			builder.item.WorkKind != domain.WorkKindResourceHandoff ||
			builder.item.TaskClass != domain.TaskClassResourceHandoff ||
			decider.calls != 1 ||
			replier.calls != 1 ||
			progress.beginCalls != 0 ||
			progress.finalizingCalls != 0 ||
			progress.completeCalls != 0 ||
			progress.blockCalls != 0 {
			t.Fatalf(
				"result=%+v semantic=%d builder=%d item=%+v decider=%d replier=%d progress=%+v",
				result,
				semanticModel.calls,
				builder.calls,
				builder.item,
				decider.calls,
				replier.calls,
				progress,
			)
		}
	})

	t.Run("resource handoff send retry reuses candidate without owner notice", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			event domain.NormalizedEvent
		}{
			{
				name: "group direct mention",
				event: domain.NormalizedEvent{
					Source: domain.SourcePoll, EventID: "poll:om_group_fix_status_retry",
					MessageID: "om_group_fix_status_retry", ChatID: "oc_any_group",
					ChatType: "group", SenderID: "ou_teammate", SenderType: "user",
					Content:  "@测试负责人 这个问题修复后改下状态哈",
					Mentions: []domain.Mention{{OpenID: "ou_owner"}},
				},
			},
			{
				name: "private message",
				event: domain.NormalizedEvent{
					Source: domain.SourcePoll, EventID: "poll:om_private_fix_status_retry",
					MessageID: "om_private_fix_status_retry", ChatID: "oc_private",
					ChatType: "p2p", SenderID: "ou_teammate", SenderType: "user",
					Content: "这个问题修复后改下状态哈",
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := openSemanticIntegrationStore(t)
				tc.event.CreatedAt = store.CurrentSession().StartedAt.Add(time.Second)
				item := enqueueDueDelegatedItem(t, store, tc.event)
				semanticModel := &semanticIntegrationModel{response: `{
					"target_message_id":"` + item.Event.MessageID + `",
					"result":"unanswered",
					"confidence":0.96,
					"reason":"the owner has not handled the linked issue status handoff",
					"target_intent":"handoff_status_request",
					"response_obligation_quote":"这个问题修复后改下状态哈",
					"task_summary":"use the linked issue",
					"task_class":"resource_handoff",
					"classification_confidence":0.96,
					"requires_progress":false
				}`}
				builder := &semanticIntegrationBuilder{}
				decider := &semanticSequenceDecider{}
				replier := &semanticRetryReplyHandler{}
				notifier := &semanticIntegrationNotifier{}
				daemon := app.NewDaemon(
					store,
					router.New(router.Config{
						OwnerOpenID: "ou_owner", Mode: domain.ModeAuto,
						ReplyScope:        domain.ReplyScopeAllGroups,
						PrivateReplyScope: domain.PrivateReplyScopeAll,
					}),
					app.WithContextBuilder(builder),
					app.WithDecider(decider),
					app.WithReplyHandler(replier),
					app.WithNotificationHandler(notifier),
					app.WithDelegatedReplyResolver(semanticIntegrationResolver{
						store: store, matcher: replymatch.New(semanticModel, "ou_owner"),
						messages: []domain.NormalizedEvent{item.Event},
					}, 0.85, 30*time.Second),
				)

				if _, err := daemon.RunOnce(context.Background()); err == nil {
					t.Fatal("first sender-facing send should fail")
				}
				if changed, err := store.RetryWorkItems([]int64{item.ID}); err != nil || changed != 1 {
					t.Fatalf("RetryWorkItems changed=%d err=%v", changed, err)
				}
				result, err := daemon.RunOnce(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if result.Decision.Kind != domain.DecisionReply ||
					semanticModel.calls != 2 || builder.calls != 1 || decider.calls != 1 ||
					replier.calls != 2 || notifier.calls != 0 ||
					len(replier.texts) != 2 || replier.texts[0] != replier.texts[1] ||
					len(notifier.texts) != 0 {
					t.Fatalf(
						"result=%+v semantic=%d builder=%d decider=%d replier=%+v notifier=%+v",
						result, semanticModel.calls, builder.calls, decider.calls, replier, notifier,
					)
				}
			})
		}
	})

	t.Run("resource handoff candidate is cancelled when current task class changes", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source: domain.SourcePoll, EventID: "poll:om_resource_reclassified",
			MessageID: "om_resource_reclassified", ChatID: "oc_any_group",
			ChatType: "group", SenderID: "ou_teammate", SenderType: "user",
			Content:  "@测试负责人 这个问题修复后改下状态哈",
			Mentions: []domain.Mention{{OpenID: "ou_owner"}}, CreatedAt: base,
		})
		semanticResolver := &semanticSequenceResolver{resolutions: []replymatch.Resolution{
			{
				Result: replymatch.ResultUnanswered, Confidence: 0.96,
				TaskSummary: "current resource handoff", TaskClass: domain.TaskClassResourceHandoff,
				ClassificationConfidence: 0.96,
			},
			{
				Result: replymatch.ResultUnanswered, Confidence: 0.96,
				TaskSummary: "current coding task", TaskClass: domain.TaskClassCoding,
				ClassificationConfidence: 0.96, RequiresProgress: true,
			},
		}}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticSequenceDecider{}
		replier := &semanticRetryReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner", Mode: domain.ModeAuto,
				ReplyScope: domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticResolver, 0.85, 30*time.Second),
		)
		if _, err := daemon.RunOnce(context.Background()); err == nil {
			t.Fatal("first sender-facing send should fail")
		}
		if changed, err := store.RetryWorkItems([]int64{item.ID}); err != nil || changed != 1 {
			t.Fatalf("RetryWorkItems changed=%d err=%v", changed, err)
		}
		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		candidate, found, err := store.ReadyWorkReplyCandidate(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "candidate_task_class_changed" ||
			semanticResolver.calls != 2 || builder.calls != 1 || decider.calls != 1 ||
			replier.calls != 1 || found {
			t.Fatalf(
				"result=%+v semantic=%d builder=%d decider=%d replier=%+v candidate=%+v found=%t",
				result, semanticResolver.calls, builder.calls, decider.calls, replier, candidate, found,
			)
		}
	})

	t.Run("newer owner mention segment cannot answer or redefine older handoff", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		link := domain.NormalizedEvent{
			MessageID: "om_old_record", ChatID: "oc_any_group", ChatType: "group",
			SenderID:  "ou_requester",
			Content:   "https://example.larksuite.com/record/shrExampleRecordToken001",
			CreatedAt: base,
		}
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:           domain.SourcePoll,
			EventID:          "poll:om_old_fix_status",
			MessageID:        "om_old_fix_status",
			ChatID:           "oc_any_group",
			ChatType:         "group",
			RootMessageID:    link.MessageID,
			ReplyToMessageID: link.MessageID,
			SenderID:         "ou_requester",
			Content:          "@测试负责人 这个问题修复后改下状态哈",
			Mentions:         []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt:        base.Add(32 * time.Second),
		})
		newerRequest := domain.NormalizedEvent{
			MessageID: "om_new_avatar_request", ChatID: "oc_any_group",
			ChatType: "group", SenderID: "ou_other",
			Content:   "另一个无关示例需求尚未完成 @测试负责人",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base.Add(8*time.Hour + 25*time.Minute),
		}
		newerAnswer := domain.NormalizedEvent{
			MessageID: "om_new_avatar_answer", ChatID: "oc_any_group",
			ChatType: "group", SenderID: "ou_owner",
			Content:   "做了，我刚把代码合了，等会儿部署完了我跟你说",
			CreatedAt: base.Add(9*time.Hour + 2*time.Minute),
		}
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_old_fix_status",
			"result":"unanswered",
			"confidence":0.96,
			"reason":"the owner has not handled the linked issue status handoff",
			"target_intent":"handoff_status_request",
			"response_obligation_quote":"这个问题修复后改下状态哈",
			"task_summary":"locate the linked issue, verify its fix, and update its workflow status",
			"task_class":"resource_handoff",
			"classification_confidence":0.96,
			"requires_progress":true
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:   store,
				matcher: replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{
					link, item.Event, newerRequest, newerAnswer,
				},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if builder.calls != 1 ||
			builder.item.WorkKind != domain.WorkKindResourceHandoff ||
			builder.item.TaskClass != domain.TaskClassResourceHandoff ||
			len(builder.item.ResolvedContext) != 2 ||
			decider.calls != 1 || replier.calls != 1 {
			t.Fatalf(
				"result=%+v builder=%d item=%+v decider=%d replier=%d",
				result,
				builder.calls,
				builder.item,
				decider.calls,
				replier.calls,
			)
		}
		for _, message := range builder.item.ResolvedContext {
			if message.MessageID == newerRequest.MessageID ||
				message.MessageID == newerAnswer.MessageID {
				t.Fatalf("unrelated later segment leaked into Agent context: %+v", message)
			}
		}
	})

	t.Run("malformed semantic result fails closed and retries later", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_ambiguous",
			MessageID: "om_ambiguous",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 这个版本能否上线？",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{"result":"yes, probably"}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{item.Event},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		items, err := store.ListWorkItems()
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "owner_reply_resolution_failed" ||
			len(items) != 1 || items[0].Status != domain.StatusWaitingUser ||
			!items[0].NextAttemptAt.After(time.Now().UTC()) ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v items=%+v builder=%d decider=%d replier=%d",
				result,
				items,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("explicit replay ignores completed prior-generation reply", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		if _, err := store.MarkCurrentSessionReady(context.Background()); err != nil {
			t.Fatal(err)
		}
		base := store.CurrentSession().StartedAt.Add(time.Second)
		link := domain.NormalizedEvent{
			MessageID: "om_replay_record",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "https://example.larksuite.com/record/shrExampleRecordToken001",
			CreatedAt: base,
		}
		target := domain.NormalizedEvent{
			Source:           domain.SourcePoll,
			EventID:          "poll:om_replay_handoff",
			MessageID:        "om_replay_handoff",
			ChatID:           "oc_any_group",
			ChatType:         "group",
			RootMessageID:    link.MessageID,
			ReplyToMessageID: link.MessageID,
			SenderID:         "ou_teammate",
			Content:          "@测试负责人 这个问题修复后改下状态哈",
			Mentions:         []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt:        base.Add(time.Second),
		}
		if _, err := store.EnqueueEvent(target); err != nil {
			t.Fatal(err)
		}
		first, ok, err := store.ClaimNext("first-generation")
		if err != nil || !ok {
			t.Fatalf("first=%+v ok=%v err=%v", first, ok, err)
		}
		stale := domain.Decision{
			Kind:        domain.DecisionReply,
			Relevance:   domain.RelevanceDirectMention,
			WorkKind:    domain.WorkKindDirectMention,
			ReplyText:   "上一代错误地回复了示例能力接口。",
			OwnerAction: "上一代错误通知",
			Reason:      "stale unrelated context",
		}
		replyActionID, _, _, completed, err := store.BeginReplyAction(
			context.Background(), first.DedupKey, stale.ReplyText,
		)
		if err != nil || completed {
			t.Fatalf("reply action=%d completed=%v err=%v", replyActionID, completed, err)
		}
		if err := store.CompleteReplyAction(
			context.Background(), replyActionID, "om_stale_sent", "",
		); err != nil {
			t.Fatal(err)
		}
		noticeActionID, _, completed, err := store.BeginPostReplyNotification(
			context.Background(), first.DedupKey, stale,
		)
		if err != nil || completed {
			t.Fatalf("notice action=%d completed=%v err=%v", noticeActionID, completed, err)
		}
		if err := store.CompletePostReplyNotification(
			context.Background(), noticeActionID, "",
		); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(first.ID, stale); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
			WorkItemID:    first.ID,
			ForceTerminal: true,
		}); err != nil {
			t.Fatal(err)
		}

		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_replay_handoff",
			"result":"unanswered",
			"confidence":0.96,
			"reason":"the target assigns the linked issue and asks for its status to be updated after the fix",
			"target_intent":"handoff_status_request",
			"response_obligation_quote":"这个问题修复后改下状态哈",
			"task_summary":"修复 record 链接中记录的问题并在完成后更新状态",
			"task_class":"resource_handoff",
			"classification_confidence":0.96,
			"requires_progress":true
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
				ReplyScope:  domain.ReplyScopeAllGroups,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{link, target},
			}, 0.85, 30*time.Second),
		)

		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if semanticModel.calls != 1 ||
			builder.calls != 1 ||
			builder.item.WorkKind != domain.WorkKindResourceHandoff ||
			builder.item.TaskClass != domain.TaskClassResourceHandoff ||
			decider.calls != 1 ||
			replier.calls != 1 ||
			result.Decision.ReplyText == stale.ReplyText {
			t.Fatalf(
				"result=%+v semantic=%d builder=%d item=%+v decider=%d replier=%d",
				result,
				semanticModel.calls,
				builder.calls,
				builder.item,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("informational group mention does not start the main agent", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_group_info",
			MessageID: "om_group_info",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 流程已更新，抄送相关同事。",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_group_info",
			"result":"answered",
			"confidence":0.62,
			"reason":"group mention without later owner reply",
			"target_intent":"informational_announcement"
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{item.Event},
			}, 0.85, 30*time.Second),
		)
		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Decision.Reason != "delegated_reply_not_needed" ||
			builder.calls != 0 || decider.calls != 0 || replier.calls != 0 {
			t.Fatalf(
				"result=%+v builder=%d decider=%d replier=%d",
				result,
				builder.calls,
				decider.calls,
				replier.calls,
			)
		}
	})

	t.Run("private task rules can create investigation without a message ask", func(t *testing.T) {
		store := openSemanticIntegrationStore(t)
		base := store.CurrentSession().StartedAt.Add(time.Second)
		item := enqueueDueDelegatedItem(t, store, domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:om_group_queue",
			MessageID: "om_group_queue",
			ChatID:    "oc_any_group",
			ChatType:  "group",
			SenderID:  "ou_teammate",
			Content:   "@测试负责人 REVIEW-QUEUE item 7 is ready.",
			Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
			CreatedAt: base,
		})
		semanticModel := &semanticIntegrationModel{response: `{
			"target_message_id":"om_group_queue",
			"result":"unanswered",
			"confidence":0.94,
			"reason":"private task rules require investigation",
			"target_intent":"investigation_request",
			"owner_obligation":"investigate",
			"obligation_source":"task_rules",
			"task_rule_evidence":"Investigate every message that contains REVIEW-QUEUE.",
			"task_summary":"Investigate REVIEW-QUEUE item 7.",
			"task_class":"investigation",
			"classification_confidence":0.91,
			"requires_progress":true
		}`}
		builder := &semanticIntegrationBuilder{}
		decider := &semanticIntegrationDecider{}
		replier := &semanticIntegrationReplyHandler{}
		daemon := app.NewDaemon(
			store,
			router.New(router.Config{
				OwnerOpenID: "ou_owner",
				Mode:        domain.ModeAuto,
			}),
			app.WithContextBuilder(builder),
			app.WithDecider(decider),
			app.WithReplyHandler(replier),
			app.WithDelegatedReplyResolver(semanticIntegrationResolver{
				store:    store,
				matcher:  replymatch.New(semanticModel, "ou_owner"),
				messages: []domain.NormalizedEvent{item.Event},
				taskRules: taskrules.Snapshot{
					Enabled: true,
					Status:  taskrules.StatusOK,
					Body:    "# Rules\nInvestigate every message that contains REVIEW-QUEUE.\n",
					Digest:  "sha256:fixture",
				},
			}, 0.85, 30*time.Second),
		)
		result, err := daemon.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if builder.calls != 1 || decider.calls != 1 || replier.calls != 1 ||
			builder.item.TaskClass != domain.TaskClassInvestigation ||
			result.Decision.Kind != domain.DecisionReply {
			t.Fatalf(
				"result=%+v builder=%+v decider=%d replier=%d",
				result,
				builder,
				decider.calls,
				replier.calls,
			)
		}
	})
}

func TestOwnerAuthoredHumanPrivateMessageIsDroppedBeforeDurableIntake(t *testing.T) {
	store := openSemanticIntegrationStore(t)
	now := store.CurrentSession().StartedAt.Add(time.Minute)
	if err := store.SetPollCursor("messages:all", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	agentRouter := router.New(router.Config{
		OwnerOpenID:       "ou_owner",
		AssistantOpenIDs:  []string{"ou_assistant"},
		Mode:              domain.ModeAuto,
		PrivateReplyScope: domain.PrivateReplyScopeAll,
	})
	livePoller := poll.New(ownerOutboundPrivatePollIM{now: now}, store, poll.Config{
		OwnerOpenID:    "ou_owner",
		IncludePrivate: true,
		Now:            func() time.Time { return now.Add(time.Second) },
		Classify:       agentRouter.Route,
	})

	result, err := livePoller.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted != 0 || len(items) != 0 {
		t.Fatalf("result=%+v items=%+v", result, items)
	}
}

func openSemanticIntegrationStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func enqueueDueDelegatedItem(
	t *testing.T,
	store *storage.Store,
	event domain.NormalizedEvent,
) domain.WorkItem {
	t.Helper()
	item := domain.NewWorkItem(event)
	item.Status = domain.StatusWaitingUser
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	receipt, err := store.RecordWorkIntake(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	item.ID = receipt.WorkItemID
	return item
}

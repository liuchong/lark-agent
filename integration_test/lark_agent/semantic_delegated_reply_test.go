package larkagent_test

import (
	"context"
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
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

type semanticIntegrationModel struct {
	response string
	calls    int
}

func (m *semanticIntegrationModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	return schema.AssistantMessage(m.response, nil), nil
}

type semanticIntegrationResolver struct {
	store    *storage.Store
	matcher  *replymatch.Resolver
	messages []domain.NormalizedEvent
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

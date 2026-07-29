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
	"github.com/liuchong/lark-agent/agent/reply"
	"github.com/liuchong/lark-agent/agent/replymatch"
	"github.com/liuchong/lark-agent/agent/router"
	"github.com/liuchong/lark-agent/agent/storage"
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
}

func (b *semanticIntegrationBuilder) Build(
	item domain.WorkItem,
) (agentcontext.Bundle, error) {
	b.calls++
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
			"reason":"no owner-authored message answered this private request"
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
			builder.calls != 1 || decider.calls != 1 || replier.calls != 1 {
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

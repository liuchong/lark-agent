package poll

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

type fakeIM struct {
	chats           serviceim.SearchChatsResult
	chatResults     map[string]serviceim.SearchChatsResult
	generalMessages serviceim.SearchMessagesResult
	atMessages      serviceim.SearchMessagesResult
	recentMessages  map[string]serviceim.ListRecentMessagesResult
	detailMessages  map[string]serviceim.Message
	generalPages    map[string]serviceim.SearchMessagesResult
	searchCalls     []serviceim.SearchMessagesRequest
	detailCalls     [][]string
}

func (f *fakeIM) SearchChats(_ context.Context, req serviceim.SearchChatsRequest) (serviceim.SearchChatsResult, error) {
	if f.chatResults != nil {
		return f.chatResults[req.Query], nil
	}
	return f.chats, nil
}

func (f *fakeIM) SearchMessages(_ context.Context, req serviceim.SearchMessagesRequest) (serviceim.SearchMessagesResult, error) {
	f.searchCalls = append(f.searchCalls, req)
	if req.IncludeAtMe {
		return f.atMessages, nil
	}
	if f.generalPages != nil {
		return f.generalPages[req.PageToken], nil
	}
	return f.generalMessages, nil
}

func (f *fakeIM) ListRecentMessages(_ context.Context, req serviceim.ListRecentMessagesRequest) (serviceim.ListRecentMessagesResult, error) {
	if f.recentMessages == nil {
		return serviceim.ListRecentMessagesResult{}, nil
	}
	return f.recentMessages[req.ChatID], nil
}

func (f *fakeIM) GetMessages(_ context.Context, messageIDs []string) ([]serviceim.Message, error) {
	f.detailCalls = append(f.detailCalls, append([]string(nil), messageIDs...))
	var messages []serviceim.Message
	for _, messageID := range messageIDs {
		if message, ok := f.detailMessages[messageID]; ok {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

type fakeStore struct {
	cursorSet bool
	cursor    time.Time
	events    []domain.NormalizedEvent
	items     []domain.WorkItem
}

type scopedFakeStore struct {
	cursors map[string]time.Time
	items   []domain.WorkItem
}

func (s *scopedFakeStore) GetPollCursor(scope string) (time.Time, bool, error) {
	value, ok := s.cursors[scope]
	return value, ok, nil
}

func (s *scopedFakeStore) SetPollCursor(scope string, value time.Time) error {
	if s.cursors == nil {
		s.cursors = map[string]time.Time{}
	}
	s.cursors[scope] = value
	return nil
}

func (s *scopedFakeStore) RecordWorkIntake(
	_ context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	s.items = append(s.items, item)
	return domain.IntakeReceipt{Disposition: domain.IntakeAdmitted}, nil
}

func (s *scopedFakeStore) RecordBackfillWorkIntake(
	ctx context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	return s.RecordWorkIntake(ctx, item)
}

type delayedPrivateIM struct {
	generalCalls int
	message      serviceim.Message
	searchCalls  []serviceim.SearchMessagesRequest
}

type failingChatHydrationIM struct {
	*fakeIM
}

func (*failingChatHydrationIM) BatchGetChats(context.Context, []string) (map[string]serviceim.Chat, error) {
	return nil, errors.New("chat metadata unavailable")
}

func (d *delayedPrivateIM) SearchChats(context.Context, serviceim.SearchChatsRequest) (serviceim.SearchChatsResult, error) {
	return serviceim.SearchChatsResult{}, nil
}

func (d *delayedPrivateIM) SearchMessages(_ context.Context, req serviceim.SearchMessagesRequest) (serviceim.SearchMessagesResult, error) {
	d.searchCalls = append(d.searchCalls, req)
	if req.IncludeAtMe {
		return serviceim.SearchMessagesResult{}, nil
	}
	d.generalCalls++
	if d.generalCalls < 2 {
		return serviceim.SearchMessagesResult{}, nil
	}
	return serviceim.SearchMessagesResult{Items: []serviceim.Message{d.message}}, nil
}

func (d *delayedPrivateIM) BatchGetChats(context.Context, []string) (map[string]serviceim.Chat, error) {
	return map[string]serviceim.Chat{
		"oc_private": {
			ChatID: "oc_private", ChatMode: "p2p", P2PTargetOpenID: "ou_bot",
		},
	}, nil
}

func (f *fakeStore) RecordWorkIntake(
	_ context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	f.items = append(f.items, item)
	f.events = append(f.events, item.Event)
	return domain.IntakeReceipt{Disposition: domain.IntakeAdmitted}, nil
}

func (f *fakeStore) RecordBackfillWorkIntake(
	ctx context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	return f.RecordWorkIntake(ctx, item)
}

func (f *fakeStore) GetPollCursor(string) (time.Time, bool, error) {
	return f.cursor, f.cursorSet, nil
}

func (f *fakeStore) SetPollCursor(_ string, cursor time.Time) error {
	f.cursor = cursor
	f.cursorSet = true
	return nil
}

func TestPollerColdStartOnlySetsCursor(t *testing.T) {
	now := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	poller := New(&fakeIM{}, store, Config{OwnerOpenID: "ou_owner", Now: func() time.Time { return now }})
	result, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.ColdStart || !store.cursor.Equal(now) {
		t.Fatalf("result=%+v cursor=%s", result, store.cursor)
	}
	if len(store.events) != 0 {
		t.Fatalf("cold start enqueued events: %+v", store.events)
	}
}

func TestBackfillAdmitsOwnerMentionsWithoutAdvancingPollCursor(t *testing.T) {
	start := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	cursor := end.Add(30 * time.Minute)
	store := &fakeStore{cursorSet: true, cursor: cursor}
	im := &fakeIM{
		chatResults: map[string]serviceim.SearchChatsResult{
			"Example Group": {Items: []serviceim.Chat{{ChatID: "oc_rd", Name: "Example Group", ChatMode: "group"}}},
		},
		atMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{{
			MessageID:    "om_owner_mention",
			ChatID:       "oc_rd",
			SenderOpenID: "ou_teammate",
			SenderType:   "user",
			Content:      "@_user_1 请看下数据库迁移问题",
			Mentions:     []domain.Mention{{Key: "@_user_1", OpenID: "ou_owner"}},
			CreateTime:   start.Add(time.Hour).Format(time.RFC3339),
		}}},
	}
	poller := New(im, store, Config{OwnerOpenID: "ou_owner"})
	result, err := poller.Backfill(context.Background(), BackfillRequest{
		ChatQuery: "Example Group",
		Start:     start,
		End:       end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Seen != 1 || result.Inserted != 1 || len(store.items) != 1 {
		t.Fatalf("result=%+v items=%d", result, len(store.items))
	}
	if !store.cursor.Equal(cursor) {
		t.Fatalf("backfill advanced poll cursor: %s", store.cursor)
	}
	call := im.searchCalls[0]
	if !call.IncludeAtMe ||
		call.Query != "" ||
		call.StartISO != start.Format(time.RFC3339) ||
		call.EndISO != end.Format(time.RFC3339) ||
		len(call.ChatIDs) != 1 || call.ChatIDs[0] != "oc_rd" ||
		len(call.AtChatterIDs) != 1 || call.AtChatterIDs[0] != "ou_owner" {
		t.Fatalf("search call=%+v", call)
	}
	event := store.items[0].Event
	if event.MessageID != "om_owner_mention" ||
		event.ChatName != "Example Group" ||
		!event.MentionsUser("ou_owner") ||
		!event.InTestScope {
		t.Fatalf("event=%+v", event)
	}
}

func TestPollerOverlapRecoversLateIndexedPrivateMessageAfterColdStart(t *testing.T) {
	start := time.Date(2026, 7, 24, 5, 35, 0, 0, time.UTC)
	now := start
	store := &scopedFakeStore{}
	im := &delayedPrivateIM{message: serviceim.Message{
		MessageID: "om_private_late", ChatID: "oc_private",
		SenderOpenID: "ou_owner", Content: "几点了？",
		CreateTime: start.Add(5 * time.Second).Format(time.RFC3339),
	}}
	poller := New(im, store, Config{
		OwnerOpenID: "ou_owner", IncludePrivate: true, IndexLookback: time.Minute,
		Now: func() time.Time { return now },
	})
	if result, err := poller.Poll(context.Background()); err != nil || !result.ColdStart {
		t.Fatalf("cold start result=%+v err=%v", result, err)
	}
	now = start.Add(10 * time.Second)
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = start.Add(20 * time.Second)
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || store.items[0].Event.ChatPartnerID != "ou_bot" {
		t.Fatalf("items=%+v", store.items)
	}
	if got := im.searchCalls[2].StartISO; got != start.Format(time.RFC3339) {
		t.Fatalf("overlap start=%q want=%q", got, start.Format(time.RFC3339))
	}
}

func TestPollerDoesNotAdvanceCursorWhenRequiredChatHydrationFails(t *testing.T) {
	start := time.Date(2026, 7, 24, 5, 35, 0, 0, time.UTC)
	now := start.Add(10 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &failingChatHydrationIM{fakeIM: &fakeIM{
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{{
			MessageID: "om_unknown_chat", ChatID: "oc_unknown",
			SenderOpenID: "ou_owner", Content: "几点了？", CreateTime: now.Format(time.RFC3339),
		}}},
	}}
	_, err := New(im, store, Config{
		OwnerOpenID: "ou_owner", Now: func() time.Time { return now },
	}).Poll(context.Background())
	if err == nil {
		t.Fatal("Poll accepted missing required chat metadata")
	}
	if !store.cursor.Equal(start) || len(store.events) != 0 {
		t.Fatalf("cursor=%s events=%+v", store.cursor, store.events)
	}
}

func TestPollerPersistsClassificationBeforeClaim(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{{
		MessageID: "om_fast", ChatID: "oc_lobster", ChatType: "group",
		SenderOpenID: "owner", Content: "@Agent ping",
	}}}}
	poller := New(im, store, Config{
		OwnerOpenID: "owner", Now: func() time.Time { return start.Add(time.Minute) },
		Classify: func(context.Context, domain.WorkItem) (domain.Decision, error) {
			return domain.Decision{WorkKind: domain.WorkKindFastPath, Priority: domain.PriorityFastPath}, nil
		},
	})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 ||
		store.items[0].WorkKind != domain.WorkKindFastPath ||
		store.items[0].Priority != domain.PriorityFastPath {
		t.Fatalf("items=%+v", store.items)
	}
}

func TestPollerEnqueuesVisibleMessagesAndMarksMentions(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		chats: serviceim.SearchChatsResult{Items: []serviceim.Chat{{ChatID: "oc_rd", Name: "Example Group", ChatMode: "group"}}},
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{
				MessageID:        "om_1",
				ChatID:           "oc_rd",
				ChatType:         "group",
				RootMessageID:    "om_root",
				ReplyToMessageID: "om_parent",
				ThreadID:         "omt_context",
				SenderOpenID:     "ou_a",
				Content:          "@_user_1 看下这个，同步给 @_user_2",
				Mentions: []domain.Mention{
					{Key: "@_user_1", OpenID: "ou_owner", Name: "Owner"},
					{Key: "@_user_2", OpenID: "ou_peer", Name: "高建"},
				},
				CreateTime: now.Format(time.RFC3339),
			},
			{MessageID: "om_2", ChatID: "oc_p2p", ChatType: "p2p", SenderOpenID: "ou_b", Content: "私聊消息", CreateTime: now.Format(time.RFC3339)},
		}},
		atMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{MessageID: "om_1", ChatID: "oc_rd", SenderOpenID: "ou_a", Content: "@_user_1 看下这个，同步给 @_user_2", CreateTime: now.Format(time.RFC3339)},
		}},
	}
	poller := New(im, store, Config{
		OwnerOpenID:    "ou_owner",
		ChatQuery:      "Example Group",
		IncludePrivate: true,
		PageSize:       20,
		Now:            func() time.Time { return now },
	})
	result, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted != 2 || len(store.events) != 2 {
		t.Fatalf("result=%+v events=%+v", result, store.events)
	}
	mentioned := eventByID(store.events, "om_1")
	private := eventByID(store.events, "om_2")
	if !mentioned.MentionsUser("ou_owner") {
		t.Fatalf("mention not marked: %+v", mentioned)
	}
	if len(mentioned.Mentions) != 2 || mentioned.Mentions[1].Key != "@_user_2" || mentioned.Mentions[1].Name != "高建" {
		t.Fatalf("mentions not preserved: %+v", mentioned.Mentions)
	}
	if mentioned.RootMessageID != "om_root" ||
		mentioned.ReplyToMessageID != "om_parent" ||
		mentioned.ThreadID != "omt_context" {
		t.Fatalf("relations not preserved: %+v", mentioned)
	}
	if !mentioned.InTestScope || mentioned.ChatName != "Example Group" {
		t.Fatalf("test scope not marked: %+v", mentioned)
	}
	if private.InTestScope {
		t.Fatalf("p2p unexpectedly marked test scope: %+v", private)
	}
	if !store.cursor.Equal(now) {
		t.Fatalf("cursor=%s want=%s", store.cursor, now)
	}
	if len(im.searchCalls) != 2 || !im.searchCalls[1].IncludeAtMe {
		t.Fatalf("search calls=%+v", im.searchCalls)
	}
}

func TestPollerDiscoversAssistantPrivateChatName(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		chatResults: map[string]serviceim.SearchChatsResult{
			"Lark Agent": {Items: []serviceim.Chat{{ChatID: "oc_bot", Name: "Lark Agent", ChatMode: "p2p"}}},
		},
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{MessageID: "om_owner_bot", ChatID: "oc_bot", ChatType: "p2p", SenderOpenID: "ou_owner", Content: "帮我查接口", CreateTime: now.Format(time.RFC3339)},
		}},
	}
	poller := New(im, store, Config{
		OwnerOpenID:    "ou_owner",
		AssistantNames: []string{"Lark Agent"},
		IncludePrivate: true,
		Now:            func() time.Time { return now },
	})
	_, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	event := eventByID(store.events, "om_owner_bot")
	if event.ChatName != "Lark Agent" || event.ChatType != "p2p" {
		t.Fatalf("event=%+v", event)
	}
}

func TestPollerPreservesPostMessageContent(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{MessageID: "om_post", ChatID: "oc_rd", MsgType: "post", SenderOpenID: "ou_a", Content: "POST /api/sample/items 会访问 SampleDB 吗？", CreateTime: now.Format(time.RFC3339)},
		}},
	}
	poller := New(im, store, Config{OwnerOpenID: "ou_owner", IncludePrivate: true, Now: func() time.Time { return now }})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := eventByID(store.events, "om_post")
	if event.Content != "POST /api/sample/items 会访问 SampleDB 吗？" {
		t.Fatalf("content=%q", event.Content)
	}
}

func TestPollerHydratesEmptySearchMessageContentFromRecentMessages(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{MessageID: "om_empty", ChatID: "oc_rd", SenderOpenID: "ou_a", CreateTime: now.Format(time.RFC3339)},
		}},
		recentMessages: map[string]serviceim.ListRecentMessagesResult{
			"oc_rd": {Items: []serviceim.Message{
				{MessageID: "om_empty", ChatID: "oc_rd", SenderOpenID: "ou_a", Content: "@Owner 需要 示例客户端增加回调类型吗？", CreateTime: now.Format(time.RFC3339)},
			}},
		},
	}
	poller := New(im, store, Config{OwnerOpenID: "ou_owner", IncludePrivate: true, Now: func() time.Time { return now }})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := eventByID(store.events, "om_empty")
	if event.Content != "@Owner 需要 示例客户端增加回调类型吗？" {
		t.Fatalf("content=%q", event.Content)
	}
}

func TestPollerHydratesReplyRelationsFromExactMessageDetails(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{{
			MessageID:    "om_reply",
			ChatID:       "oc_lobster",
			ChatType:     "group",
			SenderOpenID: "ou_owner",
			SenderType:   "user",
			MsgType:      "text",
			Content:      "@Agent 继续分析",
			CreateTime:   now.Format(time.RFC3339),
		}}},
		detailMessages: map[string]serviceim.Message{
			"om_reply": {
				MessageID:        "om_reply",
				ChatID:           "oc_lobster",
				RootMessageID:    "om_root",
				ReplyToMessageID: "om_parent",
				ThreadID:         "omt_backend",
			},
		},
	}
	poller := New(im, store, Config{
		OwnerOpenID:    "ou_owner",
		IncludePrivate: true,
		Now:            func() time.Time { return now },
	})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := eventByID(store.events, "om_reply")
	if len(im.detailCalls) != 1 ||
		event.RootMessageID != "om_root" ||
		event.ReplyToMessageID != "om_parent" ||
		event.ThreadID != "omt_backend" {
		t.Fatalf("detail_calls=%v event=%+v", im.detailCalls, event)
	}
}

func TestPollerHydratesMissingMetadataEvenWhenSearchContentExists(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{
				MessageID:  "om_owner_bot",
				ChatID:     "oc_group",
				Content:    "@_user_1 帮我看一下这个接口",
				Mentions:   []domain.Mention{{Key: "@_user_1", OpenID: "ou_bot", Name: "Assistant Bot"}},
				CreateTime: now.Format(time.RFC3339),
			},
		}},
		recentMessages: map[string]serviceim.ListRecentMessagesResult{
			"oc_group": {Items: []serviceim.Message{
				{
					MessageID:    "om_owner_bot",
					ChatID:       "oc_group",
					ChatType:     "group",
					SenderOpenID: "ou_owner",
					SenderType:   "user",
					Content:      "@_user_1 帮我看一下这个接口",
					CreateTime:   now.Format(time.RFC3339),
				},
			}},
		},
	}
	poller := New(im, store, Config{OwnerOpenID: "ou_owner", IncludePrivate: true, Now: func() time.Time { return now }})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := eventByID(store.events, "om_owner_bot")
	if event.SenderID != "ou_owner" || event.ChatType != "group" || !event.MentionsUser("ou_bot") {
		t.Fatalf("event=%+v", event)
	}
}

func eventByID(events []domain.NormalizedEvent, id string) domain.NormalizedEvent {
	for _, event := range events {
		if event.MessageID == id {
			return event
		}
	}
	return domain.NormalizedEvent{}
}

func TestPollerCanExcludePrivateChats(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{}
	poller := New(im, store, Config{
		OwnerOpenID:    "ou_owner",
		IncludePrivate: false,
		Now:            func() time.Time { return now },
	})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(im.searchCalls) != 2 {
		t.Fatalf("search calls=%+v", im.searchCalls)
	}
	for _, call := range im.searchCalls {
		if call.ChatType != "group" {
			t.Fatalf("private chats not excluded: %+v", im.searchCalls)
		}
	}
}

func TestPollerPaginatesBeforeAdvancingCursor(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		generalPages: map[string]serviceim.SearchMessagesResult{
			"": {
				Items:     []serviceim.Message{{MessageID: "om_1", ChatID: "oc_rd", CreateTime: now.Format(time.RFC3339)}},
				HasMore:   true,
				PageToken: "next",
			},
			"next": {
				Items: []serviceim.Message{{MessageID: "om_2", ChatID: "oc_rd", CreateTime: now.Format(time.RFC3339)}},
			},
		},
	}
	poller := New(im, store, Config{OwnerOpenID: "ou_owner", IncludePrivate: true, Now: func() time.Time { return now }})
	result, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Inserted != 2 || len(store.events) != 2 {
		t.Fatalf("result=%+v events=%+v", result, store.events)
	}
	if len(im.searchCalls) < 3 || im.searchCalls[1].PageToken != "next" {
		t.Fatalf("pagination calls=%+v", im.searchCalls)
	}
	if !store.cursor.Equal(now) {
		t.Fatalf("cursor=%s want=%s", store.cursor, now)
	}
}

func TestPollerParsesMillisecondMessageTime(t *testing.T) {
	start := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	now := start.Add(time.Minute)
	store := &fakeStore{cursorSet: true, cursor: start}
	im := &fakeIM{
		generalMessages: serviceim.SearchMessagesResult{Items: []serviceim.Message{
			{MessageID: "om_ms", ChatID: "oc_rd", CreateTime: "1784752861000"},
		}},
	}
	poller := New(im, store, Config{OwnerOpenID: "ou_owner", IncludePrivate: true, Now: func() time.Time { return now }})
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 || store.events[0].CreatedAt.IsZero() {
		t.Fatalf("events=%+v", store.events)
	}
}

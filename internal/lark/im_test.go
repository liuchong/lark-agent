package lark

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/tools"
)

type fakeCaller struct {
	req      APIRequest
	response interface{}
}

type routingCaller struct {
	requests []APIRequest
	call     func(APIRequest) (interface{}, error)
}

func (f *routingCaller) CallAPI(_ context.Context, req APIRequest) (interface{}, error) {
	f.requests = append(f.requests, req)
	return f.call(req)
}

func TestParseMessagePreservesPrivateChatPartner(t *testing.T) {
	message := parseMessage(map[string]any{
		"message_id": "om_private",
		"chat_id":    "oc_private",
		"chat_type":  "p2p",
		"chat_partner": map[string]any{
			"open_id": "ou_bot",
		},
	})
	if message.ChatPartnerOpenID != "ou_bot" {
		t.Fatalf("message=%+v", message)
	}
}

func TestParseMessagePreservesConversationRelations(t *testing.T) {
	message := parseMessage(map[string]any{
		"message_id": "om_reply",
		"root_id":    "om_root",
		"parent_id":  "om_parent",
		"thread_id":  "omt_thread",
	})
	if message.RootMessageID != "om_root" ||
		message.ReplyToMessageID != "om_parent" ||
		message.ThreadID != "omt_thread" {
		t.Fatalf("message=%+v", message)
	}
}

func TestBatchGetChatsReturnsPrivatePartnerIdentity(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{
			"items": []any{map[string]any{
				"chat_id": "oc_private", "chat_mode": "p2p", "p2p_target_id": "ou_bot",
			}},
		},
	}}
	chats, err := NewService(caller, "ou_owner").BatchGetChats(
		context.Background(), []string{"oc_private"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.Path != "/open-apis/im/v1/chats/batch_query" ||
		caller.req.As != IdentityUser ||
		chats["oc_private"].P2PTargetOpenID != "ou_bot" {
		t.Fatalf("request=%+v chats=%+v", caller.req, chats)
	}
}

func (f *fakeCaller) CallAPI(_ context.Context, req APIRequest) (interface{}, error) {
	f.req = req
	if f.response != nil {
		return f.response, nil
	}
	return map[string]any{
		"data": map[string]any{
			"message_id": "om_reply",
			"chat_id":    "oc_1",
		},
	}, nil
}

func TestReplyAsUserUsesUserIdentity(t *testing.T) {
	caller := &fakeCaller{}
	svc := NewService(caller, "ou_owner")
	result, err := svc.ReplyAsUser(context.Background(), tools.ReplyRequest{
		MessageID:      "om_1",
		Text:           "hello",
		IdempotencyKey: "idem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID != "om_reply" {
		t.Fatalf("result=%+v", result)
	}
	if caller.req.As != IdentityUser {
		t.Fatalf("identity=%q", caller.req.As)
	}
	if caller.req.Path != "/open-apis/im/v1/messages/om_1/reply" {
		t.Fatalf("url=%q", caller.req.Path)
	}
}

func TestReplyAsBotUsesBotIdentity(t *testing.T) {
	caller := &fakeCaller{}
	svc := NewService(caller, "ou_owner")
	result, err := svc.ReplyAsBot(context.Background(), tools.ReplyRequest{
		MessageID:      "om_private",
		Text:           "🤖现在是 05:40。",
		IdempotencyKey: "idem-bot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityBot ||
		caller.req.Path != "/open-apis/im/v1/messages/om_private/reply" ||
		result.MessageID != "om_reply" {
		t.Fatalf("request=%+v result=%+v", caller.req, result)
	}
}

func TestTypingReactionUsesBotIdentityAndReturnedReactionID(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{"reaction_id": "reaction_typing"},
	}}
	svc := NewService(caller, "ou_owner")
	reactionID, err := svc.CreateReactionAsBot(context.Background(), "om_private", "Typing")
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityBot ||
		caller.req.Method != http.MethodPost ||
		caller.req.Path != "/open-apis/im/v1/messages/om_private/reactions" ||
		reactionID != "reaction_typing" {
		t.Fatalf("request=%+v reaction_id=%q", caller.req, reactionID)
	}
	reactionType := caller.req.Data.(map[string]any)["reaction_type"].(map[string]any)
	if reactionType["emoji_type"] != "Typing" {
		t.Fatalf("data=%+v", caller.req.Data)
	}
}

func TestDeleteTypingReactionUsesBotIdentity(t *testing.T) {
	caller := &fakeCaller{}
	svc := NewService(caller, "ou_owner")
	if err := svc.DeleteReactionAsBot(context.Background(), "om_private", "reaction_typing"); err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityBot ||
		caller.req.Method != http.MethodDelete ||
		caller.req.Path != "/open-apis/im/v1/messages/om_private/reactions/reaction_typing" {
		t.Fatalf("request=%+v", caller.req)
	}
}

func TestNotifyOwnerUsesBotIdentity(t *testing.T) {
	caller := &fakeCaller{}
	svc := NewService(caller, "ou_owner")
	if err := svc.NotifyOwner(context.Background(), tools.NotifyRequest{
		Text:           "done",
		IdempotencyKey: "owner-notice-idem",
	}); err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityBot {
		t.Fatalf("identity=%q", caller.req.As)
	}
	if caller.req.Params["receive_id_type"] != "open_id" {
		t.Fatalf("params=%+v", caller.req.Params)
	}
	if caller.req.Data.(map[string]any)["uuid"] != "owner-notice-idem" {
		t.Fatalf("data=%+v", caller.req.Data)
	}
}

func TestMessageWritesRejectOversizedUUIDBeforeAPICall(t *testing.T) {
	oversized := strings.Repeat("a", 51)
	tests := []struct {
		name string
		call func(*Service) error
	}{
		{
			name: "reply",
			call: func(svc *Service) error {
				_, err := svc.ReplyAsBot(context.Background(), tools.ReplyRequest{
					MessageID:      "om_private",
					Text:           "reply",
					IdempotencyKey: oversized,
				})
				return err
			},
		},
		{
			name: "owner notification",
			call: func(svc *Service) error {
				return svc.NotifyOwner(context.Background(), tools.NotifyRequest{
					Text:           "notice",
					IdempotencyKey: oversized,
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := &fakeCaller{}
			err := test.call(NewService(caller, "ou_owner"))
			if err == nil || !strings.Contains(err.Error(), "uuid") {
				t.Fatalf("error=%v", err)
			}
			if caller.req.Method != "" {
				t.Fatalf("oversized uuid reached API: %+v", caller.req)
			}
		})
	}
}

func TestSearchChatsUsesUserIdentity(t *testing.T) {
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{"meta_data": map[string]any{
						"chat_id":   "oc_rd",
						"name":      "Example Group",
						"chat_mode": "group",
					}},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	chats, err := svc.SearchChats(context.Background(), SearchChatsRequest{
		Query:    "Example Group",
		PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityUser {
		t.Fatalf("identity=%q", caller.req.As)
	}
	if caller.req.Method != http.MethodPost || caller.req.Path != "/open-apis/im/v2/chats/search" {
		t.Fatalf("request=%s %s", caller.req.Method, caller.req.Path)
	}
	if caller.req.Params["page_size"] != 10 {
		t.Fatalf("params=%+v", caller.req.Params)
	}
	if len(chats.Items) != 1 || chats.Items[0].ChatID != "oc_rd" || chats.Items[0].Name != "Example Group" {
		t.Fatalf("chats=%+v", chats)
	}
}

func TestSearchChatsUsesRequestedBotIdentity(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{"items": []any{}},
	}}
	svc := NewService(caller, "ou_owner")
	if _, err := svc.SearchChats(context.Background(), SearchChatsRequest{
		Query: "龙虾群",
		As:    IdentityBot,
	}); err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityBot {
		t.Fatalf("identity=%q", caller.req.As)
	}
}

func TestSearchMessagesUsesUserIdentity(t *testing.T) {
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{"meta_data": map[string]any{"message_id": "om_1"}},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	messages, err := svc.SearchMessages(context.Background(), SearchMessagesRequest{
		ChatIDs:        []string{"oc_rd"},
		AtChatterIDs:   []string{"ou_owner"},
		StartISO:       "2026-07-23T02:00:00+08:00",
		EndISO:         "2026-07-23T02:30:00+08:00",
		PageSize:       20,
		IncludeAtMe:    true,
		ChatType:       "group",
		ExcludeBotSend: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityUser {
		t.Fatalf("identity=%q", caller.req.As)
	}
	if caller.req.Method != http.MethodPost || caller.req.Path != "/open-apis/im/v1/messages/search" {
		t.Fatalf("request=%s %s", caller.req.Method, caller.req.Path)
	}
	body, ok := caller.req.Data.(map[string]any)
	if !ok {
		t.Fatalf("body=%T", caller.req.Data)
	}
	filter, ok := body["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter missing: %+v", body)
	}
	if filter["is_at_me"] != true {
		t.Fatalf("filter=%+v", filter)
	}
	if len(messages.Items) != 1 || messages.Items[0].MessageID != "om_1" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestSearchMessagesExtractsPostContent(t *testing.T) {
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{
						"meta_data": map[string]any{
							"message_id": "om_post",
							"msg_type":   "post",
							"content":    `{"title":"","content":[[{"tag":"at","user_name":"Owner"},{"tag":"text","text":" POST /api/sample/items 每次都会访问 SampleDB 吗？"}]]}`,
						},
					},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	messages, err := svc.SearchMessages(context.Background(), SearchMessagesRequest{PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Items) != 1 {
		t.Fatalf("messages=%+v", messages)
	}
	want := "@Owner POST /api/sample/items 每次都会访问 SampleDB 吗？"
	if messages.Items[0].Content != want {
		t.Fatalf("content=%q want=%q", messages.Items[0].Content, want)
	}
}

func TestSearchMessagesPreservesTextMentions(t *testing.T) {
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{
						"meta_data": map[string]any{
							"message_id": "om_mentions",
							"content":    `{"text":"@_user_1 同步给 @_user_2"}`,
							"mentions": []any{
								map[string]any{
									"key":  "@_user_1",
									"name": "Owner",
									"id":   "ou_owner",
								},
								map[string]any{
									"key":  "@_user_2",
									"name": "高建",
									"id": map[string]any{
										"open_id": "ou_gaojian",
									},
								},
							},
						},
					},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	messages, err := svc.SearchMessages(context.Background(), SearchMessagesRequest{PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Items) != 1 || len(messages.Items[0].Mentions) != 2 {
		t.Fatalf("messages=%+v", messages)
	}
	if messages.Items[0].Mentions[0].Key != "@_user_1" ||
		messages.Items[0].Mentions[0].OpenID != "ou_owner" ||
		messages.Items[0].Mentions[1].Key != "@_user_2" ||
		messages.Items[0].Mentions[1].Name != "高建" {
		t.Fatalf("mentions=%+v", messages.Items[0].Mentions)
	}
}

func TestSearchMessagesExtractsStringSenderID(t *testing.T) {
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{
						"meta_data": map[string]any{
							"message_id": "om_1",
							"chat_id":    "oc_1",
							"chat_type":  "group",
							"sender": map[string]any{
								"id":          "ou_owner",
								"sender_type": "user",
							},
							"content": `{"text":"@_user_1 帮我看一下"}`,
						},
					},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	messages, err := svc.SearchMessages(context.Background(), SearchMessagesRequest{PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Items) != 1 || messages.Items[0].SenderOpenID != "ou_owner" || messages.Items[0].ChatType != "group" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestListRecentMessagesUsesUserIdentity(t *testing.T) {
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{"message_id": "om_recent", "chat_id": "oc_rd", "content": `{"text":"hi"}`},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	messages, err := svc.ListRecentMessages(context.Background(), ListRecentMessagesRequest{
		ChatID:   "oc_rd",
		PageSize: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityUser {
		t.Fatalf("identity=%q", caller.req.As)
	}
	if caller.req.Method != http.MethodGet || caller.req.Path != "/open-apis/im/v1/messages" {
		t.Fatalf("request=%s %s", caller.req.Method, caller.req.Path)
	}
	if caller.req.Params["container_id"] != "oc_rd" {
		t.Fatalf("params=%+v", caller.req.Params)
	}
	if len(messages.Items) != 1 || messages.Items[0].MessageID != "om_recent" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestGetMessageContextFindsOwnerReply(t *testing.T) {
	after := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{
						"message_id": "om_owner",
						"chat_id":    "oc_rd",
						"sender": map[string]any{
							"id": map[string]any{"open_id": "ou_owner"},
						},
						"content":     `{"text":"我已经回复了"}`,
						"create_time": after.Add(time.Second).Format(time.RFC3339),
					},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	ctx, err := svc.GetMessageContext(context.Background(), MessageContextRequest{
		ChatID:    "oc_rd",
		MessageID: "om_1",
		Limit:     5,
		After:     after,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.OwnerReplied {
		t.Fatalf("owner reply not detected: %+v", ctx)
	}
}

func TestGetMessageContextIgnoresOwnerMessagesBeforeCurrentMessage(t *testing.T) {
	after := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	caller := &fakeCaller{
		response: map[string]any{
			"data": map[string]any{
				"items": []any{
					map[string]any{
						"message_id": "om_old_owner",
						"chat_id":    "oc_rd",
						"sender": map[string]any{
							"id": map[string]any{"open_id": "ou_owner"},
						},
						"content":     `{"text":"之前说过"}`,
						"create_time": after.Add(-time.Minute).Format(time.RFC3339),
					},
				},
			},
		},
	}
	svc := NewService(caller, "ou_owner")
	ctx, err := svc.GetMessageContext(context.Background(), MessageContextRequest{
		ChatID:    "oc_rd",
		MessageID: "om_1",
		Limit:     5,
		After:     after,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.OwnerReplied {
		t.Fatalf("old owner reply incorrectly detected: %+v", ctx)
	}
}

func TestGetMessageContextUsesQuotedChainAsAuthoritativeContext(t *testing.T) {
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		if req.Path != "/open-apis/im/v1/messages/mget" {
			t.Fatalf("unexpected request=%s %s", req.Method, req.Path)
		}
		if ids, ok := req.Params["message_ids"].([]string); !ok || len(ids) != 3 {
			t.Fatalf("message_ids params=%#v", req.Params["message_ids"])
		}
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id":  "om_parent",
				"chat_id":     "oc_lobster",
				"root_id":     "om_root",
				"parent_id":   "om_root",
				"content":     `{"text":"是 sample-service 里的接口"}`,
				"create_time": "2026-07-24T01:01:00Z",
			},
			map[string]any{
				"message_id":  "om_root",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"POST /api/sample/items 为什么每次访问 SampleDB？"}`,
				"create_time": "2026-07-24T01:00:00Z",
			},
			map[string]any{
				"message_id":  "om_target",
				"chat_id":     "oc_lobster",
				"root_id":     "om_root",
				"parent_id":   "om_parent",
				"content":     `{"text":"为什么？"}`,
				"create_time": "2026-07-24T01:02:00Z",
			},
			map[string]any{
				"message_id":  "om_other_chat",
				"chat_id":     "oc_other",
				"content":     `{"text":"不得跨会话泄露"}`,
				"create_time": "2026-07-24T01:00:30Z",
			},
		}}}, nil
	}}
	svc := NewService(caller, "ou_owner")
	result, err := svc.GetMessageContext(context.Background(), MessageContextRequest{
		ChatID:           "oc_lobster",
		MessageID:        "om_target",
		RootMessageID:    "om_root",
		ReplyToMessageID: "om_parent",
		Limit:            30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Selection.Mode != ContextModeReplyChain || result.Selection.Incomplete {
		t.Fatalf("selection=%+v", result.Selection)
	}
	if got := messageIDs(result.Messages); strings.Join(got, ",") != "om_root,om_parent,om_target" {
		t.Fatalf("messages=%v", got)
	}
}

func TestGetMessageContextRecoversReplyTargetFromSameChatFallback(t *testing.T) {
	for _, test := range []struct {
		name             string
		includeCrossChat bool
	}{
		{name: "target missing from mget"},
		{name: "target returned for another chat", includeCrossChat: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
				calls++
				if req.Path == "/open-apis/im/v1/messages/mget" {
					items := []any{
						map[string]any{
							"message_id":  "om_root",
							"chat_id":     "oc_lobster",
							"content":     `{"text":"根消息"}`,
							"create_time": "2026-07-24T01:00:00Z",
						},
						map[string]any{
							"message_id":  "om_parent",
							"chat_id":     "oc_lobster",
							"root_id":     "om_root",
							"parent_id":   "om_root",
							"content":     `{"text":"直接父消息"}`,
							"create_time": "2026-07-24T01:01:00Z",
						},
					}
					if test.includeCrossChat {
						items = append(items, map[string]any{
							"message_id":  "om_target",
							"chat_id":     "oc_other",
							"content":     `{"text":"不得跨群采用"}`,
							"create_time": "2026-07-24T01:02:00Z",
						})
					}
					return map[string]any{"data": map[string]any{"items": items}}, nil
				}
				return map[string]any{"data": map[string]any{"items": []any{
					map[string]any{
						"message_id":  "om_target",
						"chat_id":     "oc_lobster",
						"root_id":     "om_root",
						"parent_id":   "om_parent",
						"content":     `{"text":"为什么？"}`,
						"create_time": "2026-07-24T01:02:00Z",
					},
				}}}, nil
			}}
			result, err := NewService(caller, "ou_owner").GetMessageContext(
				context.Background(),
				MessageContextRequest{
					ChatID:           "oc_lobster",
					MessageID:        "om_target",
					RootMessageID:    "om_root",
					ReplyToMessageID: "om_parent",
					CreatedAt:        time.Date(2026, 7, 24, 1, 2, 0, 0, time.UTC),
					Limit:            30,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || result.Selection.Mode != ContextModeReplyChain ||
				result.Selection.Incomplete || len(result.Selection.MissingMessageIDs) != 0 {
				t.Fatalf("calls=%d selection=%+v", calls, result.Selection)
			}
			if got := strings.Join(messageIDs(result.Messages), ","); got != "om_root,om_parent,om_target" {
				t.Fatalf("messages=%s", got)
			}
		})
	}
}

func TestGetMessageContextReadsThreadOnlyThroughTarget(t *testing.T) {
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		if req.Path != "/open-apis/im/v1/messages" ||
			req.Params["container_id_type"] != "thread" ||
			req.Params["container_id"] != "omt_backend" {
			t.Fatalf("unexpected request=%+v", req)
		}
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id":  "om_root",
				"chat_id":     "oc_lobster",
				"thread_id":   "omt_backend",
				"content":     `{"text":"POST /api/sample/items"}`,
				"create_time": "2026-07-24T01:00:00Z",
			},
			map[string]any{
				"message_id":  "om_parent",
				"chat_id":     "oc_lobster",
				"thread_id":   "omt_backend",
				"content":     `{"text":"每次都会访问 SampleDB"}`,
				"create_time": "2026-07-24T01:01:00Z",
			},
			map[string]any{
				"message_id":  "om_target",
				"chat_id":     "oc_lobster",
				"thread_id":   "omt_backend",
				"parent_id":   "om_parent",
				"content":     `{"text":"为什么？"}`,
				"create_time": "2026-07-24T01:02:00Z",
			},
			map[string]any{
				"message_id":  "om_future",
				"chat_id":     "oc_lobster",
				"thread_id":   "omt_backend",
				"content":     `{"text":"稍后出现的答案"}`,
				"create_time": "2026-07-24T01:03:00Z",
			},
		}}}, nil
	}}
	result, err := NewService(caller, "ou_owner").GetMessageContext(
		context.Background(),
		MessageContextRequest{
			ChatID:           "oc_lobster",
			MessageID:        "om_target",
			RootMessageID:    "om_root",
			ReplyToMessageID: "om_parent",
			ThreadID:         "omt_backend",
			CreatedAt:        time.Date(2026, 7, 24, 1, 2, 0, 0, time.UTC),
			Limit:            30,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selection.Mode != ContextModeThread {
		t.Fatalf("selection=%+v", result.Selection)
	}
	if got := strings.Join(messageIDs(result.Messages), ","); got != "om_root,om_parent,om_target" {
		t.Fatalf("messages=%s", got)
	}
}

func TestGetMessageContextExcludesFutureNearbyMessages(t *testing.T) {
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id":  "om_future",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"future"}`,
				"create_time": "2026-07-24T01:03:00Z",
			},
			map[string]any{
				"message_id":  "om_target",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"现在的问题"}`,
				"create_time": "2026-07-24T01:02:00Z",
			},
			map[string]any{
				"message_id":  "om_previous",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"POST /api/sample/items"}`,
				"create_time": "2026-07-24T01:01:00Z",
			},
		}}}, nil
	}}
	result, err := NewService(caller, "ou_owner").GetMessageContext(
		context.Background(),
		MessageContextRequest{
			ChatID:    "oc_lobster",
			MessageID: "om_target",
			Limit:     30,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selection.Mode != ContextModeAdjacent {
		t.Fatalf("selection=%+v", result.Selection)
	}
	if got := strings.Join(messageIDs(result.Messages), ","); got != "om_previous,om_target" {
		t.Fatalf("messages=%s", got)
	}
}

func TestGetMessageContextFallsBackWhenQuotedParentCannotBeRead(t *testing.T) {
	calls := 0
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		calls++
		if req.Path == "/open-apis/im/v1/messages/mget" {
			return nil, errors.New("quoted parent unavailable")
		}
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id":  "om_target",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"这个接口呢？"}`,
				"create_time": "2026-07-24T01:02:00Z",
			},
			map[string]any{
				"message_id":  "om_nearby",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"附近上下文"}`,
				"create_time": "2026-07-24T01:01:00Z",
			},
		}}}, nil
	}}
	result, err := NewService(caller, "ou_owner").GetMessageContext(
		context.Background(),
		MessageContextRequest{
			ChatID:           "oc_lobster",
			MessageID:        "om_target",
			ReplyToMessageID: "om_missing",
			CreatedAt:        time.Date(2026, 7, 24, 1, 2, 0, 0, time.UTC),
			Limit:            30,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !result.Selection.Incomplete ||
		result.Selection.Mode != ContextModeAdjacent ||
		strings.Join(result.Selection.MissingMessageIDs, ",") != "om_missing" {
		t.Fatalf("calls=%d selection=%+v", calls, result.Selection)
	}
	if got := strings.Join(messageIDs(result.Messages), ","); got != "om_nearby,om_target" {
		t.Fatalf("messages=%s", got)
	}
}

func TestGetMessageContextMarksCrossChatRootAsMissing(t *testing.T) {
	calls := 0
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		calls++
		if req.Path == "/open-apis/im/v1/messages/mget" {
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id":  "om_parent",
					"chat_id":     "oc_lobster",
					"root_id":     "om_root",
					"parent_id":   "om_root",
					"content":     `{"text":"直接父消息"}`,
					"create_time": "2026-07-24T01:01:00Z",
				},
				map[string]any{
					"message_id":  "om_root",
					"chat_id":     "oc_other",
					"content":     `{"text":"另一个群的根消息"}`,
					"create_time": "2026-07-24T01:00:00Z",
				},
				map[string]any{
					"message_id":  "om_target",
					"chat_id":     "oc_lobster",
					"root_id":     "om_root",
					"parent_id":   "om_parent",
					"content":     `{"text":"为什么？"}`,
					"create_time": "2026-07-24T01:02:00Z",
				},
			}}}, nil
		}
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id":  "om_target",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"为什么？"}`,
				"create_time": "2026-07-24T01:02:00Z",
			},
			map[string]any{
				"message_id":  "om_nearby",
				"chat_id":     "oc_lobster",
				"content":     `{"text":"同群临近上下文"}`,
				"create_time": "2026-07-24T01:01:30Z",
			},
		}}}, nil
	}}
	result, err := NewService(caller, "ou_owner").GetMessageContext(
		context.Background(),
		MessageContextRequest{
			ChatID:           "oc_lobster",
			MessageID:        "om_target",
			RootMessageID:    "om_root",
			ReplyToMessageID: "om_parent",
			CreatedAt:        time.Date(2026, 7, 24, 1, 2, 0, 0, time.UTC),
			Limit:            30,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !result.Selection.Incomplete ||
		result.Selection.Mode != ContextModeAdjacent ||
		strings.Join(result.Selection.MissingMessageIDs, ",") != "om_root" {
		t.Fatalf("calls=%d selection=%+v", calls, result.Selection)
	}
	if got := strings.Join(messageIDs(result.Messages), ","); got != "om_nearby,om_target" {
		t.Fatalf("messages=%s", got)
	}
}

func TestGetMessageContextAddsAdjacentFallbackToIncompleteThread(t *testing.T) {
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		if req.Params["container_id_type"] == "thread" {
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id":  "om_root",
					"chat_id":     "oc_lobster",
					"thread_id":   "omt_backend",
					"content":     `{"text":"thread root"}`,
					"create_time": "2026-07-24T01:00:00Z",
				},
			}}}, nil
		}
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{
				"message_id":  "om_target",
				"chat_id":     "oc_lobster",
				"thread_id":   "omt_backend",
				"content":     `{"text":"current message"}`,
				"create_time": "2026-07-24T01:02:00Z",
			},
		}}}, nil
	}}
	result, err := NewService(caller, "ou_owner").GetMessageContext(
		context.Background(),
		MessageContextRequest{
			ChatID:        "oc_lobster",
			MessageID:     "om_target",
			RootMessageID: "om_root",
			ThreadID:      "omt_backend",
			CreatedAt:     time.Date(2026, 7, 24, 1, 2, 0, 0, time.UTC),
			Limit:         30,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selection.Mode != ContextModeThread ||
		!result.Selection.Incomplete ||
		!strings.Contains(result.Selection.Reason, "adjacent messages were added") {
		t.Fatalf("selection=%+v", result.Selection)
	}
	if got := strings.Join(messageIDs(result.Messages), ","); got != "om_root,om_target" {
		t.Fatalf("messages=%s", got)
	}
}

func TestCompactMessagesPinsRootParentAndTarget(t *testing.T) {
	messages := make([]Message, 0, 35)
	for i := 0; i < 35; i++ {
		messages = append(messages, Message{
			MessageID:  "om_" + strconv.Itoa(i),
			CreateTime: time.Date(2026, 7, 24, 1, 0, i, 0, time.UTC).Format(time.RFC3339),
		})
	}
	compacted, truncated := compactMessages(messages, MessageContextRequest{
		MessageID:        "om_34",
		RootMessageID:    "om_0",
		ReplyToMessageID: "om_1",
	}, 30)
	if !truncated || len(compacted) != 30 {
		t.Fatalf("truncated=%v messages=%d", truncated, len(compacted))
	}
	for _, pinned := range []string{"om_0", "om_1", "om_34"} {
		if !containsMessage(compacted, pinned) {
			t.Fatalf("pinned message %s missing: %v", pinned, messageIDs(compacted))
		}
	}
}

func messageIDs(messages []Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.MessageID)
	}
	return ids
}

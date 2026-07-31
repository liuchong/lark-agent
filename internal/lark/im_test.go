package lark

import (
	"context"
	"errors"
	"net/http"
	"reflect"
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

type resourceCaller struct {
	routingCaller
	resources map[string]MessageResource
}

func (f *resourceCaller) GetMessageResource(
	_ context.Context,
	req MessageResourceRequest,
) (MessageResource, error) {
	return f.resources[req.MessageID+":"+req.FileKey], nil
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

func TestParseMessagePreservesImageContextMetadata(t *testing.T) {
	message := parseMessage(map[string]any{
		"message_id": "om_image",
		"chat_id":    "oc_group",
		"msg_type":   "image",
		"sender": map[string]any{
			"id":          map[string]any{"open_id": "ou_sender"},
			"name":        "Ada",
			"sender_type": "user",
		},
		"content": `{"image_key":"img_v2_evidence"}`,
	})

	t.Run("sender display name", func(t *testing.T) {
		field := reflect.ValueOf(message).FieldByName("SenderDisplayName")
		if !field.IsValid() || field.Kind() != reflect.String || field.String() != "Ada" {
			t.Fatalf("sender display name was not preserved: message=%+v", message)
		}
	})

	t.Run("non-empty text placeholder", func(t *testing.T) {
		if strings.TrimSpace(message.Content) == "" {
			t.Fatalf("image message must retain a non-empty text placeholder: message=%+v", message)
		}
	})

	t.Run("typed image attachment", func(t *testing.T) {
		attachments := reflect.ValueOf(message).FieldByName("Attachments")
		if !attachments.IsValid() || attachments.Kind() != reflect.Slice || attachments.Len() != 1 {
			t.Fatalf("image message must retain one typed attachment: message=%+v", message)
		}
		attachment := attachments.Index(0)
		if attachment.Kind() == reflect.Pointer {
			attachment = attachment.Elem()
		}
		if !attachment.IsValid() || attachment.Kind() != reflect.Struct {
			t.Fatalf("attachment must be a typed struct: attachment=%v", attachments.Index(0))
		}
		typeField := attachment.FieldByName("Type")
		if !typeField.IsValid() {
			typeField = attachment.FieldByName("Kind")
		}
		keyField := attachment.FieldByName("Key")
		if !keyField.IsValid() {
			keyField = attachment.FieldByName("ImageKey")
		}
		if !typeField.IsValid() || typeField.Kind() != reflect.String || typeField.String() != "image" ||
			!keyField.IsValid() || keyField.Kind() != reflect.String ||
			keyField.String() != "img_v2_evidence" {
			t.Fatalf("attachment lost image type or key: attachment=%+v", attachment.Interface())
		}
	})
}

func TestHydrateContextImagesIsBoundedAndMarksUnreadableEvidence(t *testing.T) {
	caller := &resourceCaller{resources: map[string]MessageResource{
		"om_one:img_one": {
			Data:     []byte("\x89PNG\r\n\x1a\nsmall"),
			FileName: "one.png",
		},
		"om_two:img_two": {
			Data:     []byte(strings.Repeat("x", 33)),
			FileName: "two.png",
		},
	}}
	service := NewService(caller, "ou_owner")
	messages := []Message{
		{
			MessageID: "om_one",
			Attachments: []MessageAttachment{{
				Type: "image", Key: "img_one",
			}},
		},
		{
			MessageID: "om_two",
			Attachments: []MessageAttachment{{
				Type: "image", Key: "img_two",
			}},
		},
		{
			MessageID: "om_three",
			Attachments: []MessageAttachment{{
				Type: "image", Key: "img_three",
			}},
		},
	}

	got := service.HydrateContextImages(
		context.Background(),
		messages,
		ImageHydrationLimits{MaxImages: 2, MaxImageBytes: 32, MaxTotalBytes: 64},
	)
	if attachment := got[0].Attachments[0]; !attachment.Readable ||
		attachment.MediaType != "image/png" ||
		len(attachment.Data) == 0 {
		t.Fatalf("first attachment=%+v", attachment)
	}
	if attachment := got[1].Attachments[0]; attachment.Readable ||
		attachment.UnreadableReason != "image_exceeds_size_limit" ||
		len(attachment.Data) != 0 {
		t.Fatalf("second attachment=%+v", attachment)
	}
	if attachment := got[2].Attachments[0]; attachment.Readable ||
		attachment.UnreadableReason != "image_count_limit_reached" {
		t.Fatalf("third attachment=%+v", attachment)
	}
}

func TestParseMessageExtractsTrustedMarkerFromLocalizedPost(t *testing.T) {
	marker := "[lark-agent-github-ref:v1:synthetic]"
	message := parseMessage(map[string]any{
		"message_id": "om_notification",
		"chat_id":    "oc_synthetic",
		"msg_type":   "post",
		"sender": map[string]any{
			"id":          "cli_current",
			"id_type":     "app_id",
			"sender_type": "app",
		},
		"body": map[string]any{
			"content": `{"en_us":{"title":"GitHub failure","content":[[{"tag":"text","text":"Status: failure"}],[{"tag":"text","text":"` + marker + `"}]]}}`,
		},
	})
	if message.SenderOpenID != "cli_current" || message.SenderType != "app" {
		t.Fatalf("sender=%q type=%q", message.SenderOpenID, message.SenderType)
	}
	if !strings.Contains(message.Content, marker) {
		t.Fatalf("localized post lost trusted marker: %q", message.Content)
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

func TestSendMessageAsBotUsesChatIdentityAndReturnsTypedResult(t *testing.T) {
	caller := &fakeCaller{response: map[string]any{
		"data": map[string]any{
			"message_id": "om_notification",
			"chat_id":    "oc_synthetic",
		},
	}}
	svc := NewService(caller, "ou_owner")
	result, err := svc.SendMessageAsBot(context.Background(), SendMessageRequest{
		ChatID:         "oc_synthetic",
		MessageType:    "post",
		Content:        `{"zh_cn":{"title":"CI failed","content":[[{"tag":"text","text":"failure"}]]}}`,
		IdempotencyKey: "gh-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.req.As != IdentityBot ||
		caller.req.Path != "/open-apis/im/v1/messages" ||
		caller.req.Params["receive_id_type"] != "chat_id" {
		t.Fatalf("request=%+v", caller.req)
	}
	body := caller.req.Data.(map[string]any)
	if body["receive_id"] != "oc_synthetic" ||
		body["msg_type"] != "post" ||
		body["content"] == "" ||
		body["uuid"] != "gh-1234" {
		t.Fatalf("body=%+v", body)
	}
	if result.MessageID != "om_notification" || result.ChatID != "oc_synthetic" {
		t.Fatalf("result=%+v", result)
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
		{
			name: "chat notification",
			call: func(svc *Service) error {
				_, err := svc.SendMessageAsBot(context.Background(), SendMessageRequest{
					ChatID:         "oc_synthetic",
					MessageType:    "post",
					Content:        `{}`,
					IdempotencyKey: oversized,
				})
				return err
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
		Query: "Acceptance Group",
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

func TestGetSemanticReplyContextPaginatesThroughTargetAndLaterDiscussion(t *testing.T) {
	base := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		switch req.Path {
		case "/open-apis/im/v1/messages/mget":
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id": "om_target", "chat_id": "oc_group",
					"content":     `{"text":"发布日期是哪天？"}`,
					"create_time": base.Format(time.RFC3339),
				},
			}}}, nil
		case "/open-apis/im/v1/messages":
			if req.Params["page_token"] == "page_2" {
				return map[string]any{"data": map[string]any{"items": []any{
					map[string]any{
						"message_id": "om_target", "chat_id": "oc_group",
						"content":     `{"text":"发布日期是哪天？"}`,
						"create_time": base.Format(time.RFC3339),
					},
				}}}, nil
			}
			return map[string]any{"data": map[string]any{
				"has_more": true, "page_token": "page_2",
				"items": []any{
					map[string]any{
						"message_id": "om_owner", "chat_id": "oc_group",
						"sender":      map[string]any{"id": map[string]any{"open_id": "ou_owner"}},
						"content":     `{"text":"我先确认一下发布计划。"}`,
						"create_time": base.Add(time.Minute).Format(time.RFC3339),
					},
				},
			}}, nil
		default:
			t.Fatalf("unexpected request=%s %s", req.Method, req.Path)
			return nil, nil
		}
	}}

	result, err := NewService(caller, "ou_owner").GetSemanticReplyContext(
		context.Background(),
		SemanticReplyContextRequest{
			ChatID:          "oc_group",
			TargetMessageID: "om_target",
			Since:           base,
			MaxMessages:     10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Withdrawn || result.Incomplete || len(result.Messages) != 2 ||
		result.Messages[0].MessageID != "om_target" ||
		result.Messages[1].MessageID != "om_owner" {
		t.Fatalf("result=%+v", result)
	}
	if result.ContextCutoff.Before(base.Add(time.Minute)) {
		t.Fatalf("context cutoff=%s", result.ContextCutoff)
	}
}

func TestGetSemanticReplyContextIncludesPreTargetWindowTargetAndLaterClarification(t *testing.T) {
	targetTime := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		switch req.Path {
		case "/open-apis/im/v1/messages/mget":
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id": "om_target", "chat_id": "oc_group",
					"content":     `{"text":"Please investigate this."}`,
					"create_time": targetTime.Format(time.RFC3339),
				},
			}}}, nil
		case "/open-apis/im/v1/messages":
			if req.Params["page_token"] == "older" {
				return map[string]any{"data": map[string]any{"items": []any{
					map[string]any{
						"message_id": "om_context", "chat_id": "oc_group",
						"content":     `{"text":"Production message edits return 1408."}`,
						"create_time": targetTime.Add(-2*time.Minute - 30*time.Second).Format(time.RFC3339),
					},
					map[string]any{
						"message_id": "om_too_old", "chat_id": "oc_group",
						"content":     `{"text":"Unrelated older discussion."}`,
						"create_time": targetTime.Add(-3*time.Minute - time.Second).Format(time.RFC3339),
					},
				}}}, nil
			}
			return map[string]any{"data": map[string]any{
				"has_more": true, "page_token": "older",
				"items": []any{
					map[string]any{
						"message_id": "om_clarification", "chat_id": "oc_group",
						"content":     `{"text":"Clarification: production is not deployed."}`,
						"create_time": targetTime.Add(2 * time.Minute).Format(time.RFC3339),
					},
					map[string]any{
						"message_id": "om_target", "chat_id": "oc_group",
						"content":     `{"text":"Please investigate this."}`,
						"create_time": targetTime.Format(time.RFC3339),
					},
				},
			}}, nil
		default:
			t.Fatalf("unexpected request=%s %s", req.Method, req.Path)
			return nil, nil
		}
	}}

	result, err := NewService(caller, "ou_owner").GetSemanticReplyContext(
		context.Background(),
		SemanticReplyContextRequest{
			ChatID:          "oc_group",
			TargetMessageID: "om_target",
			Since:           targetTime.Add(-3 * time.Minute),
			MaxMessages:     20,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messageIDs(result.Messages), ","); got != "om_context,om_target,om_clarification" {
		t.Fatalf("shared context message IDs=%s, want pre-target,target,clarification", got)
	}
	if result.ContextCutoff.Before(targetTime.Add(2 * time.Minute)) {
		t.Fatalf("context cutoff=%s does not cover clarification", result.ContextCutoff)
	}
}

func TestGetSemanticReplyContextIncludesRootOutsideAdjacentWindow(t *testing.T) {
	targetTime := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	mgetCalls := 0
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		switch req.Path {
		case "/open-apis/im/v1/messages/mget":
			mgetCalls++
			if mgetCalls == 1 {
				return map[string]any{"data": map[string]any{"items": []any{
					map[string]any{
						"message_id": "om_target", "chat_id": "oc_group",
						"root_id":     "om_root",
						"content":     `{"text":"@测试负责人 你看一下"}`,
						"create_time": targetTime.Format(time.RFC3339),
					},
				}}}, nil
			}
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id": "om_root", "chat_id": "oc_group",
					"content":     `{"text":"生产示例事件返回 1408 SampleEventDisabled"}`,
					"create_time": targetTime.Add(-20 * time.Minute).Format(time.RFC3339),
				},
				map[string]any{
					"message_id": "om_target", "chat_id": "oc_group",
					"root_id":     "om_root",
					"content":     `{"text":"@测试负责人 你看一下"}`,
					"create_time": targetTime.Format(time.RFC3339),
				},
			}}}, nil
		case "/open-apis/im/v1/messages":
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id": "om_target", "chat_id": "oc_group",
					"root_id":     "om_root",
					"content":     `{"text":"@测试负责人 你看一下"}`,
					"create_time": targetTime.Format(time.RFC3339),
				},
			}}}, nil
		default:
			t.Fatalf("unexpected request=%s %s", req.Method, req.Path)
			return nil, nil
		}
	}}

	result, err := NewService(caller, "ou_owner").GetSemanticReplyContext(
		context.Background(),
		SemanticReplyContextRequest{
			ChatID:          "oc_group",
			TargetMessageID: "om_target",
			Since:           targetTime.Add(-3 * time.Minute),
			MaxMessages:     20,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Incomplete ||
		strings.Join(messageIDs(result.Messages), ",") != "om_root,om_target" {
		t.Fatalf("result=%+v", result)
	}
	if mgetCalls != 2 {
		t.Fatalf("mget calls=%d, want target plus relation lookup", mgetCalls)
	}
}

func TestGetSemanticReplyContextMarksBoundedWindowIncomplete(t *testing.T) {
	base := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		if req.Path == "/open-apis/im/v1/messages/mget" {
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{
					"message_id": "om_target", "chat_id": "oc_group",
					"create_time": base.Format(time.RFC3339),
				},
			}}}, nil
		}
		return map[string]any{"data": map[string]any{
			"has_more": true, "page_token": "more",
			"items": []any{
				map[string]any{
					"message_id": "om_new", "chat_id": "oc_group",
					"create_time": base.Add(time.Minute).Format(time.RFC3339),
				},
			},
		}}, nil
	}}

	result, err := NewService(caller, "ou_owner").GetSemanticReplyContext(
		context.Background(),
		SemanticReplyContextRequest{
			ChatID:          "oc_group",
			TargetMessageID: "om_target",
			Since:           base,
			MaxMessages:     1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Incomplete || result.Withdrawn {
		t.Fatalf("result=%+v", result)
	}
}

func TestGetSemanticReplyContextTreatsMissingExactTargetAsWithdrawn(t *testing.T) {
	caller := &routingCaller{call: func(req APIRequest) (interface{}, error) {
		if req.Path != "/open-apis/im/v1/messages/mget" {
			t.Fatalf("unexpected request=%s %s", req.Method, req.Path)
		}
		return map[string]any{"data": map[string]any{"items": []any{}}}, nil
	}}

	result, err := NewService(caller, "ou_owner").GetSemanticReplyContext(
		context.Background(),
		SemanticReplyContextRequest{
			ChatID:          "oc_group",
			TargetMessageID: "om_missing",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Withdrawn || result.Incomplete || len(result.Messages) != 0 {
		t.Fatalf("result=%+v", result)
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

func TestCompactMessagesDropsUnreferencedAppNoise(t *testing.T) {
	messages := []Message{
		{MessageID: "om_human_1", SenderType: "user", Content: "请看审核链路", CreateTime: "2026-07-24T01:00:00Z"},
		{MessageID: "om_app_1", SenderType: "app", Content: "deployment succeeded", CreateTime: "2026-07-24T01:00:01Z"},
		{MessageID: "om_bot_1", SenderType: "bot", Content: "pull request merged", CreateTime: "2026-07-24T01:00:02Z"},
		{MessageID: "om_parent", SenderType: "app", Content: "explicitly referenced build", CreateTime: "2026-07-24T01:00:03Z"},
		{MessageID: "om_target", SenderType: "user", Content: "@Owner 请初步调研", CreateTime: "2026-07-24T01:00:04Z"},
	}
	compacted, _ := compactMessages(messages, MessageContextRequest{
		MessageID:        "om_target",
		ReplyToMessageID: "om_parent",
	}, 30)
	if containsMessage(compacted, "om_app_1") || containsMessage(compacted, "om_bot_1") {
		t.Fatalf("unreferenced app noise remained: %v", messageIDs(compacted))
	}
	for _, want := range []string{"om_human_1", "om_parent", "om_target"} {
		if !containsMessage(compacted, want) {
			t.Fatalf("wanted message %s missing: %v", want, messageIDs(compacted))
		}
	}
}

func TestCompactMessagesKeepsCurrentAssistantPrivateContextWhenRequested(t *testing.T) {
	messages := []Message{
		{MessageID: "om_owner_1", SenderType: "user", Content: "前一个问题", CreateTime: "2026-07-31T00:50:00Z"},
		{MessageID: "om_assistant_notice", SenderType: "app", Content: "审批 #453，批准命令如下", CreateTime: "2026-07-31T00:51:00Z"},
		{MessageID: "om_owner_confirm", SenderType: "user", Content: "确认", CreateTime: "2026-07-31T00:52:00Z"},
	}
	compacted, truncated := compactMessages(messages, MessageContextRequest{
		MessageID:          "om_owner_confirm",
		IncludeAppMessages: true,
	}, 30)
	if truncated {
		t.Fatalf("trusted private context should not be marked truncated: %v", messageIDs(compacted))
	}
	for _, want := range []string{"om_owner_1", "om_assistant_notice", "om_owner_confirm"} {
		if !containsMessage(compacted, want) {
			t.Fatalf("wanted message %s missing: %v", want, messageIDs(compacted))
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

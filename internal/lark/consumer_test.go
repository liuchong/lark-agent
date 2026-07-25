package lark

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestProjectMessageEventRequiresSDKFields(t *testing.T) {
	event := sdkMessageEvent()
	event.Event.Message.MessageId = nil
	if _, err := projectMessageEvent(event); err == nil ||
		!strings.Contains(err.Error(), "message_id") {
		t.Fatalf("expected missing message_id error, got %v", err)
	}
}

func TestProjectMessageEventPreservesTypedFields(t *testing.T) {
	event := sdkMessageEvent()
	envelope, err := projectMessageEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.EventID != "evt_1" ||
		envelope.MessageID != "om_1" ||
		envelope.ChatID != "oc_1" ||
		envelope.SenderID != "ou_owner" ||
		envelope.Content != "hello" {
		t.Fatalf("envelope=%+v", envelope)
	}
}

func TestProjectLegacyMessageEventPreservesFlatFields(t *testing.T) {
	var event legacyMessageEvent
	if err := json.Unmarshal([]byte(`{
	  "uuid":"evt_legacy",
	  "ts":"1784853999000",
	  "event":{
	    "type":"message",
	    "message_id":"om_legacy",
	    "chat_id":"oc_private",
	    "chat_type":"private",
	    "open_id":"ou_owner",
	    "text":"几点了？",
	    "mentions":[{"key":"@_user_1","id":{"open_id":"ou_bot"},"name":"Assistant Bot"}]
	  }
	}`), &event); err != nil {
		t.Fatal(err)
	}
	envelope, err := projectLegacyMessageEvent(&event)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.EventID != "evt_legacy" ||
		envelope.MessageID != "om_legacy" ||
		envelope.ChatID != "oc_private" ||
		envelope.ChatType != "p2p" ||
		envelope.SenderID != "ou_owner" ||
		envelope.SenderType != "user" ||
		envelope.Content != "几点了？" {
		t.Fatalf("envelope=%+v", envelope)
	}
	if len(envelope.Mentions) != 1 || envelope.Mentions[0].OpenID != "ou_bot" {
		t.Fatalf("mentions=%+v", envelope.Mentions)
	}
}

func TestProjectLegacyMessageEventAcceptsOpenPlatformIDs(t *testing.T) {
	var event legacyMessageEvent
	if err := json.Unmarshal([]byte(`{
	  "uuid":"evt_open_ids",
	  "ts":"1784853999000",
	  "event":{
	    "type":"message",
	    "open_message_id":"om_open",
	    "open_chat_id":"oc_open",
	    "chat_type":"private",
	    "open_id":"ou_owner",
	    "text":"你好，几点了？"
	  }
	}`), &event); err != nil {
		t.Fatal(err)
	}
	envelope, err := projectLegacyMessageEvent(&event)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.MessageID != "om_open" ||
		envelope.ChatID != "oc_open" ||
		envelope.ChatType != "p2p" ||
		envelope.SenderID != "ou_owner" ||
		envelope.SenderType != "user" {
		t.Fatalf("envelope=%+v", envelope)
	}
}

func TestRealtimeReceiveTimeFillsMissingLegacyCreateTime(t *testing.T) {
	oldNow := realtimeNow
	t.Cleanup(func() { realtimeNow = oldNow })
	realtimeNow = func() time.Time {
		return time.Date(2026, 7, 25, 4, 42, 31, 0, time.UTC)
	}
	got := prepareRealtimeEnvelope(EventEnvelope{
		MessageID:  "om_1",
		ChatID:     "oc_1",
		SenderID:   "ou_owner",
		SenderType: "user",
	})
	if !got.CreatedAt.Equal(realtimeNow()) {
		t.Fatalf("created_at=%s", got.CreatedAt)
	}
}

func TestProjectLegacyMessageEventPreservesNestedFields(t *testing.T) {
	var event legacyMessageEvent
	if err := json.Unmarshal([]byte(`{
	  "event_id":"evt_nested",
	  "event":{
	    "type":"message",
	    "message":{
	      "message_id":"om_nested",
	      "chat_id":"oc_group",
	      "chat_type":"group",
	      "content":"{\"text\":\"@机器人 ping\"}",
	      "root_id":"om_root",
	      "parent_id":"om_parent",
	      "thread_id":"omt_1",
	      "create_time":"1784853999000",
	      "mentions":[{"key":"@_user_1","open_id":"ou_bot","name":"机器人"}]
	    },
	    "sender":{"sender_type":"user","sender_id":{"open_id":"ou_owner"}}
	  }
	}`), &event); err != nil {
		t.Fatal(err)
	}
	envelope, err := projectLegacyMessageEvent(&event)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.EventID != "evt_nested" ||
		envelope.MessageID != "om_nested" ||
		envelope.ChatID != "oc_group" ||
		envelope.ChatType != "group" ||
		envelope.SenderID != "ou_owner" ||
		envelope.Content != "@机器人 ping" ||
		envelope.RootID != "om_root" ||
		envelope.ReplyTo != "om_parent" ||
		envelope.ThreadID != "omt_1" {
		t.Fatalf("envelope=%+v", envelope)
	}
	if len(envelope.Mentions) != 1 || envelope.Mentions[0].OpenID != "ou_bot" {
		t.Fatalf("mentions=%+v", envelope.Mentions)
	}
}

func sdkMessageEvent() *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{
			Header: &larkevent.EventHeader{
				EventID:    "evt_1",
				CreateTime: "1710000000000",
			},
		},
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageId: stringPtr("om_1"),
				ChatId:    stringPtr("oc_1"),
				ChatType:  stringPtr("group"),
				Content:   stringPtr(`{"text":"hello"}`),
			},
			Sender: &larkim.EventSender{
				SenderType: stringPtr("user"),
				SenderId:   &larkim.UserId{OpenId: stringPtr("ou_owner")},
			},
		},
	}
}

func stringPtr(value string) *string {
	return &value
}

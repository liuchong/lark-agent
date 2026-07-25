package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

type fakeStore struct {
	events []domain.NormalizedEvent
}

func (f *fakeStore) RecordIntake(
	_ context.Context,
	ev domain.NormalizedEvent,
) (domain.IntakeReceipt, error) {
	f.events = append(f.events, ev)
	return domain.IntakeReceipt{Disposition: domain.IntakeAdmitted}, nil
}

func TestNormalizeRealtimeMessage(t *testing.T) {
	raw := []byte(`{
	  "header":{"event_id":"evt_1","event_type":"im.message.receive_v1","create_time":"1700000000000"},
	  "event":{
	    "message":{
	      "message_id":"om_1",
	      "chat_id":"oc_1",
	      "root_id":"om_root",
	      "parent_id":"om_parent",
	      "thread_id":"omt_1",
	      "content":"{\"text\":\"hello\"}",
	      "mentions":[{"key":"@_user_1","id":{"open_id":"ou_owner"},"name":"Owner"}]
	    },
	    "sender":{"sender_type":"user","sender_id":{"open_id":"ou_sender"}}
	  }
	}`)
	ev, err := NormalizeRealtime(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Source != domain.SourceRealtime || ev.EventID != "evt_1" || ev.MessageID != "om_1" {
		t.Fatalf("event=%+v", ev)
	}
	if !ev.MentionsUser("ou_owner") {
		t.Fatalf("mentions=%+v", ev.Mentions)
	}
	if ev.RootMessageID != "om_root" || ev.ReplyToMessageID != "om_parent" || ev.ThreadID != "omt_1" {
		t.Fatalf("relations=%+v", ev)
	}
}

func TestNormalizeProcessedRealtimeMessage(t *testing.T) {
	raw := []byte(`{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_processed",
	  "message_id":"om_processed",
	  "create_time":"1784853999000",
	  "chat_id":"oc_private",
	  "chat_type":"p2p",
	  "root_id":"om_root",
	  "reply_to":"om_parent",
	  "thread_id":"omt_private",
	  "message_type":"text",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"几点了？",
	  "mentions":[{"key":"@_user_1","open_id":"ou_bot","name":"Assistant Bot"}]
	}`)
	ev, err := NormalizeRealtime(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Source != domain.SourceRealtime ||
		ev.EventID != "evt_processed" ||
		ev.MessageID != "om_processed" ||
		ev.ChatType != "p2p" ||
		ev.SenderID != "ou_owner" ||
		ev.Content != "几点了？" {
		t.Fatalf("event=%+v", ev)
	}
	wantCreatedAt := time.UnixMilli(1784853999000).UTC()
	if !ev.CreatedAt.Equal(wantCreatedAt) {
		t.Fatalf("created_at=%s want=%s", ev.CreatedAt, wantCreatedAt)
	}
	if !ev.MentionsUser("ou_bot") {
		t.Fatalf("mentions=%+v", ev.Mentions)
	}
	if ev.RootMessageID != "om_root" || ev.ReplyToMessageID != "om_parent" || ev.ThreadID != "omt_private" {
		t.Fatalf("relations=%+v", ev)
	}
}

func TestNormalizeProcessedRealtimeMessageReadsCreatedAt(t *testing.T) {
	raw := []byte(`{
	  "type":"message",
	  "event_id":"evt_created_at",
	  "message_id":"om_created_at",
	  "created_at":"2026-07-24T20:45:48.322521Z",
	  "chat_id":"oc_private",
	  "chat_type":"p2p",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"几点了？"
	}`)
	ev, err := NormalizeRealtime(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 24, 20, 45, 48, 322521000, time.UTC)
	if !ev.CreatedAt.Equal(want) {
		t.Fatalf("created_at=%s want=%s", ev.CreatedAt, want)
	}
}

func TestIngestorStoresBothSources(t *testing.T) {
	store := &fakeStore{}
	ingestor := New(store)
	events := []domain.NormalizedEvent{
		{Source: domain.SourceRealtime, EventID: "evt_1", MessageID: "om_1"},
		{Source: domain.SourcePoll, EventID: "poll_1", MessageID: "om_2"},
	}
	count, err := ingestor.Ingest(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(store.events) != 2 {
		t.Fatalf("count=%d events=%+v", count, store.events)
	}
}

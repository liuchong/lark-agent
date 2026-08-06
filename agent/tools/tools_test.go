package tools

import (
	"context"
	"testing"
)

type fakeMessenger struct {
	replyTarget string
	replyText   string
}

func (f *fakeMessenger) ReplyAsUser(_ context.Context, req ReplyRequest) (ReplyResult, error) {
	f.replyTarget = req.MessageID
	f.replyText = req.Text
	return ReplyResult{MessageID: "om_reply", ChatID: "oc_1"}, nil
}

func (f *fakeMessenger) NotifyOwner(_ context.Context, req NotifyRequest) error {
	f.replyText = req.Text
	return nil
}

func TestReplyMessageToolRequiresTarget(t *testing.T) {
	tool := ReplyMessageTool{Messenger: &fakeMessenger{}}
	if _, err := tool.Execute(context.Background(), ReplyRequest{Text: "hello"}); err == nil {
		t.Fatal("accepted missing message id")
	}
}

func TestReplyMessageToolCallsMessenger(t *testing.T) {
	m := &fakeMessenger{}
	tool := ReplyMessageTool{Messenger: m}
	result, err := tool.Execute(context.Background(), ReplyRequest{MessageID: "om_1", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID != "om_reply" || m.replyTarget != "om_1" {
		t.Fatalf("result=%+v messenger=%+v", result, m)
	}
}

func TestPublicMessageUUIDIsStableBoundedAndNamespaceDistinct(t *testing.T) {
	if key := PublicMessageUUID("reply", ""); key != "" {
		t.Fatalf("empty internal key produced public UUID %q", key)
	}
	internalKey := "message:om_example_message_001:g7:reply"
	replyKey := PublicMessageUUID("reply", internalKey)
	if replyKey != PublicMessageUUID("reply", internalKey) {
		t.Fatalf("public message UUID is not stable: %q", replyKey)
	}
	if replyKey == internalKey || len(replyKey) > 50 {
		t.Fatalf("public key=%q len=%d", replyKey, len(replyKey))
	}
	if replyKey == PublicMessageUUID("investigation", internalKey) {
		t.Fatalf("namespaces share public UUID %q", replyKey)
	}
	if replyKey == PublicMessageUUID(
		"reply",
		"message:om_example_message_001:g8:reply",
	) {
		t.Fatalf("communication generations share public UUID %q", replyKey)
	}
}

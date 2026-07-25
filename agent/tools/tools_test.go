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

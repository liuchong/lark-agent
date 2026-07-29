package lifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/tools"
)

type recordingMessenger struct {
	requests []tools.NotifyRequest
}

func (m *recordingMessenger) ReplyAsUser(context.Context, tools.ReplyRequest) (tools.ReplyResult, error) {
	return tools.ReplyResult{}, nil
}

func (m *recordingMessenger) NotifyOwner(_ context.Context, request tools.NotifyRequest) error {
	m.requests = append(m.requests, request)
	return nil
}

func TestControllerSendsIdempotentLifecycleNotices(t *testing.T) {
	messenger := &recordingMessenger{}
	controller := NewController(messenger, Options{Language: "zh-CN", OwnerName: "测试负责人"})
	summary := Summary{Resumed: 3, WaitingOwner: 2, Terminalized: 1, Uncertain: 1}
	transitionID := "session-" + strings.Repeat("a", 32)

	if err := controller.NotifyOffline(context.Background(), transitionID, summary); err != nil {
		t.Fatal(err)
	}
	if err := controller.NotifyOnline(context.Background(), transitionID, summary); err != nil {
		t.Fatal(err)
	}
	if len(messenger.requests) != 2 {
		t.Fatalf("requests=%+v", messenger.requests)
	}
	offlineKey := messenger.requests[0].IdempotencyKey
	onlineKey := messenger.requests[1].IdempotencyKey
	for _, key := range []string{offlineKey, onlineKey} {
		if len(key) > 50 {
			t.Fatalf("lifecycle idempotency key exceeds Lark uuid limit: %q (%d)", key, len(key))
		}
	}
	if offlineKey == onlineKey ||
		!strings.HasPrefix(offlineKey, "lifecycle:offline:") ||
		!strings.HasPrefix(onlineKey, "lifecycle:online:") {
		t.Fatalf("offline=%q online=%q", offlineKey, onlineKey)
	}
	for i, want := range []string{"正在离线", "已上线"} {
		text := messenger.requests[i].Text
		for _, fragment := range []string{want, "3", "2", "1", "测试负责人"} {
			if !strings.Contains(text, fragment) {
				t.Fatalf("notice %d missing %q: %s", i, fragment, text)
			}
		}
		if strings.Contains(text, "Agent") {
			t.Fatalf("notice mixes English product prose: %s", text)
		}
	}
}

func TestControllerLifecycleIdempotencyKeyIsStable(t *testing.T) {
	messenger := &recordingMessenger{}
	controller := NewController(messenger, Options{Language: "zh-CN", OwnerName: "测试负责人"})
	sessionID := "session-" + strings.Repeat("b", 32)

	if err := controller.NotifyOnline(context.Background(), sessionID, Summary{}); err != nil {
		t.Fatal(err)
	}
	if err := controller.NotifyOnline(context.Background(), sessionID, Summary{}); err != nil {
		t.Fatal(err)
	}
	if messenger.requests[0].IdempotencyKey != messenger.requests[1].IdempotencyKey {
		t.Fatalf("requests=%+v", messenger.requests)
	}
}

func TestControllerRejectsMissingTransitionIdentity(t *testing.T) {
	controller := NewController(&recordingMessenger{}, Options{Language: "zh-CN", OwnerName: "测试负责人"})
	if err := controller.NotifyOnline(context.Background(), "", Summary{}); err == nil {
		t.Fatal("missing session id must fail")
	}
	if err := controller.NotifyOffline(context.Background(), "", Summary{}); err == nil {
		t.Fatal("missing transition id must fail")
	}
}

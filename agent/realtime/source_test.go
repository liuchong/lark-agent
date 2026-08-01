package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
	"github.com/liuchong/lark-agent/internal/lark"
)

func TestLarkConsumerUsesPublishedMessageEvent(t *testing.T) {
	if messageReceiveEventKey != "im.message.receive_v1" {
		t.Fatalf("event key=%q", messageReceiveEventKey)
	}
}

type preflightCaller struct {
	body string
}

func (c preflightCaller) CallAPI(
	context.Context,
	lark.APIRequest,
) (any, error) {
	var response any
	if err := json.Unmarshal([]byte(c.body), &response); err != nil {
		return nil, err
	}
	return response, nil
}

type fakeConsumer struct {
	payload string
	err     error
}

func (f fakeConsumer) Consume(_ context.Context, out io.Writer) error {
	if f.payload != "" {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(f.payload)); err != nil {
			return err
		}
		if _, err := io.WriteString(out, compact.String()+"\n"); err != nil {
			return err
		}
	}
	return f.err
}

type fakeStore struct {
	items []domain.WorkItem
}

func (f *fakeStore) RecordWorkIntake(
	_ context.Context,
	item domain.WorkItem,
) (domain.IntakeReceipt, error) {
	f.items = append(f.items, item)
	return domain.IntakeReceipt{Disposition: domain.IntakeAdmitted}, nil
}

func ownerPrivatePayload(messageID string) string {
	return `{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_1",
	  "message_id":"` + messageID + `",
	  "create_time":"1784853999000",
	  "chat_id":"oc_private",
	  "chat_type":"p2p",
	  "message_type":"text",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"几点了？"
	}`
}

func ownerPrivateLegacyPayload(messageID string) string {
	return `{
	  "type":"message",
	  "event_id":"evt_legacy",
	  "message_id":"` + messageID + `",
	  "create_time":"1784853999000",
	  "chat_id":"oc_private",
	  "chat_type":"p2p",
	  "message_type":"text",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"几点了？"
	}`
}

func TestRealtimePreflightRequiresPrivateAndGroupMentionScopes(t *testing.T) {
	client := preflightCaller{body: `{
	  "code":0,
	  "data":{"items":[{
	    "version_id":"v1",
	    "version":"1.0.0",
	    "status":1,
	    "publish_time":"1784853999000",
	    "event_infos":[{"event_type":"im.message.receive_v1"}],
	    "scopes":[{
	      "scope":"im:message.p2p_msg:readonly",
	      "token_types":["tenant"]
	    }]
	  }]}
	}`}
	err := preflightRealtime(context.Background(), client, "cli_test")
	var permissionErr *errs.PermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error=%v is not typed", err)
	}
	if permissionErr.Subtype != errs.SubtypeMissingScope ||
		len(permissionErr.MissingScopes) != 1 ||
		permissionErr.MissingScopes[0] != groupMentionScope {
		t.Fatalf("permission error=%+v", permissionErr)
	}
}

func TestRealtimePreflightAcceptsPublishedEventWithBothScopes(t *testing.T) {
	client := preflightCaller{body: `{
	  "code":0,
	  "data":{"items":[{
	    "version_id":"v1",
	    "version":"1.0.0",
	    "status":1,
	    "publish_time":"1784853999000",
	    "event_infos":[{"event_type":"im.message.receive_v1"}],
	    "scopes":[
	      {"scope":"im:message.p2p_msg:readonly","token_types":["tenant"]},
	      {"scope":"im:message.group_at_msg:readonly","token_types":["tenant"]},
	      {"scope":"im:message.reactions:read","token_types":["tenant"]}
	    ]
	  }]}
	}`}
	if err := preflightRealtime(context.Background(), client, "cli_test"); err != nil {
		t.Fatal(err)
	}
}

func TestSourcePersistsClassifiedOwnerPrivateEventWithoutPoll(t *testing.T) {
	store := &fakeStore{}
	source := NewSource(fakeConsumer{payload: ownerPrivatePayload("om_realtime")}, store, Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		Classify: func(_ context.Context, item domain.WorkItem) (domain.Decision, error) {
			if item.Event.ChatPartnerID != "ou_bot" {
				t.Fatalf("chat_partner_id=%q", item.Event.ChatPartnerID)
			}
			return domain.Decision{
				WorkKind: domain.WorkKindFastPath,
				Priority: domain.PriorityFastPath,
			}, nil
		},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("items=%+v", store.items)
	}
	item := store.items[0]
	if item.Event.Source != domain.SourceRealtime ||
		item.Event.MessageID != "om_realtime" ||
		item.WorkKind != domain.WorkKindFastPath ||
		item.Priority != domain.PriorityFastPath {
		t.Fatalf("item=%+v", item)
	}
}

func TestSourcePersistsLegacyOwnerPrivateEventWithoutPoll(t *testing.T) {
	store := &fakeStore{}
	source := NewSource(fakeConsumer{payload: ownerPrivateLegacyPayload("om_legacy")}, store, Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
		Classify: func(_ context.Context, item domain.WorkItem) (domain.Decision, error) {
			if item.Event.ChatPartnerID != "ou_bot" {
				t.Fatalf("chat_partner_id=%q", item.Event.ChatPartnerID)
			}
			return domain.Decision{
				WorkKind: domain.WorkKindFastPath,
				Priority: domain.PriorityFastPath,
			}, nil
		},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("items=%+v", store.items)
	}
	item := store.items[0]
	if item.Event.Source != domain.SourceRealtime ||
		item.Event.EventID != "evt_legacy" ||
		item.Event.MessageID != "om_legacy" ||
		item.Event.ChatType != "p2p" ||
		item.WorkKind != domain.WorkKindFastPath ||
		item.Priority != domain.PriorityFastPath {
		t.Fatalf("item=%+v", item)
	}
}

func TestSourceFillsReceiveTimeWhenRealtimeEventOmitsCreateTime(t *testing.T) {
	oldNow := sourceNow
	t.Cleanup(func() { sourceNow = oldNow })
	sourceNow = func() time.Time {
		return time.Date(2026, 7, 25, 4, 45, 48, 0, time.UTC)
	}
	store := &fakeStore{}
	payload := `{
	  "type":"message",
	  "event_id":"evt_no_time",
	  "message_id":"om_no_time",
	  "chat_id":"oc_group",
	  "chat_type":"group",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"@_user_1 ping",
	  "mentions":[{"key":"@_user_1","open_id":"ou_bot","name":"Assistant Bot"}]
	}`
	source := NewSource(fakeConsumer{payload: payload}, store, Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 {
		t.Fatalf("items=%+v", store.items)
	}
	if !store.items[0].Event.CreatedAt.Equal(sourceNow()) {
		t.Fatalf("created_at=%s", store.items[0].Event.CreatedAt)
	}
}

func TestSourceDropsNonOwnerMessagesBeforeQueue(t *testing.T) {
	store := &fakeStore{}
	payload := `{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_other",
	  "message_id":"om_other",
	  "create_time":"1784853999000",
	  "chat_id":"oc_private",
	  "chat_type":"p2p",
	  "sender_id":"ou_other",
	  "sender_type":"user",
	  "content":"帮我编程"
	}`
	source := NewSource(fakeConsumer{payload: payload}, store, Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 0 {
		t.Fatalf("non-owner items=%+v", store.items)
	}
}

func TestSourceDropsNonOwnerGroupMessageThatMentionsAssistantBeforeQueue(t *testing.T) {
	store := &fakeStore{}
	payload := `{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_group_mention",
	  "message_id":"om_group_mention",
	  "create_time":"1784853999000",
	  "chat_id":"oc_group",
	  "chat_type":"group",
	  "sender_id":"ou_other",
	  "sender_type":"user",
	  "content":"@_user_1 帮我查这个接口",
	  "mentions":[{"key":"@_user_1","open_id":"ou_bot","name":"Assistant Bot"}]
	}`
	source := NewSource(fakeConsumer{payload: payload}, store, Config{
		OwnerOpenID:         "ou_owner",
		AssistantOpenIDs:    []string{"ou_bot"},
		AssistantReplyScope: domain.ReplyScopeAllGroups,
		Classify: func(_ context.Context, _ domain.WorkItem) (domain.Decision, error) {
			t.Fatal("non-owner assistant mention reached classifier")
			return domain.Decision{}, nil
		},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 0 {
		t.Fatalf("non-owner assistant mention items=%+v", store.items)
	}
}

func TestSourceConfiguredAssistantScopeDropsOutsideGroupBeforeQueue(t *testing.T) {
	store := &fakeStore{}
	payload := `{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_group_outside",
	  "message_id":"om_group_outside",
	  "create_time":"1784853999000",
	  "chat_id":"oc_outside",
	  "chat_type":"group",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"@_user_1 帮我查这个接口",
	  "mentions":[{"key":"@_user_1","open_id":"ou_bot","name":"Assistant Bot"}]
	}`
	source := NewSource(fakeConsumer{payload: payload}, store, Config{
		OwnerOpenID:         "ou_owner",
		AssistantOpenIDs:    []string{"ou_bot"},
		AssistantReplyScope: domain.ReplyScopeConfiguredGroups,
		ConfiguredChatIDs:   []string{"oc_configured"},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 0 {
		t.Fatalf("outside assistant scope items=%+v", store.items)
	}
}

func TestSourceConfiguredAssistantScopeMarksBotResolvedGroup(t *testing.T) {
	store := &fakeStore{}
	payload := `{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_group_configured",
	  "message_id":"om_group_configured",
	  "create_time":"1784853999000",
	  "chat_id":"oc_configured",
	  "chat_type":"group",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"@_user_1 帮我查这个接口",
	  "mentions":[{"key":"@_user_1","open_id":"ou_bot","name":"Assistant Bot"}]
	}`
	source := NewSource(fakeConsumer{payload: payload}, store, Config{
		OwnerOpenID:         "ou_owner",
		AssistantOpenIDs:    []string{"ou_bot"},
		AssistantReplyScope: domain.ReplyScopeConfiguredGroups,
		ConfiguredChatIDs:   []string{"oc_configured"},
		Classify: func(_ context.Context, item domain.WorkItem) (domain.Decision, error) {
			if !item.Event.InAssistantScope || item.Event.InTestScope {
				t.Fatalf("event scope=%+v", item.Event)
			}
			return domain.Decision{
				Relevance: domain.RelevanceAssistantRequest,
				WorkKind:  domain.WorkKindSimpleQuestion,
				Priority:  domain.PrioritySimpleQuestion,
			}, nil
		},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 1 || !store.items[0].Event.InAssistantScope {
		t.Fatalf("configured assistant scope items=%+v", store.items)
	}
}

func TestSourceOnlyAcceptsGroupMessageThatMentionsAssistant(t *testing.T) {
	store := &fakeStore{}
	payload := `{
	  "type":"im.message.receive_v1",
	  "event_id":"evt_group",
	  "message_id":"om_group",
	  "create_time":"1784853999000",
	  "chat_id":"oc_group",
	  "chat_type":"group",
	  "sender_id":"ou_owner",
	  "sender_type":"user",
	  "content":"普通群消息"
	}`
	source := NewSource(fakeConsumer{payload: payload}, store, Config{
		OwnerOpenID:      "ou_owner",
		AssistantOpenIDs: []string{"ou_bot"},
	})

	if err := source.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.items) != 0 {
		t.Fatalf("ordinary owner group message was queued: %+v", store.items)
	}
}

type retryRunner struct {
	calls atomic.Int32
	seen  chan struct{}
}

func (r *retryRunner) Run(ctx context.Context) error {
	call := r.calls.Add(1)
	r.seen <- struct{}{}
	if call == 1 {
		return errors.New("connection lost")
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestSupervisorRestartsFailedRealtimeSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &retryRunner{seen: make(chan struct{}, 2)}
	reported := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		Supervise(ctx, runner, time.Millisecond, 2*time.Millisecond, func(err error) {
			reported <- err
		})
		close(done)
	}()

	<-runner.seen
	select {
	case err := <-reported:
		if err == nil || err.Error() != "connection lost" {
			t.Fatalf("reported=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first real-time failure was not reported")
	}
	select {
	case <-runner.seen:
	case <-time.After(time.Second):
		t.Fatal("real-time session was not restarted")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop with context")
	}
}

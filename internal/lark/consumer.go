package lark

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unsafe"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type Consumer struct {
	AppID     string
	AppSecret string
	BaseURL   string
}

type ResourceSignalKind string

const (
	ResourceSignalComment      ResourceSignalKind = "comment"
	ResourceSignalRecordChange ResourceSignalKind = "record_change"
	ResourceSignalFieldChange  ResourceSignalKind = "field_change"
)

type ResourceSignal struct {
	Kind       ResourceSignalKind `json:"kind"`
	EventID    string             `json:"event_id"`
	FileToken  string             `json:"file_token"`
	FileType   string             `json:"file_type,omitempty"`
	TableID    string             `json:"table_id,omitempty"`
	RecordIDs  []string           `json:"record_ids,omitempty"`
	CommentID  string             `json:"comment_id,omitempty"`
	ReplyID    string             `json:"reply_id,omitempty"`
	ToOpenID   string             `json:"to_open_id,omitempty"`
	Mentioned  bool               `json:"mentioned,omitempty"`
	ObservedAt time.Time          `json:"observed_at"`
}

type ConsumerHandlers struct {
	Message  func(EventEnvelope) error
	Resource func(ResourceSignal) error
}

var realtimeNow = func() time.Time {
	return time.Now().UTC()
}

func (c Consumer) Consume(ctx context.Context, handle func(EventEnvelope) error) error {
	return c.ConsumeWithHandlers(ctx, ConsumerHandlers{Message: handle})
}

func (c Consumer) ConsumeWithHandlers(ctx context.Context, handlers ConsumerHandlers) error {
	if c.AppID == "" || c.AppSecret == "" {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "lark app credentials are not configured")
	}
	if handlers.Message == nil && handlers.Resource == nil {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "event handler is required")
	}
	eventErr := make(chan error, 1)
	handleEvent := func(envelope EventEnvelope, err error) error {
		if err == nil {
			envelope = prepareRealtimeEnvelope(envelope)
			if handlers.Message != nil {
				err = handlers.Message(envelope)
			}
		}
		if err != nil {
			select {
			case eventErr <- err:
			default:
			}
			return err
		}
		return nil
	}
	sdkLogger := newCredentialSafeSDKLogger()
	handler := dispatcher.NewEventDispatcher("", "")
	handler.Config.Logger = sdkLogger
	handler.OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		return handleEvent(projectMessageEvent(event))
	})
	registerSDKEventHandler(handler, "message", legacyMessageHandler{handle: func(event *legacyMessageEvent) error {
		return handleEvent(projectLegacyMessageEvent(event))
	}})
	handleResource := func(signal ResourceSignal, err error) error {
		if err == nil && handlers.Resource != nil {
			if signal.ObservedAt.IsZero() {
				signal.ObservedAt = realtimeNow()
			}
			err = handlers.Resource(signal)
		}
		if err != nil {
			select {
			case eventErr <- err:
			default:
			}
		}
		return err
	}
	handler.OnP2NoticeCommentAddV1(func(
		ctx context.Context,
		event *larkdrive.P2NoticeCommentAddV1,
	) error {
		return handleResource(projectCommentNotice(event))
	})
	handler.OnP2FileBitableRecordChangedV1(func(
		ctx context.Context,
		event *larkdrive.P2FileBitableRecordChangedV1,
	) error {
		return handleResource(projectBaseRecordChange(event))
	})
	handler.OnP2FileBitableFieldChangedV1(func(
		ctx context.Context,
		event *larkdrive.P2FileBitableFieldChangedV1,
	) error {
		return handleResource(projectBaseFieldChange(event))
	})
	for _, eventType := range ignoredRealtimeEventTypes {
		registerSDKEventHandler(handler, eventType, ignoredEventHandler{})
	}
	options := []ws.ClientOption{
		ws.WithEventHandler(handler),
		ws.WithLogger(sdkLogger),
	}
	if c.BaseURL != "" {
		options = append(options, ws.WithDomain(c.BaseURL))
	}
	client := ws.NewClient(c.AppID, c.AppSecret, options...)
	startErr := make(chan error, 1)
	go func() { startErr <- client.Start(ctx) }()
	select {
	case err := <-eventErr:
		return err
	case err := <-startErr:
		if err != nil {
			return errs.NewNetworkError(errs.SubtypeNetworkTransport, "lark SDK websocket event consumer exited").WithCause(err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func projectCommentNotice(event *larkdrive.P2NoticeCommentAddV1) (ResourceSignal, error) {
	if event == nil || event.Event == nil || event.Event.NoticeMeta == nil {
		return ResourceSignal{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark comment notice event is empty",
		)
	}
	notice := event.Event.NoticeMeta
	signal := ResourceSignal{
		Kind:      ResourceSignalComment,
		FileToken: larkcore.StringValue(notice.FileToken),
		FileType:  larkcore.StringValue(notice.FileType),
		CommentID: larkcore.StringValue(event.Event.CommentId),
		ReplyID:   larkcore.StringValue(event.Event.ReplyId),
		Mentioned: event.Event.IsMentioned != nil && *event.Event.IsMentioned,
	}
	if notice.ToUserId != nil {
		signal.ToOpenID = larkcore.StringValue(notice.ToUserId.OpenId)
	}
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		signal.EventID = event.EventV2Base.Header.EventID
		signal.ObservedAt = parseMessageTime(event.EventV2Base.Header.CreateTime)
	}
	if signal.EventID == "" || signal.FileToken == "" || signal.CommentID == "" {
		return ResourceSignal{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark comment notice is missing event_id, file_token, or comment_id",
		)
	}
	return signal, nil
}

func projectBaseRecordChange(
	event *larkdrive.P2FileBitableRecordChangedV1,
) (ResourceSignal, error) {
	if event == nil || event.Event == nil {
		return ResourceSignal{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark Base record event is empty",
		)
	}
	signal := ResourceSignal{
		Kind:      ResourceSignalRecordChange,
		FileToken: larkcore.StringValue(event.Event.FileToken),
		FileType:  larkcore.StringValue(event.Event.FileType),
		TableID:   larkcore.StringValue(event.Event.TableId),
	}
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		signal.EventID = event.EventV2Base.Header.EventID
		signal.ObservedAt = parseMessageTime(event.EventV2Base.Header.CreateTime)
	}
	seen := map[string]bool{}
	for _, action := range event.Event.ActionList {
		if action == nil {
			continue
		}
		recordID := larkcore.StringValue(action.RecordId)
		if recordID != "" && !seen[recordID] {
			seen[recordID] = true
			signal.RecordIDs = append(signal.RecordIDs, recordID)
		}
	}
	if signal.EventID == "" || signal.FileToken == "" || signal.TableID == "" {
		return ResourceSignal{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark Base record event is missing event_id, file_token, or table_id",
		)
	}
	return signal, nil
}

func projectBaseFieldChange(
	event *larkdrive.P2FileBitableFieldChangedV1,
) (ResourceSignal, error) {
	if event == nil || event.Event == nil {
		return ResourceSignal{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark Base field event is empty",
		)
	}
	signal := ResourceSignal{
		Kind:      ResourceSignalFieldChange,
		FileToken: larkcore.StringValue(event.Event.FileToken),
		FileType:  larkcore.StringValue(event.Event.FileType),
		TableID:   larkcore.StringValue(event.Event.TableId),
	}
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		signal.EventID = event.EventV2Base.Header.EventID
		signal.ObservedAt = parseMessageTime(event.EventV2Base.Header.CreateTime)
	}
	if signal.EventID == "" || signal.FileToken == "" || signal.TableID == "" {
		return ResourceSignal{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"Lark Base field event is missing event_id, file_token, or table_id",
		)
	}
	return signal, nil
}

func prepareRealtimeEnvelope(envelope EventEnvelope) EventEnvelope {
	if envelope.CreatedAt.IsZero() {
		envelope.CreatedAt = realtimeNow()
	}
	return envelope
}

func projectMessageEvent(event *larkim.P2MessageReceiveV1) (EventEnvelope, error) {
	if event == nil || event.Event == nil {
		return EventEnvelope{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "lark websocket message event is empty")
	}
	message := event.Event.Message
	sender := event.Event.Sender
	out := EventEnvelope{}
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		out.EventID = event.EventV2Base.Header.EventID
		out.CreatedAt = parseMessageTime(event.EventV2Base.Header.CreateTime)
	}
	if message != nil {
		out.MessageID = larkcore.StringValue(message.MessageId)
		out.ChatID = larkcore.StringValue(message.ChatId)
		out.ChatType = larkcore.StringValue(message.ChatType)
		out.RootID = larkcore.StringValue(message.RootId)
		out.ReplyTo = larkcore.StringValue(message.ParentId)
		out.ThreadID = larkcore.StringValue(message.ThreadId)
		out.Content = eventText(larkcore.StringValue(message.Content))
		if out.CreatedAt.IsZero() {
			out.CreatedAt = parseMessageTime(larkcore.StringValue(message.CreateTime))
		}
		for _, mention := range message.Mentions {
			out.Mentions = append(out.Mentions, Mention{
				Key:    larkcore.StringValue(mention.Key),
				Name:   larkcore.StringValue(mention.Name),
				OpenID: userOpenID(mention.Id),
			})
		}
	}
	if sender != nil {
		out.SenderType = larkcore.StringValue(sender.SenderType)
		out.SenderID = userOpenID(sender.SenderId)
	}
	if err := validateProjectedMessageEvent(out); err != nil {
		return EventEnvelope{}, err
	}
	return out, nil
}

type legacyMessageHandler struct {
	handle func(*legacyMessageEvent) error
}

func (h legacyMessageHandler) Event() any {
	return &legacyMessageEvent{}
}

func (h legacyMessageHandler) Handle(_ context.Context, event any) error {
	return h.handle(event.(*legacyMessageEvent))
}

var ignoredRealtimeEventTypes = []string{
	"message_read",
	"im.message.message_read_v1",
	"im.message.reaction.created_v1",
	"im.message.reaction.deleted_v1",
	"im.chat.access_event.bot_p2p_chat_entered_v1",
}

type ignoredEventHandler struct{}

func (ignoredEventHandler) Event() any {
	return &map[string]any{}
}

func (ignoredEventHandler) Handle(context.Context, any) error {
	return nil
}

// registerSDKEventHandler bridges legacy event keys that the public SDK can
// dispatch but does not expose a typed registration method for.
func registerSDKEventHandler(
	eventDispatcher *dispatcher.EventDispatcher,
	eventType string,
	handler larkevent.EventHandler,
) {
	value := reflect.ValueOf(eventDispatcher).Elem().FieldByName("eventType2EventHandler")
	handlers := reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()
	handlers.SetMapIndex(reflect.ValueOf(eventType), reflect.ValueOf(handler))
}

type legacyMention struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	OpenID string `json:"open_id"`
	ID     struct {
		OpenID string `json:"open_id"`
	} `json:"id"`
}

type legacyMessageEvent struct {
	UUID    string `json:"uuid"`
	EventID string `json:"event_id"`
	TS      string `json:"ts"`
	Event   struct {
		Type       string          `json:"type"`
		MessageID  string          `json:"message_id"`
		OpenMsgID  string          `json:"open_message_id"`
		ChatID     string          `json:"chat_id"`
		OpenChatID string          `json:"open_chat_id"`
		ChatType   string          `json:"chat_type"`
		OpenID     string          `json:"open_id"`
		UserOpenID string          `json:"user_open_id"`
		SenderID   string          `json:"sender_id"`
		SenderType string          `json:"sender_type"`
		MsgType    string          `json:"msg_type"`
		Text       string          `json:"text"`
		Content    string          `json:"content"`
		RootID     string          `json:"root_id"`
		ParentID   string          `json:"parent_id"`
		ThreadID   string          `json:"thread_id"`
		Mentions   []legacyMention `json:"mentions"`
		Message    struct {
			MessageID  string          `json:"message_id"`
			ChatID     string          `json:"chat_id"`
			ChatType   string          `json:"chat_type"`
			Content    string          `json:"content"`
			CreateTime string          `json:"create_time"`
			RootID     string          `json:"root_id"`
			ParentID   string          `json:"parent_id"`
			ThreadID   string          `json:"thread_id"`
			Mentions   []legacyMention `json:"mentions"`
		} `json:"message"`
		Sender struct {
			SenderType string `json:"sender_type"`
			SenderID   struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
	} `json:"event"`
}

func projectLegacyMessageEvent(event *legacyMessageEvent) (EventEnvelope, error) {
	if event == nil {
		return EventEnvelope{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "lark websocket legacy message event is empty")
	}
	out := EventEnvelope{
		EventID:   firstNonBlank(event.EventID, event.UUID),
		CreatedAt: parseMessageTime(event.TS),
	}
	message := event.Event.Message
	if message.MessageID != "" {
		out.MessageID = message.MessageID
		out.ChatID = message.ChatID
		out.ChatType = normalizeLegacyChatType(message.ChatType)
		out.RootID = message.RootID
		out.ReplyTo = message.ParentID
		out.ThreadID = message.ThreadID
		out.Content = eventText(message.Content)
		out.CreatedAt = firstTime(out.CreatedAt, parseMessageTime(message.CreateTime))
		out.Mentions = projectLegacyMentions(message.Mentions)
	} else {
		out.MessageID = firstNonBlank(event.Event.MessageID, event.Event.OpenMsgID)
		out.ChatID = firstNonBlank(event.Event.ChatID, event.Event.OpenChatID)
		out.ChatType = normalizeLegacyChatType(event.Event.ChatType)
		out.RootID = event.Event.RootID
		out.ReplyTo = event.Event.ParentID
		out.ThreadID = event.Event.ThreadID
		out.Content = firstNonBlank(event.Event.Text, eventText(event.Event.Content))
		out.Mentions = projectLegacyMentions(event.Event.Mentions)
	}
	out.SenderID = firstNonBlank(event.Event.Sender.SenderID.OpenID, event.Event.OpenID, event.Event.UserOpenID, event.Event.SenderID)
	out.SenderType = firstNonBlank(event.Event.Sender.SenderType, event.Event.SenderType)
	if out.SenderType == "" && out.SenderID != "" {
		out.SenderType = "user"
	}
	if err := validateProjectedMessageEvent(out); err != nil {
		return EventEnvelope{}, err
	}
	return out, nil
}

func projectLegacyMentions(items []legacyMention) []Mention {
	out := make([]Mention, 0, len(items))
	for _, item := range items {
		out = append(out, Mention{
			Key:    item.Key,
			Name:   item.Name,
			OpenID: firstNonBlank(item.OpenID, item.ID.OpenID),
		})
	}
	return out
}

func normalizeLegacyChatType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "private", "p2p":
		return "p2p"
	default:
		return value
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func userOpenID(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
	return larkcore.StringValue(id.OpenId)
}

func eventText(raw string) string {
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &content); err == nil && content.Text != "" {
		return content.Text
	}
	return raw
}

func validateProjectedMessageEvent(event EventEnvelope) error {
	for name, value := range map[string]string{
		"message_id":  event.MessageID,
		"chat_id":     event.ChatID,
		"sender_type": event.SenderType,
		"sender_id":   event.SenderID,
	} {
		if value == "" {
			return errs.NewInternalError(errs.SubtypeInvalidResponse, "lark websocket message event is missing %s", name)
		}
	}
	if event.SenderType != "user" && event.SenderType != "bot" {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "unsupported lark websocket sender_type %q", event.SenderType)
	}
	return nil
}

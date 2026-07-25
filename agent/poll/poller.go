// Package poll adapts user-visible Lark conversations into the durable inbox.
package poll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

const (
	cursorScopeAllMessages = "messages:all"
	cursorScopeIntakeFloor = "messages:intake-floor"
)

// IMClient is the live Lark IM surface needed by the poller.
type IMClient interface {
	SearchChats(context.Context, serviceim.SearchChatsRequest) (serviceim.SearchChatsResult, error)
	SearchMessages(context.Context, serviceim.SearchMessagesRequest) (serviceim.SearchMessagesResult, error)
}

type recentMessageLister interface {
	ListRecentMessages(context.Context, serviceim.ListRecentMessagesRequest) (serviceim.ListRecentMessagesResult, error)
}

type exactMessageGetter interface {
	GetMessages(context.Context, []string) ([]serviceim.Message, error)
}

type chatBatchGetter interface {
	BatchGetChats(context.Context, []string) (map[string]serviceim.Chat, error)
}

// Store persists cursor state and normalized events.
type Store interface {
	GetPollCursor(scope string) (time.Time, bool, error)
	SetPollCursor(scope string, cursor time.Time) error
	RecordWorkIntake(context.Context, domain.WorkItem) (domain.IntakeReceipt, error)
}

// Config controls user-message polling.
type Config struct {
	OwnerOpenID    string
	ChatQuery      string
	AssistantNames []string
	IncludePrivate bool
	PageSize       int
	IndexLookback  time.Duration
	Now            func() time.Time
	Classify       func(context.Context, domain.WorkItem) (domain.Decision, error)
}

// Result summarizes a poll cycle.
type Result struct {
	ColdStart   bool     `json:"cold_start" yaml:"cold_start"`
	Seen        int      `json:"seen" yaml:"seen"`
	Inserted    int      `json:"inserted" yaml:"inserted"`
	TestChatIDs []string `json:"test_chat_ids,omitempty" yaml:"test_chat_ids,omitempty"`
}

// Poller discovers visible conversations and ingests new messages.
type Poller struct {
	im            IMClient
	store         Store
	cfg           Config
	testChats     map[string]serviceim.Chat
	chatDetails   map[string]serviceim.Chat
	discovered    bool
	pageSize      int
	indexLookback time.Duration
	currentTime   func() time.Time
}

// New creates a user-message poller.
func New(im IMClient, store Store, cfg Config) *Poller {
	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	indexLookback := cfg.IndexLookback
	if indexLookback <= 0 {
		indexLookback = 2 * time.Minute
	}
	return &Poller{
		im:            im,
		store:         store,
		cfg:           cfg,
		testChats:     map[string]serviceim.Chat{},
		chatDetails:   map[string]serviceim.Chat{},
		pageSize:      pageSize,
		indexLookback: indexLookback,
		currentTime:   now,
	}
}

// Poll ingests messages newer than the last cursor. On first run it only sets
// the cursor, preventing historical messages from triggering replies.
func (p *Poller) Poll(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := p.currentTime().UTC()
	if err := p.discoverTestChats(ctx); err != nil {
		return Result{}, err
	}
	cursor, ok, err := p.store.GetPollCursor(cursorScopeAllMessages)
	if err != nil {
		return Result{}, err
	}
	result := Result{TestChatIDs: p.testChatIDs()}
	if !ok {
		if err := p.store.SetPollCursor(cursorScopeIntakeFloor, now); err != nil {
			return Result{}, err
		}
		if err := p.store.SetPollCursor(cursorScopeAllMessages, now); err != nil {
			return Result{}, err
		}
		result.ColdStart = true
		return result, nil
	}
	intakeFloor, floorSet, err := p.store.GetPollCursor(cursorScopeIntakeFloor)
	if err != nil {
		return Result{}, err
	}
	if !floorSet {
		intakeFloor = cursor
		if err := p.store.SetPollCursor(cursorScopeIntakeFloor, intakeFloor); err != nil {
			return Result{}, err
		}
	}
	queryStart := cursor.Add(-p.indexLookback)
	if queryStart.Before(intakeFloor) {
		queryStart = intakeFloor
	}

	general, err := p.searchAllMessages(ctx, serviceim.SearchMessagesRequest{
		StartISO:       queryStart.Format(time.RFC3339),
		EndISO:         now.Format(time.RFC3339),
		PageSize:       p.pageSize,
		ExcludeBotSend: true,
		ChatType:       p.chatTypeFilter(),
	})
	if err != nil {
		return Result{}, err
	}
	atMe, err := p.searchAllMessages(ctx, serviceim.SearchMessagesRequest{
		StartISO:       queryStart.Format(time.RFC3339),
		EndISO:         now.Format(time.RFC3339),
		PageSize:       p.pageSize,
		IncludeAtMe:    true,
		AtChatterIDs:   []string{p.cfg.OwnerOpenID},
		ExcludeBotSend: true,
		ChatType:       p.chatTypeFilter(),
	})
	if err != nil {
		return Result{}, err
	}
	mentioned := map[string]bool{}
	for _, msg := range atMe.Items {
		if msg.MessageID != "" {
			mentioned[msg.MessageID] = true
		}
	}
	seen := map[string]serviceim.Message{}
	for _, msg := range general.Items {
		if msg.MessageID != "" {
			seen[msg.MessageID] = msg
		}
	}
	for _, msg := range atMe.Items {
		if msg.MessageID != "" {
			seen[msg.MessageID] = mergeMessage(seen[msg.MessageID], msg)
		}
	}
	if err := p.hydrateExactMessageDetails(ctx, seen); err != nil {
		return result, err
	}
	if err := p.hydrateChatMetadata(ctx, seen); err != nil {
		return result, err
	}
	if err := p.hydrateEmptyMessages(ctx, seen); err != nil {
		return result, err
	}
	result.Seen = len(seen)
	for _, msg := range seen {
		event := p.eventFromMessage(msg, mentioned[msg.MessageID])
		item := domain.NewWorkItem(event)
		if p.cfg.Classify != nil {
			decision, err := p.cfg.Classify(ctx, item)
			if err != nil {
				return result, err
			}
			item.WorkKind = decision.WorkKind
			item.Priority = decision.Priority
		}
		receipt, err := p.store.RecordWorkIntake(ctx, item)
		if err != nil {
			return result, err
		}
		if receipt.Disposition == domain.IntakeAdmitted {
			result.Inserted++
		}
	}
	if err := p.store.SetPollCursor(cursorScopeAllMessages, now); err != nil {
		return result, err
	}
	return result, nil
}

func (p *Poller) hydrateChatMetadata(ctx context.Context, messages map[string]serviceim.Message) error {
	getter, ok := p.im.(chatBatchGetter)
	if !ok || len(messages) == 0 {
		return nil
	}
	seenIDs := make(map[string]struct{}, len(messages))
	chatIDs := make([]string, 0, len(messages))
	for _, msg := range messages {
		if msg.ChatID == "" {
			continue
		}
		if _, exists := seenIDs[msg.ChatID]; exists {
			continue
		}
		seenIDs[msg.ChatID] = struct{}{}
		chatIDs = append(chatIDs, msg.ChatID)
	}
	details, err := getter.BatchGetChats(ctx, chatIDs)
	if err != nil {
		return err
	}
	for chatID, chat := range details {
		p.chatDetails[chatID] = chat
	}
	for messageID, msg := range messages {
		chat, exists := details[msg.ChatID]
		if !exists {
			continue
		}
		if msg.ChatType == "" {
			msg.ChatType = chat.ChatMode
			if msg.ChatType == "" {
				msg.ChatType = chat.ChatType
			}
		}
		if msg.ChatPartnerOpenID == "" {
			msg.ChatPartnerOpenID = chat.P2PTargetOpenID
		}
		messages[messageID] = msg
	}
	return nil
}

func (p *Poller) hydrateExactMessageDetails(
	ctx context.Context,
	seen map[string]serviceim.Message,
) error {
	getter, ok := p.im.(exactMessageGetter)
	if !ok || len(seen) == 0 {
		return nil
	}
	messageIDs := make([]string, 0, len(seen))
	for messageID := range seen {
		messageIDs = append(messageIDs, messageID)
	}
	for start := 0; start < len(messageIDs); start += 50 {
		end := min(start+50, len(messageIDs))
		details, err := getter.GetMessages(ctx, messageIDs[start:end])
		if err != nil {
			return err
		}
		for _, detail := range details {
			current, exists := seen[detail.MessageID]
			if !exists {
				continue
			}
			seen[detail.MessageID] = mergeMessage(current, detail)
		}
	}
	return nil
}

func mergeMessage(base, overlay serviceim.Message) serviceim.Message {
	out := base
	if out.MessageID == "" {
		out.MessageID = overlay.MessageID
	}
	if out.ChatID == "" {
		out.ChatID = overlay.ChatID
	}
	if out.ChatType == "" {
		out.ChatType = overlay.ChatType
	}
	if out.ChatPartnerOpenID == "" {
		out.ChatPartnerOpenID = overlay.ChatPartnerOpenID
	}
	if out.RootMessageID == "" {
		out.RootMessageID = overlay.RootMessageID
	}
	if out.ReplyToMessageID == "" {
		out.ReplyToMessageID = overlay.ReplyToMessageID
	}
	if out.ThreadID == "" {
		out.ThreadID = overlay.ThreadID
	}
	if out.SenderOpenID == "" {
		out.SenderOpenID = overlay.SenderOpenID
	}
	if out.SenderType == "" {
		out.SenderType = overlay.SenderType
	}
	if out.MsgType == "" {
		out.MsgType = overlay.MsgType
	}
	if strings.TrimSpace(out.Content) == "" {
		out.Content = overlay.Content
	}
	if len(out.Mentions) == 0 {
		out.Mentions = append([]domain.Mention(nil), overlay.Mentions...)
	}
	if out.CreateTime == "" {
		out.CreateTime = overlay.CreateTime
	}
	if out.UpdateTime == "" {
		out.UpdateTime = overlay.UpdateTime
	}
	return out
}

func (p *Poller) hydrateEmptyMessages(ctx context.Context, seen map[string]serviceim.Message) error {
	lister, ok := p.im.(recentMessageLister)
	if !ok {
		return nil
	}
	chatIDs := map[string]bool{}
	for _, msg := range seen {
		if msg.ChatID != "" && needsRecentHydration(msg) {
			chatIDs[msg.ChatID] = true
		}
	}
	for chatID := range chatIDs {
		recent, err := lister.ListRecentMessages(ctx, serviceim.ListRecentMessagesRequest{ChatID: chatID, PageSize: 50})
		if err != nil {
			return err
		}
		for _, msg := range recent.Items {
			current, ok := seen[msg.MessageID]
			if !ok {
				continue
			}
			if needsRecentHydration(current) {
				seen[msg.MessageID] = mergeMessage(current, msg)
			}
		}
	}
	return nil
}

func needsRecentHydration(msg serviceim.Message) bool {
	return strings.TrimSpace(msg.Content) == "" ||
		msg.ChatType == "" ||
		(strings.EqualFold(msg.ChatType, "p2p") && msg.ChatPartnerOpenID == "") ||
		msg.SenderOpenID == "" ||
		msg.SenderType == "" ||
		msg.MsgType == "" ||
		msg.CreateTime == ""
}

func (p *Poller) searchAllMessages(ctx context.Context, req serviceim.SearchMessagesRequest) (serviceim.SearchMessagesResult, error) {
	var out serviceim.SearchMessagesResult
	for {
		result, err := p.im.SearchMessages(ctx, req)
		if err != nil {
			return serviceim.SearchMessagesResult{}, err
		}
		out.Items = append(out.Items, result.Items...)
		out.HasMore = result.HasMore
		out.PageToken = result.PageToken
		if !result.HasMore || result.PageToken == "" {
			return out, nil
		}
		req.PageToken = result.PageToken
	}
}

func (p *Poller) chatTypeFilter() string {
	if p.cfg.IncludePrivate {
		return ""
	}
	return "group"
}

func (p *Poller) discoverTestChats(ctx context.Context) error {
	if p.discovered {
		p.discovered = true
		return nil
	}
	queries := uniqueNonEmpty(append([]string{p.cfg.ChatQuery}, p.cfg.AssistantNames...))
	for _, query := range queries {
		result, err := p.im.SearchChats(ctx, serviceim.SearchChatsRequest{Query: query, PageSize: 20})
		if err != nil {
			return err
		}
		for _, chat := range result.Items {
			if chat.ChatID != "" {
				p.testChats[chat.ChatID] = chat
			}
		}
	}
	p.discovered = true
	return nil
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (p *Poller) eventFromMessage(msg serviceim.Message, mentionedOwner bool) domain.NormalizedEvent {
	chat := p.testChats[msg.ChatID]
	mentions := append([]domain.Mention(nil), msg.Mentions...)
	if mentionedOwner {
		hasOwner := false
		for _, mention := range mentions {
			if mention.OpenID == p.cfg.OwnerOpenID {
				hasOwner = true
				break
			}
		}
		if !hasOwner {
			mentions = append(mentions, domain.Mention{OpenID: p.cfg.OwnerOpenID})
		}
	}
	chatName := chat.Name
	if chatName == "" {
		chatName = p.chatDetails[msg.ChatID].Name
	}
	if chatName == "" {
		chatName = msg.ChatID
	}
	chatType := msg.ChatType
	if chatType == "" {
		chatType = chat.ChatMode
	}
	if chatType == "" {
		chatType = p.chatDetails[msg.ChatID].ChatMode
	}
	return domain.NormalizedEvent{
		Source:           domain.SourcePoll,
		EventID:          "poll:" + msg.MessageID,
		MessageID:        msg.MessageID,
		ChatID:           msg.ChatID,
		ChatName:         chatName,
		ChatType:         chatType,
		ChatPartnerID:    msg.ChatPartnerOpenID,
		RootMessageID:    msg.RootMessageID,
		ReplyToMessageID: msg.ReplyToMessageID,
		ThreadID:         msg.ThreadID,
		SenderID:         msg.SenderOpenID,
		SenderType:       msg.SenderType,
		Content:          msg.Content,
		Mentions:         mentions,
		CreatedAt:        parseMessageTime(msg.CreateTime),
		RawDigest:        digestMessage(msg),
		InTestScope:      chat.ChatID != "",
	}
}

func (p *Poller) testChatIDs() []string {
	out := make([]string, 0, len(p.testChats))
	for id := range p.testChats {
		out = append(out, id)
	}
	return out
}

func parseMessageTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed
	}
	if millis, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(millis).UTC()
	}
	return time.Time{}
}

func digestMessage(msg serviceim.Message) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		msg.MessageID,
		msg.ChatID,
		msg.RootMessageID,
		msg.ReplyToMessageID,
		msg.ThreadID,
		msg.Content,
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// Package im exposes typed IM services shared by CLI and lark-agent code.
package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Caller is the API surface required by this service.
type Caller interface {
	CallAPI(context.Context, APIRequest) (interface{}, error)
}

// Service implements the agent tools.Messenger interface.
type Service struct {
	caller      Caller
	ownerOpenID string
}

// SendMessageRequest sends one fully encoded Lark message to an exact chat.
type SendMessageRequest struct {
	ChatID         string
	MessageType    string
	Content        string
	IdempotencyKey string
}

type SendMessageResult struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id"`
}

// SearchChatsRequest searches chats visible to the requested identity.
type SearchChatsRequest struct {
	Query     string
	PageSize  int
	PageToken string
	As        Identity
}

// SearchChatsResult is a single page of visible chats.
type SearchChatsResult struct {
	Items     []Chat
	HasMore   bool
	PageToken string
}

// Chat is the subset of Lark chat metadata needed by the live agent.
type Chat struct {
	ChatID          string
	Name            string
	ChatMode        string
	ChatType        string
	P2PTargetOpenID string
}

// SearchMessagesRequest searches messages visible to the current user.
type SearchMessagesRequest struct {
	Query          string
	ChatIDs        []string
	AtChatterIDs   []string
	StartISO       string
	EndISO         string
	ChatType       string
	PageSize       int
	PageToken      string
	IncludeAtMe    bool
	ExcludeBotSend bool
}

// SearchMessagesResult is a single page of message search results.
type SearchMessagesResult struct {
	Items     []Message
	HasMore   bool
	PageToken string
}

// ListRecentMessagesRequest lists recent messages from a single conversation.
type ListRecentMessagesRequest struct {
	ChatID    string
	PageSize  int
	PageToken string
	Order     string
}

// ListRecentMessagesResult is a single page of conversation messages.
type ListRecentMessagesResult struct {
	Items     []Message
	HasMore   bool
	PageToken string
}

// SemanticReplyContextRequest asks for one bounded same-chat window that
// includes the exact delegated target and messages observed after it.
type SemanticReplyContextRequest struct {
	ChatID          string
	TargetMessageID string
	Since           time.Time
	MaxMessages     int
}

// SemanticReplyContext is the conservative input to semantic owner-answer
// matching. Incomplete or withdrawn contexts must not authorize a reply.
type SemanticReplyContext struct {
	Messages      []Message
	ContextCutoff time.Time
	Incomplete    bool
	Withdrawn     bool
}

// ContextMode describes how messages were selected around the target.
type ContextMode = domain.ContextMode

const (
	ContextModeAdjacent   = domain.ContextModeAdjacent
	ContextModeReplyChain = domain.ContextModeReplyChain
	ContextModeThread     = domain.ContextModeThread
)

// MessageContextRequest asks for the latest context around a message.
type MessageContextRequest struct {
	Mode               ContextMode
	ChatID             string
	MessageID          string
	RootMessageID      string
	ReplyToMessageID   string
	ThreadID           string
	CreatedAt          time.Time
	Limit              int
	After              time.Time
	IncludeAppMessages bool
}

// ContextSelection records the bounded relation-aware context decision.
type ContextSelection = domain.ContextSelection

// MessageContext contains recent messages and whether the owner has already replied.
type MessageContext struct {
	Messages     []Message
	OwnerReplied bool
	Selection    ContextSelection
}

// Message is the subset of message data needed by the agent.
type Message struct {
	MessageID         string
	ChatID            string
	ChatType          string
	ChatPartnerOpenID string
	RootMessageID     string
	ReplyToMessageID  string
	ThreadID          string
	SenderOpenID      string
	SenderDisplayName string
	SenderType        string
	MsgType           string
	Content           string
	Attachments       []MessageAttachment
	Mentions          []domain.Mention
	CreateTime        string
	UpdateTime        string
}

// MessageAttachment is a typed reference to message evidence that can be
// fetched through the Lark resource API when the model needs it.
type MessageAttachment struct {
	Type             string
	Key              string
	MediaType        string
	Data             []byte
	Readable         bool
	UnreadableReason string
}

type messageResourceCaller interface {
	GetMessageResource(context.Context, MessageResourceRequest) (MessageResource, error)
}

// ImageHydrationLimits bounds ephemeral image evidence for one model request.
type ImageHydrationLimits struct {
	MaxImages     int
	MaxImageBytes int64
	MaxTotalBytes int64
	As            Identity
}

// NewService creates an IM service.
func NewService(caller Caller, ownerOpenID string) *Service {
	return &Service{caller: caller, ownerOpenID: ownerOpenID}
}

// HydrateContextImages fetches image evidence serially and records an explicit
// unreadable reason for every image that cannot be supplied to the model.
func (s *Service) HydrateContextImages(
	ctx context.Context,
	messages []Message,
	limits ImageHydrationLimits,
) []Message {
	out := append([]Message(nil), messages...)
	if limits.MaxImages <= 0 || limits.MaxImages > 2 {
		limits.MaxImages = 2
	}
	if limits.MaxImageBytes <= 0 || limits.MaxImageBytes > 1<<20 {
		limits.MaxImageBytes = 1 << 20
	}
	if limits.MaxTotalBytes <= 0 || limits.MaxTotalBytes > 2<<20 {
		limits.MaxTotalBytes = 2 << 20
	}
	reader, available := s.caller.(messageResourceCaller)
	seen := 0
	var total int64
	for messageIndex := range out {
		out[messageIndex].Attachments = append(
			[]MessageAttachment(nil),
			out[messageIndex].Attachments...,
		)
		for attachmentIndex := range out[messageIndex].Attachments {
			attachment := &out[messageIndex].Attachments[attachmentIndex]
			if attachment.Type != "image" {
				continue
			}
			if seen >= limits.MaxImages {
				attachment.UnreadableReason = "image_count_limit_reached"
				continue
			}
			seen++
			if !available {
				attachment.UnreadableReason = "image_resource_reader_unavailable"
				continue
			}
			remaining := limits.MaxTotalBytes - total
			if remaining <= 0 {
				attachment.UnreadableReason = "image_total_size_limit_reached"
				continue
			}
			maxBytes := min(limits.MaxImageBytes, remaining)
			resource, err := reader.GetMessageResource(ctx, MessageResourceRequest{
				MessageID: out[messageIndex].MessageID,
				FileKey:   attachment.Key,
				Type:      "image",
				As:        limits.As,
				MaxBytes:  maxBytes,
			})
			if err != nil {
				attachment.UnreadableReason = "image_download_failed"
				continue
			}
			if resource.TooLarge || int64(len(resource.Data)) > maxBytes {
				if maxBytes < limits.MaxImageBytes {
					attachment.UnreadableReason = "image_total_size_limit_reached"
				} else {
					attachment.UnreadableReason = "image_exceeds_size_limit"
				}
				continue
			}
			mediaType := supportedImageMediaType(resource.Data)
			if mediaType == "" {
				attachment.UnreadableReason = "unsupported_image_type"
				continue
			}
			attachment.MediaType = mediaType
			attachment.Data = append([]byte(nil), resource.Data...)
			attachment.Readable = true
			attachment.UnreadableReason = ""
			total += int64(len(resource.Data))
		}
	}
	return out
}

func supportedImageMediaType(data []byte) string {
	mediaType := strings.TrimSpace(strings.SplitN(
		http.DetectContentType(data),
		";",
		2,
	)[0])
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return mediaType
	default:
		return ""
	}
}

// SearchChats searches chats visible to the requested identity.
func (s *Service) SearchChats(ctx context.Context, req SearchChatsRequest) (SearchChatsResult, error) {
	if s.caller == nil {
		return SearchChatsResult{}, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	pageSize := clamp(req.PageSize, 1, 100, 20)
	params := map[string]any{"page_size": pageSize}
	if req.PageToken != "" {
		params["page_token"] = req.PageToken
	}
	body := map[string]any{}
	if req.Query != "" {
		body["query"] = req.Query
	}
	identity := req.As
	if identity == "" {
		identity = IdentityUser
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodPost,
		Path:   "/open-apis/im/v2/chats/search",
		Params: params,
		Data:   body,
		As:     identity,
	})
	if err != nil {
		return SearchChatsResult{}, err
	}
	data := responseData(result)
	items := arrayValue(data["items"])
	out := SearchChatsResult{
		HasMore:   boolValue(data["has_more"]),
		PageToken: stringValue(data["page_token"]),
	}
	for _, raw := range items {
		item := mapValue(raw)
		if meta := mapValue(item["meta_data"]); len(meta) > 0 {
			item = meta
		}
		chat := parseChat(item)
		if chat.ChatID != "" {
			out.Items = append(out.Items, chat)
		}
	}
	return out, nil
}

// BatchGetChats hydrates conversation metadata for message search results.
func (s *Service) BatchGetChats(ctx context.Context, chatIDs []string) (map[string]Chat, error) {
	if s.caller == nil {
		return nil, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	out := make(map[string]Chat, len(chatIDs))
	for start := 0; start < len(chatIDs); start += 50 {
		end := min(start+50, len(chatIDs))
		result, err := s.caller.CallAPI(ctx, APIRequest{
			Method: http.MethodPost,
			Path:   "/open-apis/im/v1/chats/batch_query",
			Params: map[string]any{"user_id_type": "open_id"},
			Data:   map[string]any{"chat_ids": chatIDs[start:end]},
			As:     IdentityUser,
		})
		if err != nil {
			return nil, err
		}
		for _, raw := range arrayValue(responseData(result)["items"]) {
			chat := parseChat(mapValue(raw))
			if chat.ChatID != "" {
				out[chat.ChatID] = chat
			}
		}
	}
	return out, nil
}

// SearchMessages searches messages visible to the user identity.
func (s *Service) SearchMessages(ctx context.Context, req SearchMessagesRequest) (SearchMessagesResult, error) {
	if s.caller == nil {
		return SearchMessagesResult{}, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	pageSize := clamp(req.PageSize, 1, 50, 20)
	params := map[string]any{"page_size": pageSize}
	if req.PageToken != "" {
		params["page_token"] = req.PageToken
	}
	filter := map[string]any{}
	if len(req.ChatIDs) > 0 {
		filter["chat_ids"] = req.ChatIDs
	}
	if len(req.AtChatterIDs) > 0 {
		filter["at_chatter_ids"] = req.AtChatterIDs
	}
	if req.StartISO != "" || req.EndISO != "" {
		timeRange := map[string]any{}
		if req.StartISO != "" {
			timeRange["start_time"] = req.StartISO
		}
		if req.EndISO != "" {
			timeRange["end_time"] = req.EndISO
		}
		filter["time_range"] = timeRange
	}
	if req.ChatType != "" {
		filter["chat_type"] = req.ChatType
	}
	if req.IncludeAtMe {
		filter["is_at_me"] = true
	}
	if req.ExcludeBotSend {
		filter["exclude_from_types"] = []string{"bot"}
	}
	body := map[string]any{"query": req.Query}
	if len(filter) > 0 {
		body["filter"] = filter
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodPost,
		Path:   "/open-apis/im/v1/messages/search",
		Params: params,
		Data:   body,
		As:     IdentityUser,
	})
	if err != nil {
		return SearchMessagesResult{}, err
	}
	data := responseData(result)
	out := SearchMessagesResult{
		HasMore:   boolValue(data["has_more"]),
		PageToken: stringValue(data["page_token"]),
	}
	for _, raw := range arrayValue(data["items"]) {
		msg := parseMessage(raw)
		if msg.MessageID != "" {
			out.Items = append(out.Items, msg)
		}
	}
	return out, nil
}

// ListRecentMessages lists recent messages in a single conversation as the user.
func (s *Service) ListRecentMessages(ctx context.Context, req ListRecentMessagesRequest) (ListRecentMessagesResult, error) {
	if s.caller == nil {
		return ListRecentMessagesResult{}, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if req.ChatID == "" {
		return ListRecentMessagesResult{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "chat_id is required").WithParam("chat_id")
	}
	order := "ByCreateTimeDesc"
	if req.Order == "asc" {
		order = "ByCreateTimeAsc"
	}
	pageSize := clamp(req.PageSize, 1, 50, 20)
	params := map[string]any{
		"container_id_type":         "chat",
		"container_id":              req.ChatID,
		"sort_type":                 order,
		"page_size":                 strconv.Itoa(pageSize),
		"card_msg_content_type":     "raw_card_content",
		"only_thread_root_messages": "false",
		"with_sender_name":          "true",
	}
	if req.PageToken != "" {
		params["page_token"] = req.PageToken
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodGet,
		Path:   "/open-apis/im/v1/messages",
		Params: params,
		As:     IdentityUser,
	})
	if err != nil {
		return ListRecentMessagesResult{}, err
	}
	data := responseData(result)
	out := ListRecentMessagesResult{
		HasMore:   boolValue(data["has_more"]),
		PageToken: stringValue(data["page_token"]),
	}
	for _, raw := range arrayValue(data["items"]) {
		msg := parseMessage(raw)
		if msg.MessageID != "" {
			out.Items = append(out.Items, msg)
		}
	}
	return out, nil
}

// GetMessages reads exact messages as the user identity.
func (s *Service) GetMessages(ctx context.Context, messageIDs []string) ([]Message, error) {
	if s.caller == nil {
		return nil, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	ids := uniqueStrings(messageIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 50 {
		return nil, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"at most 50 message IDs can be read at once",
		).WithParam("message_ids")
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodGet,
		Path:   "/open-apis/im/v1/messages/mget",
		Params: map[string]any{
			"message_ids":           ids,
			"card_msg_content_type": "raw_card_content",
			"with_sender_name":      "true",
		},
		As: IdentityUser,
	})
	if err != nil {
		return nil, err
	}
	data := responseData(result)
	messages := make([]Message, 0, len(ids))
	for _, raw := range arrayValue(data["items"]) {
		message := parseMessage(raw)
		if message.MessageID != "" {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

// GetSemanticReplyContext reads the exact target and paginates the same chat
// from newest to oldest until the target-time boundary is covered.
func (s *Service) GetSemanticReplyContext(
	ctx context.Context,
	req SemanticReplyContextRequest,
) (SemanticReplyContext, error) {
	if strings.TrimSpace(req.ChatID) == "" {
		return SemanticReplyContext{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"chat_id is required",
		).WithParam("chat_id")
	}
	if strings.TrimSpace(req.TargetMessageID) == "" {
		return SemanticReplyContext{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"target_message_id is required",
		).WithParam("target_message_id")
	}
	targets, err := s.GetMessages(ctx, []string{req.TargetMessageID})
	if err != nil {
		return SemanticReplyContext{}, err
	}
	if len(targets) == 0 {
		return SemanticReplyContext{
			ContextCutoff: time.Now().UTC(),
			Withdrawn:     true,
		}, nil
	}
	target := targets[0]
	if target.ChatID != req.ChatID {
		return SemanticReplyContext{
			ContextCutoff: time.Now().UTC(),
			Incomplete:    true,
		}, nil
	}
	since := req.Since
	if targetTime := parseMessageTime(target.CreateTime); since.IsZero() ||
		(!targetTime.IsZero() && targetTime.Before(since)) {
		since = targetTime
	}
	maxMessages := clamp(req.MaxMessages, 1, 200, 100)
	byID := map[string]Message{target.MessageID: target}
	incomplete := false
	if target.ThreadID != "" ||
		target.ReplyToMessageID != "" ||
		target.RootMessageID != "" {
		related, relationErr := s.GetMessageContext(ctx, MessageContextRequest{
			ChatID:           req.ChatID,
			MessageID:        target.MessageID,
			RootMessageID:    target.RootMessageID,
			ReplyToMessageID: target.ReplyToMessageID,
			ThreadID:         target.ThreadID,
			CreatedAt:        parseMessageTime(target.CreateTime),
			Limit:            min(maxMessages, 30),
		})
		if relationErr != nil {
			return SemanticReplyContext{}, relationErr
		}
		incomplete = related.Selection.Incomplete
		for _, message := range related.Messages {
			if message.ChatID == req.ChatID {
				byID[message.MessageID] = message
			}
		}
	}
	pageToken := ""
	for {
		page, pageErr := s.ListRecentMessages(ctx, ListRecentMessagesRequest{
			ChatID:    req.ChatID,
			PageSize:  min(50, maxMessages),
			PageToken: pageToken,
		})
		if pageErr != nil {
			return SemanticReplyContext{}, pageErr
		}
		reachedBoundary := false
		for _, message := range page.Items {
			if message.ChatID != req.ChatID {
				continue
			}
			createdAt := parseMessageTime(message.CreateTime)
			if !since.IsZero() && !createdAt.IsZero() && createdAt.Before(since) {
				reachedBoundary = true
				continue
			}
			byID[message.MessageID] = message
			if len(byID) > maxMessages {
				incomplete = true
				break
			}
			if !since.IsZero() && !createdAt.IsZero() && !createdAt.After(since) {
				reachedBoundary = true
			}
		}
		if incomplete || reachedBoundary || !page.HasMore {
			break
		}
		if page.PageToken == "" || page.PageToken == pageToken {
			incomplete = true
			break
		}
		pageToken = page.PageToken
	}
	messages := make([]Message, 0, min(len(byID), maxMessages))
	for _, message := range byID {
		messages = append(messages, message)
	}
	sortMessagesChronologically(messages)
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	return SemanticReplyContext{
		Messages:      messages,
		ContextCutoff: time.Now().UTC(),
		Incomplete:    incomplete,
	}, nil
}

// ListThreadMessages reads one chronological page from a thread as the user.
func (s *Service) ListThreadMessages(
	ctx context.Context,
	threadID string,
	pageSize int,
	pageToken string,
) (ListRecentMessagesResult, error) {
	if s.caller == nil {
		return ListRecentMessagesResult{}, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if strings.TrimSpace(threadID) == "" {
		return ListRecentMessagesResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"thread_id is required",
		).WithParam("thread_id")
	}
	params := map[string]any{
		"container_id_type":     "thread",
		"container_id":          threadID,
		"sort_type":             "ByCreateTimeAsc",
		"page_size":             strconv.Itoa(clamp(pageSize, 1, 50, 50)),
		"card_msg_content_type": "raw_card_content",
		"with_sender_name":      "true",
	}
	if pageToken != "" {
		params["page_token"] = pageToken
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodGet,
		Path:   "/open-apis/im/v1/messages",
		Params: params,
		As:     IdentityUser,
	})
	if err != nil {
		return ListRecentMessagesResult{}, err
	}
	data := responseData(result)
	out := ListRecentMessagesResult{
		HasMore:   boolValue(data["has_more"]),
		PageToken: stringValue(data["page_token"]),
	}
	for _, raw := range arrayValue(data["items"]) {
		message := parseMessage(raw)
		if message.MessageID != "" {
			out.Items = append(out.Items, message)
		}
	}
	return out, nil
}

// GetMessageContext resolves bounded same-chat context and separately preserves
// the pre-send owner-replied check used by delegated replies.
func (s *Service) GetMessageContext(ctx context.Context, req MessageContextRequest) (MessageContext, error) {
	if strings.TrimSpace(req.ChatID) == "" {
		return MessageContext{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"chat_id is required",
		).WithParam("chat_id")
	}
	limit := clamp(req.Limit, 1, 30, 20)
	if !req.After.IsZero() {
		return s.ownerReplyContext(ctx, req, limit)
	}
	if req.Mode == ContextModeAdjacent {
		return s.adjacentContext(ctx, req, limit)
	}
	if req.ThreadID != "" {
		result, err := s.threadContext(ctx, req, limit)
		if err == nil && !result.Selection.Incomplete {
			return result, nil
		}
		if err == nil {
			return s.mergeThreadFallback(ctx, req, limit, result)
		}
		return s.adjacentFallback(ctx, req, limit, relationIDs(req), err)
	}
	if req.ReplyToMessageID != "" || req.RootMessageID != "" {
		result, err := s.replyChainContext(ctx, req, limit)
		if err == nil && !result.Selection.Incomplete {
			return result, nil
		}
		if err == nil {
			return s.mergeReplyFallback(ctx, req, limit, result)
		}
		return s.adjacentFallback(ctx, req, limit, replyChainMissingIDs(result, req), err)
	}

	adjacent, err := s.adjacentContext(ctx, req, limit)
	if err != nil {
		return MessageContext{}, err
	}
	for _, message := range adjacent.Messages {
		if message.MessageID != req.MessageID {
			continue
		}
		hydrated := req
		hydrated.RootMessageID = message.RootMessageID
		hydrated.ReplyToMessageID = message.ReplyToMessageID
		hydrated.ThreadID = message.ThreadID
		if hydrated.CreatedAt.IsZero() {
			hydrated.CreatedAt = parseMessageTime(message.CreateTime)
		}
		if hydrated.ThreadID != "" {
			result, resolveErr := s.threadContext(ctx, hydrated, limit)
			if resolveErr == nil && !result.Selection.Incomplete {
				return result, nil
			}
			if resolveErr == nil {
				return s.mergeThreadFallback(ctx, hydrated, limit, result)
			}
			return incompleteContext(adjacent, relationIDs(hydrated), resolveErr), nil
		}
		if hydrated.ReplyToMessageID != "" || hydrated.RootMessageID != "" {
			result, resolveErr := s.replyChainContext(ctx, hydrated, limit)
			if resolveErr == nil && !result.Selection.Incomplete {
				return result, nil
			}
			if resolveErr == nil {
				return s.mergeReplyFallback(ctx, hydrated, limit, result)
			}
			return incompleteContext(
				adjacent,
				replyChainMissingIDs(result, hydrated),
				resolveErr,
			), nil
		}
		break
	}
	return adjacent, nil
}

func (s *Service) ownerReplyContext(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
) (MessageContext, error) {
	recent, err := s.ListRecentMessages(ctx, ListRecentMessagesRequest{
		ChatID: req.ChatID, PageSize: limit,
	})
	if err != nil {
		return MessageContext{}, err
	}
	out := MessageContext{
		Messages: recent.Items,
		Selection: ContextSelection{
			Mode:            ContextModeAdjacent,
			AnchorMessageID: req.MessageID,
		},
	}
	for _, message := range recent.Items {
		if message.SenderOpenID == s.ownerOpenID &&
			message.MessageID != req.MessageID &&
			messageAfter(message, req.After) {
			out.OwnerReplied = true
			break
		}
	}
	return out, nil
}

func (s *Service) adjacentContext(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
) (MessageContext, error) {
	recent, err := s.ListRecentMessages(ctx, ListRecentMessagesRequest{
		ChatID: req.ChatID, PageSize: min(50, max(limit*2, limit)),
	})
	if err != nil {
		return MessageContext{}, err
	}
	effectiveReq := req
	if effectiveReq.CreatedAt.IsZero() && effectiveReq.MessageID != "" {
		for _, message := range recent.Items {
			if message.MessageID == effectiveReq.MessageID {
				effectiveReq.CreatedAt = parseMessageTime(message.CreateTime)
				break
			}
		}
	}
	if effectiveReq.MessageID != "" && effectiveReq.CreatedAt.IsZero() {
		return MessageContext{
			Selection: ContextSelection{
				Mode:              ContextModeAdjacent,
				AnchorMessageID:   effectiveReq.MessageID,
				Incomplete:        true,
				MissingMessageIDs: []string{effectiveReq.MessageID},
				Reason:            "target message was not found in the readable same-chat window",
			},
		}, nil
	}
	messages := make([]Message, 0, len(recent.Items))
	for _, message := range recent.Items {
		if message.ChatID != req.ChatID {
			continue
		}
		if messageAfterTarget(message, effectiveReq) {
			continue
		}
		messages = append(messages, message)
	}
	sortMessagesChronologically(messages)
	messages, truncated := compactMessages(messages, effectiveReq, limit)
	return MessageContext{
		Messages: messages,
		Selection: ContextSelection{
			Mode:            ContextModeAdjacent,
			AnchorMessageID: req.MessageID,
			Truncated:       truncated || recent.HasMore,
		},
	}, nil
}

func (s *Service) replyChainContext(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
) (MessageContext, error) {
	requested := uniqueStrings(append(relationIDs(req), req.MessageID))
	messages, err := s.GetMessages(ctx, requested)
	if err != nil {
		return MessageContext{}, err
	}
	byID := make(map[string]Message, len(messages))
	for _, message := range messages {
		if message.ChatID == req.ChatID {
			byID[message.MessageID] = message
		}
	}
	var missing []string
	for _, relationID := range relationIDs(req) {
		if _, ok := byID[relationID]; !ok {
			missing = append(missing, relationID)
		}
	}
	if len(missing) > 0 {
		return unavailableReplyChain(
			req,
			missing,
			"quoted relation message is unavailable in the current chat",
		)
	}

	const maxReplyDepth = 16
	cursor := req.ReplyToMessageID
	for depth := 0; cursor != "" && depth < maxReplyDepth; depth++ {
		message, ok := byID[cursor]
		if !ok || message.ReplyToMessageID == "" {
			break
		}
		parentID := message.ReplyToMessageID
		if _, seen := byID[parentID]; seen {
			cursor = parentID
			continue
		}
		parents, parentErr := s.GetMessages(ctx, []string{parentID})
		if parentErr != nil {
			return MessageContext{}, parentErr
		}
		parent, ok := messageInChat(parents, parentID, req.ChatID)
		if !ok {
			return unavailableReplyChain(
				req,
				[]string{parentID},
				"quoted ancestor message is unavailable in the current chat",
			)
		}
		byID[parentID] = parent
		cursor = parentID
	}

	messages = messages[:0]
	for _, message := range byID {
		messages = append(messages, message)
	}
	sortMessagesChronologically(messages)
	messages, truncated := compactMessages(messages, req, limit)
	targetMissing := req.MessageID != "" && !containsMessage(messages, req.MessageID)
	var missingMessageIDs []string
	var reason string
	if targetMissing {
		missingMessageIDs = []string{req.MessageID}
		reason = "target message is unavailable in the current chat"
	}
	return MessageContext{
		Messages: messages,
		Selection: ContextSelection{
			Mode:              ContextModeReplyChain,
			AnchorMessageID:   req.MessageID,
			RootMessageID:     req.RootMessageID,
			ReplyToMessageID:  req.ReplyToMessageID,
			Truncated:         truncated,
			Incomplete:        targetMissing,
			MissingMessageIDs: missingMessageIDs,
			Reason:            reason,
		},
	}, nil
}

func (s *Service) threadContext(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
) (MessageContext, error) {
	const maxThreadMessages = 100
	var (
		messages  []Message
		pageToken string
		hasMore   bool
		found     bool
	)
	for len(messages) < maxThreadMessages {
		page, err := s.ListThreadMessages(ctx, req.ThreadID, min(50, maxThreadMessages-len(messages)), pageToken)
		if err != nil {
			return MessageContext{}, err
		}
		for _, message := range page.Items {
			if message.ChatID != req.ChatID {
				continue
			}
			if messageAfterTarget(message, req) {
				continue
			}
			messages = append(messages, message)
			if message.MessageID == req.MessageID {
				found = true
				break
			}
		}
		hasMore = page.HasMore
		if found || !page.HasMore || page.PageToken == "" {
			break
		}
		pageToken = page.PageToken
	}
	rawCount := len(messages)
	rootMissing := false
	if req.RootMessageID != "" && !containsMessage(messages, req.RootMessageID) {
		root, err := s.GetMessages(ctx, []string{req.RootMessageID})
		if err != nil {
			return MessageContext{}, err
		}
		if len(root) > 0 && root[0].ChatID == req.ChatID {
			messages = append(messages, root[0])
		} else {
			rootMissing = true
		}
	}
	sortMessagesChronologically(messages)
	messages, compacted := compactMessages(messages, req, limit)
	incomplete := (!found && req.MessageID != "") || rootMissing
	return MessageContext{
		Messages: messages,
		Selection: ContextSelection{
			Mode:              ContextModeThread,
			AnchorMessageID:   req.MessageID,
			RootMessageID:     req.RootMessageID,
			ReplyToMessageID:  req.ReplyToMessageID,
			Truncated:         compacted || (!found && hasMore) || rawCount >= maxThreadMessages,
			Incomplete:        incomplete,
			MissingMessageIDs: missingThreadMessageIDs(found, rootMissing, req),
			Reason:            threadIncompleteReason(found, rootMissing, req),
		},
	}, nil
}

func (s *Service) mergeThreadFallback(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
	thread MessageContext,
) (MessageContext, error) {
	adjacent, err := s.adjacentContext(ctx, req, limit)
	if err != nil {
		// Preserve the already bounded same-chat thread context when the
		// optional adjacent fallback cannot be read.
		return thread, nil //nolint:nilerr
	}
	combined := append(append([]Message(nil), thread.Messages...), adjacent.Messages...)
	combined = uniqueMessages(combined, req.ChatID)
	sortMessagesChronologically(combined)
	combined, compacted := compactMessages(combined, req, limit)
	thread.Messages = combined
	thread.Selection.Truncated = thread.Selection.Truncated || adjacent.Selection.Truncated || compacted
	if thread.Selection.Reason == "" {
		thread.Selection.Reason = "thread context is incomplete; same-chat adjacent messages were added"
	} else {
		thread.Selection.Reason += "; same-chat adjacent messages were added"
	}
	return thread, nil
}

func (s *Service) mergeReplyFallback(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
	replyChain MessageContext,
) (MessageContext, error) {
	adjacent, err := s.adjacentContext(ctx, req, limit)
	if err != nil {
		return replyChain, nil //nolint:nilerr // Preserve verified quoted messages if optional fallback fails.
	}
	combined := append(append([]Message(nil), replyChain.Messages...), adjacent.Messages...)
	combined = uniqueMessages(combined, req.ChatID)
	sortMessagesChronologically(combined)
	compactedMessages, compacted := compactMessages(combined, req, limit)
	replyChain.Messages = compactedMessages
	replyChain.Selection.Truncated =
		replyChain.Selection.Truncated || adjacent.Selection.Truncated || compacted
	if containsMessage(replyChain.Messages, req.MessageID) {
		replyChain.Selection.Incomplete = false
		replyChain.Selection.MissingMessageIDs = nil
		replyChain.Selection.Reason = ""
	}
	return replyChain, nil
}

func (s *Service) adjacentFallback(
	ctx context.Context,
	req MessageContextRequest,
	limit int,
	missing []string,
	cause error,
) (MessageContext, error) {
	adjacent, err := s.adjacentContext(ctx, req, limit)
	if err != nil {
		return MessageContext{}, err
	}
	return incompleteContext(adjacent, missing, cause), nil
}

func incompleteContext(context MessageContext, missing []string, cause error) MessageContext {
	context.Selection.Mode = ContextModeAdjacent
	context.Selection.Incomplete = true
	context.Selection.MissingMessageIDs = uniqueStrings(missing)
	context.Selection.Reason = "quoted context is incomplete"
	if cause != nil {
		context.Selection.Reason += ": " + cause.Error()
	}
	return context
}

// ReplyAsUser replies to a specific message using user identity.
func (s *Service) ReplyAsUser(ctx context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	return s.reply(ctx, req, IdentityUser)
}

// ReplyAsBot replies to a specific message using bot identity.
func (s *Service) ReplyAsBot(ctx context.Context, req tools.ReplyRequest) (tools.ReplyResult, error) {
	return s.reply(ctx, req, IdentityBot)
}

// SendMessageAsBot sends an HTTP-only bot message to an exact chat. It does not
// construct or start a WebSocket consumer.
func (s *Service) SendMessageAsBot(ctx context.Context, req SendMessageRequest) (SendMessageResult, error) {
	if s.caller == nil {
		return SendMessageResult{}, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if strings.TrimSpace(req.ChatID) == "" {
		return SendMessageResult{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "chat_id is required").WithParam("chat_id")
	}
	switch req.MessageType {
	case "text", "post":
	default:
		return SendMessageResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported message type %q",
			req.MessageType,
		).WithParam("message_type")
	}
	if strings.TrimSpace(req.Content) == "" || !json.Valid([]byte(req.Content)) {
		return SendMessageResult{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"message content must be valid non-empty JSON",
		).WithParam("content")
	}
	if err := validateMessageUUID(req.IdempotencyKey); err != nil {
		return SendMessageResult{}, err
	}
	body := map[string]any{
		"receive_id": req.ChatID,
		"msg_type":   req.MessageType,
		"content":    req.Content,
	}
	if req.IdempotencyKey != "" {
		body["uuid"] = req.IdempotencyKey
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodPost,
		Path:   "/open-apis/im/v1/messages",
		Params: map[string]any{"receive_id_type": "chat_id"},
		Data:   body,
		As:     IdentityBot,
	})
	if err != nil {
		return SendMessageResult{}, err
	}
	data := responseData(result)
	out := SendMessageResult{
		MessageID: stringValue(data["message_id"]),
		ChatID:    stringValue(data["chat_id"]),
	}
	if out.MessageID == "" {
		return SendMessageResult{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"message create response is missing message_id",
		)
	}
	if out.ChatID == "" {
		out.ChatID = req.ChatID
	}
	return out, nil
}

func (s *Service) reply(ctx context.Context, req tools.ReplyRequest, as Identity) (tools.ReplyResult, error) {
	if s.caller == nil {
		return tools.ReplyResult{}, errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if err := validateMessageUUID(req.IdempotencyKey); err != nil {
		return tools.ReplyResult{}, err
	}
	content, err := json.Marshal(map[string]string{"text": req.Text})
	if err != nil {
		return tools.ReplyResult{}, errs.NewInternalError(errs.SubtypeUnknown, "encode reply text").WithCause(err)
	}
	body := map[string]any{
		"msg_type": "text",
		"content":  string(content),
	}
	if req.IdempotencyKey != "" {
		body["uuid"] = req.IdempotencyKey
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodPost,
		Path:   fmt.Sprintf("/open-apis/im/v1/messages/%s/reply", url.PathEscape(req.MessageID)),
		Data:   body,
		As:     as,
	})
	if err != nil {
		return tools.ReplyResult{}, err
	}
	return parseReplyResult(result), nil
}

// CreateReactionAsBot adds one emoji reaction as the assistant bot.
func (s *Service) CreateReactionAsBot(ctx context.Context, messageID, emojiType string) (string, error) {
	if s.caller == nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if strings.TrimSpace(messageID) == "" {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "message_id is required").WithParam("message_id")
	}
	if strings.TrimSpace(emojiType) == "" {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "emoji_type is required").WithParam("emoji_type")
	}
	result, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodPost,
		Path: fmt.Sprintf(
			"/open-apis/im/v1/messages/%s/reactions",
			url.PathEscape(messageID),
		),
		Data: map[string]any{
			"reaction_type": map[string]any{"emoji_type": emojiType},
		},
		As: IdentityBot,
	})
	if err != nil {
		return "", err
	}
	reactionID := stringValue(responseData(result)["reaction_id"])
	if reactionID == "" {
		return "", errs.NewInternalError(errs.SubtypeInvalidResponse, "reaction create response is missing reaction_id")
	}
	return reactionID, nil
}

// DeleteReactionAsBot removes a reaction previously added by the assistant bot.
func (s *Service) DeleteReactionAsBot(ctx context.Context, messageID, reactionID string) error {
	if s.caller == nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if strings.TrimSpace(messageID) == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "message_id is required").WithParam("message_id")
	}
	if strings.TrimSpace(reactionID) == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "reaction_id is required").WithParam("reaction_id")
	}
	_, err := s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodDelete,
		Path: fmt.Sprintf(
			"/open-apis/im/v1/messages/%s/reactions/%s",
			url.PathEscape(messageID),
			url.PathEscape(reactionID),
		),
		As: IdentityBot,
	})
	return err
}

// NotifyOwner sends a bot message to the owner.
func (s *Service) NotifyOwner(ctx context.Context, req tools.NotifyRequest) error {
	if s.caller == nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "IM API caller is not configured")
	}
	if s.ownerOpenID == "" {
		return errs.NewConfigError(errs.SubtypeInvalidConfig, "owner open_id is required").WithField("owner.open_id")
	}
	if err := validateMessageUUID(req.IdempotencyKey); err != nil {
		return err
	}
	content, err := json.Marshal(map[string]string{"text": req.Text})
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "encode notification text").WithCause(err)
	}
	body := map[string]any{
		"receive_id": s.ownerOpenID,
		"msg_type":   "text",
		"content":    string(content),
	}
	if req.IdempotencyKey != "" {
		body["uuid"] = req.IdempotencyKey
	}
	_, err = s.caller.CallAPI(ctx, APIRequest{
		Method: http.MethodPost,
		Path:   "/open-apis/im/v1/messages",
		Params: map[string]any{"receive_id_type": "open_id"},
		Data:   body,
		As:     IdentityBot,
	})
	return err
}

func validateMessageUUID(uuid string) error {
	if len(uuid) <= 50 {
		return nil
	}
	return errs.NewValidationError(
		errs.SubtypeInvalidArgument,
		"message uuid exceeds the 50-character public API limit",
	).WithParam("uuid")
}

func parseReplyResult(result interface{}) tools.ReplyResult {
	data, _ := result.(map[string]any)
	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}
	out := tools.ReplyResult{}
	if v, ok := data["message_id"].(string); ok {
		out.MessageID = v
	}
	if v, ok := data["chat_id"].(string); ok {
		out.ChatID = v
	}
	return out
}

func parseMessage(raw any) Message {
	item := mapValue(raw)
	if meta := mapValue(item["meta_data"]); len(meta) > 0 {
		for k, v := range meta {
			if _, exists := item[k]; !exists {
				item[k] = v
			}
		}
	}
	sender := mapValue(item["sender"])
	chatPartner := mapValue(item["chat_partner"])
	senderID := mapValue(sender["id"])
	if len(senderID) == 0 {
		senderID = mapValue(sender["sender_id"])
	}
	msg := Message{
		MessageID:         firstString(item, "message_id", "messageId"),
		ChatID:            firstString(item, "chat_id", "chatId"),
		ChatType:          firstString(item, "chat_type", "chatType"),
		ChatPartnerOpenID: firstString(chatPartner, "open_id", "openId"),
		RootMessageID:     firstString(item, "root_id", "rootId"),
		ReplyToMessageID:  firstString(item, "parent_id", "parentId", "reply_to", "replyTo"),
		ThreadID:          firstString(item, "thread_id", "threadId"),
		SenderType:        firstString(sender, "sender_type", "type"),
		MsgType:           firstString(item, "msg_type", "message_type"),
		Content:           stringValue(item["content"]),
		Mentions:          parseMentions(item["mentions"]),
		CreateTime:        firstString(item, "create_time", "createTime"),
		UpdateTime:        firstString(item, "update_time", "updateTime"),
	}
	msg.SenderDisplayName = firstString(sender, "name", "display_name", "displayName")
	msg.SenderOpenID = firstString(senderID, "open_id", "openId", "user_id", "userId")
	if msg.SenderOpenID == "" {
		msg.SenderOpenID = firstString(sender, "open_id", "openId", "sender_id", "senderId", "id")
	}
	if msg.SenderOpenID == "" {
		msg.SenderOpenID = firstString(item, "sender_open_id", "senderOpenId", "sender_id", "senderId")
	}
	if msg.Content == "" {
		msg.Content = textFromContent(item["body"])
	} else {
		msg.Content = textFromContent(msg.Content)
	}
	if msg.MsgType == "image" {
		imageKey := contentString(item["content"], "image_key")
		if imageKey == "" {
			imageKey = contentString(item["body"], "image_key")
		}
		if imageKey != "" {
			msg.Attachments = []MessageAttachment{{Type: "image", Key: imageKey}}
		}
		if strings.TrimSpace(msg.Content) == "" {
			msg.Content = "[图片]"
		}
	}
	return msg
}

func contentString(raw any, key string) string {
	value := raw
	if encoded, ok := raw.(string); ok {
		var decoded map[string]any
		if json.Unmarshal([]byte(encoded), &decoded) != nil {
			return ""
		}
		value = decoded
	}
	return firstString(mapValue(value), key)
}

func parseChat(item map[string]any) Chat {
	if meta := mapValue(item["meta_data"]); len(meta) > 0 {
		item = meta
	}
	return Chat{
		ChatID:          firstString(item, "chat_id", "chatId"),
		Name:            stringValue(item["name"]),
		ChatMode:        firstString(item, "chat_mode", "chatMode"),
		ChatType:        firstString(item, "chat_type", "chatType"),
		P2PTargetOpenID: firstString(item, "p2p_target_id", "p2pTargetId"),
	}
}

func parseMentions(raw any) []domain.Mention {
	items := arrayValue(raw)
	out := make([]domain.Mention, 0, len(items))
	for _, rawItem := range items {
		item := mapValue(rawItem)
		mention := domain.Mention{
			Key:  firstString(item, "key"),
			Name: firstString(item, "name"),
		}
		id := item["id"]
		switch v := id.(type) {
		case string:
			mention.OpenID = v
		default:
			mention.OpenID = firstString(mapValue(v), "open_id", "openId", "user_id", "userId")
		}
		if mention.Key != "" || mention.OpenID != "" || mention.Name != "" {
			out = append(out, mention)
		}
	}
	return out
}

func textFromContent(raw any) string {
	switch v := raw.(type) {
	case string:
		var body map[string]any
		if err := json.Unmarshal([]byte(v), &body); err == nil {
			return textFromContent(body)
		}
		return v
	case map[string]any:
		for _, locale := range []string{"en_us", "zh_cn", "ja_jp"} {
			if localized, ok := v[locale]; ok {
				if text := textFromContent(localized); text != "" {
					return text
				}
			}
		}
		if content := stringValue(v["content"]); content != "" {
			return textFromContent(content)
		}
		if text := stringValue(v["text"]); text != "" {
			return text
		}
		if name := firstString(v, "user_name", "name"); name != "" {
			return "@" + name
		}
		if content, ok := v["content"]; ok {
			return textFromContent(content)
		}
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := textFromContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, ""))
	}
	return ""
}

func messageAfter(msg Message, after time.Time) bool {
	if after.IsZero() {
		return true
	}
	parsed := parseMessageTime(msg.CreateTime)
	return !parsed.IsZero() && parsed.After(after)
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

func responseData(result any) map[string]any {
	data := mapValue(result)
	if nested := mapValue(data["data"]); len(nested) > 0 {
		return nested
	}
	return data
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func arrayValue(v any) []any {
	switch items := v.(type) {
	case []any:
		return items
	default:
		return nil
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(m[key]); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolValue(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func relationIDs(req MessageContextRequest) []string {
	return uniqueStrings([]string{req.RootMessageID, req.ReplyToMessageID})
}

func unavailableReplyChain(
	req MessageContextRequest,
	missing []string,
	reason string,
) (MessageContext, error) {
	err := errs.NewInternalError(errs.SubtypeInvalidResponse, "%s", reason)
	return MessageContext{
		Selection: ContextSelection{
			Mode:              ContextModeReplyChain,
			AnchorMessageID:   req.MessageID,
			RootMessageID:     req.RootMessageID,
			ReplyToMessageID:  req.ReplyToMessageID,
			Incomplete:        true,
			MissingMessageIDs: uniqueStrings(missing),
			Reason:            reason,
		},
	}, err
}

func replyChainMissingIDs(result MessageContext, req MessageContextRequest) []string {
	if len(result.Selection.MissingMessageIDs) > 0 {
		return result.Selection.MissingMessageIDs
	}
	return relationIDs(req)
}

func messageInChat(messages []Message, messageID string, chatID string) (Message, bool) {
	for _, message := range messages {
		if message.MessageID == messageID && message.ChatID == chatID {
			return message, true
		}
	}
	return Message{}, false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
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

func messageAfterTarget(message Message, req MessageContextRequest) bool {
	if req.CreatedAt.IsZero() {
		return false
	}
	createdAt := parseMessageTime(message.CreateTime)
	return !createdAt.IsZero() && createdAt.After(req.CreatedAt)
}

func sortMessagesChronologically(messages []Message) {
	sort.SliceStable(messages, func(i, j int) bool {
		left := parseMessageTime(messages[i].CreateTime)
		right := parseMessageTime(messages[j].CreateTime)
		if left.Equal(right) {
			return messages[i].MessageID < messages[j].MessageID
		}
		if left.IsZero() {
			return false
		}
		if right.IsZero() {
			return true
		}
		return left.Before(right)
	})
}

func compactMessages(messages []Message, req MessageContextRequest, limit int) ([]Message, bool) {
	pinned := map[string]bool{
		req.RootMessageID:    req.RootMessageID != "",
		req.ReplyToMessageID: req.ReplyToMessageID != "",
		req.MessageID:        req.MessageID != "",
	}
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		if isAppContextMessage(message) &&
			!req.IncludeAppMessages &&
			!pinned[message.MessageID] {
			continue
		}
		filtered = append(filtered, message)
	}
	truncated := len(filtered) != len(messages)
	messages = filtered
	if len(messages) <= limit {
		return messages, truncated
	}
	selected := make(map[string]Message, limit)
	for _, message := range messages {
		if pinned[message.MessageID] && len(selected) < limit {
			selected[message.MessageID] = message
		}
	}
	for index := len(messages) - 1; index >= 0 && len(selected) < limit; index-- {
		message := messages[index]
		selected[message.MessageID] = message
	}
	out := make([]Message, 0, len(selected))
	for _, message := range selected {
		out = append(out, message)
	}
	sortMessagesChronologically(out)
	return out, true
}

func isAppContextMessage(message Message) bool {
	switch strings.ToLower(strings.TrimSpace(message.SenderType)) {
	case "app", "bot":
		return true
	default:
		return false
	}
}

func containsMessage(messages []Message, messageID string) bool {
	for _, message := range messages {
		if message.MessageID == messageID {
			return true
		}
	}
	return false
}

func uniqueMessages(messages []Message, chatID string) []Message {
	seen := make(map[string]bool, len(messages))
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.MessageID == "" ||
			seen[message.MessageID] ||
			message.ChatID != chatID {
			continue
		}
		seen[message.MessageID] = true
		out = append(out, message)
	}
	return out
}

func missingThreadMessageIDs(
	foundTarget bool,
	missingRoot bool,
	req MessageContextRequest,
) []string {
	var missing []string
	if !foundTarget && req.MessageID != "" {
		missing = append(missing, req.MessageID)
	}
	if missingRoot && req.RootMessageID != "" {
		missing = append(missing, req.RootMessageID)
	}
	return uniqueStrings(missing)
}

func threadIncompleteReason(
	foundTarget bool,
	missingRoot bool,
	req MessageContextRequest,
) string {
	switch {
	case !foundTarget && req.MessageID != "" && missingRoot:
		return "target and root messages were not found in the readable thread window"
	case !foundTarget && req.MessageID != "":
		return "target message was not found in the readable thread window"
	case missingRoot:
		return "root message was not found in the readable thread window"
	default:
		return ""
	}
}

func clamp(value, minValue, maxValue, defaultValue int) int {
	if value == 0 {
		value = defaultValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

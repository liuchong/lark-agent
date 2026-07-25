// Package ingest normalizes events from Lark sources and writes them to the
// durable inbox.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Store is the durable intake dependency.
type Store interface {
	RecordIntake(context.Context, domain.NormalizedEvent) (domain.IntakeReceipt, error)
}

// Ingestor normalizes and stores incoming events.
type Ingestor struct {
	store Store
}

// New creates an Ingestor.
func New(store Store) *Ingestor {
	return &Ingestor{store: store}
}

// Ingest writes normalized events. The context is reserved for future source
// adapters and keeps the API cancellation-ready.
func (i *Ingestor) Ingest(ctx context.Context, events []domain.NormalizedEvent) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var inserted int
	for _, event := range events {
		receipt, err := i.store.RecordIntake(ctx, event)
		if err != nil {
			return inserted, err
		}
		if receipt.Disposition == domain.IntakeAdmitted {
			inserted++
		}
	}
	return inserted, nil
}

// NormalizeRealtime flattens an im.message.receive_v1 payload.
func NormalizeRealtime(raw []byte) (domain.NormalizedEvent, error) {
	var processed struct {
		EventID    string            `json:"event_id"`
		MessageID  string            `json:"message_id"`
		CreateTime string            `json:"create_time"`
		CreatedAt  time.Time         `json:"created_at"`
		ChatID     string            `json:"chat_id"`
		ChatType   string            `json:"chat_type"`
		RootID     string            `json:"root_id"`
		ReplyTo    string            `json:"reply_to"`
		ThreadID   string            `json:"thread_id"`
		SenderID   string            `json:"sender_id"`
		SenderType string            `json:"sender_type"`
		Content    string            `json:"content"`
		Mentions   []json.RawMessage `json:"mentions"`
	}
	if err := json.Unmarshal(raw, &processed); err != nil {
		return domain.NormalizedEvent{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode Lark realtime event").WithCause(err)
	}
	if processed.MessageID != "" {
		return domain.NormalizedEvent{
			Source:           domain.SourceRealtime,
			EventID:          processed.EventID,
			MessageID:        processed.MessageID,
			ChatID:           processed.ChatID,
			ChatType:         processed.ChatType,
			RootMessageID:    processed.RootID,
			ReplyToMessageID: processed.ReplyTo,
			ThreadID:         processed.ThreadID,
			SenderID:         processed.SenderID,
			SenderType:       processed.SenderType,
			Content:          processed.Content,
			Mentions:         parseMentions(processed.Mentions),
			CreatedAt:        firstTime(parseLarkTimestamp(processed.CreateTime), processed.CreatedAt),
			RawDigest:        digest(raw),
		}, nil
	}

	var envelope struct {
		Header struct {
			EventID    string `json:"event_id"`
			CreateTime string `json:"create_time"`
		} `json:"header"`
		Event struct {
			Message struct {
				MessageID  string            `json:"message_id"`
				ChatID     string            `json:"chat_id"`
				ChatType   string            `json:"chat_type"`
				RootID     string            `json:"root_id"`
				ParentID   string            `json:"parent_id"`
				ThreadID   string            `json:"thread_id"`
				Content    string            `json:"content"`
				CreateTime string            `json:"create_time"`
				Mentions   []json.RawMessage `json:"mentions"`
			} `json:"message"`
			Sender struct {
				SenderType string `json:"sender_type"`
				SenderID   struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return domain.NormalizedEvent{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode Lark realtime event").WithCause(err)
	}
	return domain.NormalizedEvent{
		Source:           domain.SourceRealtime,
		EventID:          envelope.Header.EventID,
		MessageID:        envelope.Event.Message.MessageID,
		ChatID:           envelope.Event.Message.ChatID,
		ChatType:         envelope.Event.Message.ChatType,
		RootMessageID:    envelope.Event.Message.RootID,
		ReplyToMessageID: envelope.Event.Message.ParentID,
		ThreadID:         envelope.Event.Message.ThreadID,
		SenderID:         envelope.Event.Sender.SenderID.OpenID,
		SenderType:       envelope.Event.Sender.SenderType,
		Content:          envelope.Event.Message.Content,
		Mentions:         parseMentions(envelope.Event.Message.Mentions),
		CreatedAt:        parseLarkTimestamp(firstNonEmpty(envelope.Event.Message.CreateTime, envelope.Header.CreateTime)),
		RawDigest:        digest(raw),
	}, nil
}

// PollMessage is the already-projected user-token message search result.
type PollMessage struct {
	EventID          string
	MessageID        string
	ChatID           string
	RootMessageID    string
	ReplyToMessageID string
	ThreadID         string
	SenderID         string
	Content          string
	Mentions         []domain.Mention
}

// NormalizePoll converts a user-token search result to the shared event shape.
func NormalizePoll(msg PollMessage) domain.NormalizedEvent {
	raw := []byte(strings.Join([]string{
		msg.EventID,
		msg.MessageID,
		msg.ChatID,
		msg.RootMessageID,
		msg.ReplyToMessageID,
		msg.ThreadID,
		msg.Content,
	}, "\x00"))
	return domain.NormalizedEvent{
		Source:           domain.SourcePoll,
		EventID:          msg.EventID,
		MessageID:        msg.MessageID,
		ChatID:           msg.ChatID,
		RootMessageID:    msg.RootMessageID,
		ReplyToMessageID: msg.ReplyToMessageID,
		ThreadID:         msg.ThreadID,
		SenderID:         msg.SenderID,
		Content:          msg.Content,
		Mentions:         msg.Mentions,
		RawDigest:        digest(raw),
	}
}

func parseMentions(rawMentions []json.RawMessage) []domain.Mention {
	out := make([]domain.Mention, 0, len(rawMentions))
	for _, raw := range rawMentions {
		var item struct {
			Key    string `json:"key"`
			Name   string `json:"name"`
			OpenID string `json:"open_id"`
			ID     any    `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		mention := domain.Mention{Key: item.Key, Name: item.Name, OpenID: item.OpenID}
		switch v := item.ID.(type) {
		case string:
			mention.OpenID = v
		case map[string]any:
			if openID, ok := v["open_id"].(string); ok {
				mention.OpenID = openID
			}
		}
		if mention.OpenID != "" || mention.Name != "" {
			out = append(out, mention)
		}
	}
	return out
}

func parseLarkTimestamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	milliseconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

package lark

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Mention struct {
	Key    string `json:"key,omitempty"`
	OpenID string `json:"open_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

type EventEnvelope struct {
	EventID    string    `json:"event_id,omitempty"`
	MessageID  string    `json:"message_id"`
	ChatID     string    `json:"chat_id"`
	ChatType   string    `json:"chat_type,omitempty"`
	SenderID   string    `json:"sender_id,omitempty"`
	SenderType string    `json:"sender_type"`
	Content    string    `json:"content,omitempty"`
	RootID     string    `json:"root_id,omitempty"`
	ReplyTo    string    `json:"reply_to,omitempty"`
	ThreadID   string    `json:"thread_id,omitempty"`
	Mentions   []Mention `json:"mentions,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
}

func DecodeEvent(data []byte) (EventEnvelope, error) {
	var event EventEnvelope
	if err := json.Unmarshal(data, &event); err != nil {
		return EventEnvelope{}, fmt.Errorf("decode lark event: %w", err)
	}
	for name, value := range map[string]string{
		"message_id":  event.MessageID,
		"chat_id":     event.ChatID,
		"sender_type": event.SenderType,
	} {
		if strings.TrimSpace(value) == "" {
			return EventEnvelope{}, fmt.Errorf("lark event is missing %s", name)
		}
	}
	if event.SenderType != "user" && event.SenderType != "bot" {
		return EventEnvelope{}, fmt.Errorf("unsupported sender_type %q", event.SenderType)
	}
	return event, nil
}

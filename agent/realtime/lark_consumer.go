package realtime

import (
	"context"
	"encoding/json"
	"io"

	"github.com/liuchong/lark-agent/internal/lark"
)

const (
	messageReceiveEventKey = "im.message.receive_v1"
	privateMessageScope    = "im:message.p2p_msg:readonly"
	groupMentionScope      = "im:message.group_at_msg:readonly"
)

type LarkConsumerConfig struct {
	AppID     string
	AppSecret string
	BaseURL   string
}

type LarkConsumer struct {
	caller lark.Caller
	cfg    LarkConsumerConfig
}

func NewLarkConsumer(caller lark.Caller, cfg LarkConsumerConfig) *LarkConsumer {
	return &LarkConsumer{caller: caller, cfg: cfg}
}

func (c *LarkConsumer) Consume(ctx context.Context, out io.Writer) error {
	if _, err := lark.CheckPublishedApp(ctx, c.caller, c.cfg.AppID); err != nil {
		return err
	}
	consumer := lark.Consumer{
		AppID:     c.cfg.AppID,
		AppSecret: c.cfg.AppSecret,
		BaseURL:   c.cfg.BaseURL,
	}
	encoder := json.NewEncoder(out)
	return consumer.Consume(ctx, func(event lark.EventEnvelope) error {
		return encoder.Encode(event)
	})
}

func preflightRealtime(ctx context.Context, caller lark.Caller, appID string) error {
	_, err := lark.CheckPublishedApp(ctx, caller, appID)
	return err
}

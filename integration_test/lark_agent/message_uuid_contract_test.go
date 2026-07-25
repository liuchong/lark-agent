package larkagent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/tools"
	"github.com/liuchong/lark-agent/internal/apperr"
	"github.com/liuchong/lark-agent/internal/lark"
)

type messageUUIDCaller struct {
	calls int
}

func (c *messageUUIDCaller) CallAPI(context.Context, lark.APIRequest) (interface{}, error) {
	c.calls++
	return map[string]any{"data": map[string]any{"message_id": "om_reply"}}, nil
}

func TestPublicMessageWritesRejectOversizedUUIDBeforeSDKCall(t *testing.T) {
	oversized := strings.Repeat("x", 51)
	tests := []struct {
		name string
		call func(*lark.Service) error
	}{
		{
			name: "reply",
			call: func(service *lark.Service) error {
				_, err := service.ReplyAsBot(context.Background(), tools.ReplyRequest{
					MessageID:      "om_private",
					Text:           "reply",
					IdempotencyKey: oversized,
				})
				return err
			},
		},
		{
			name: "owner notification",
			call: func(service *lark.Service) error {
				return service.NotifyOwner(context.Background(), tools.NotifyRequest{
					Text:           "notice",
					IdempotencyKey: oversized,
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caller := &messageUUIDCaller{}
			err := test.call(lark.NewService(caller, "ou_owner"))
			problem, ok := errs.ProblemOf(err)
			if !ok || problem.Subtype != errs.SubtypeInvalidArgument || problem.Param != "uuid" {
				t.Fatalf("error=%v problem=%+v", err, problem)
			}
			if caller.calls != 0 {
				t.Fatalf("SDK boundary was invoked %d times", caller.calls)
			}
		})
	}
}

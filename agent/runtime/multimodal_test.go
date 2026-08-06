package runtime

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
)

func TestInitialUserMessageIncludesEphemeralConversationImages(t *testing.T) {
	bundle := agentcontext.Bundle{
		Event: domain.NormalizedEvent{MessageID: "om_target", Content: "请看上下文"},
		Conversation: []domain.NormalizedEvent{
			{
				MessageID:  "om_image",
				SenderName: "Ada",
				Content:    "[图片]",
				Attachments: []domain.Attachment{{
					Type:      "image",
					Key:       "img_evidence",
					MediaType: "image/png",
					Readable:  true,
					DataURL:   "data:image/png;base64,cG5n",
				}},
			},
		},
	}

	message := initialUserMessage(bundle)
	if message.Role != schema.User || len(message.UserInputMultiContent) != 3 {
		t.Fatalf("message=%+v", message)
	}
	if message.UserInputMultiContent[0].Type != schema.ChatMessagePartTypeText ||
		!strings.Contains(message.UserInputMultiContent[0].Text, "Evaluate this Lark message") {
		t.Fatalf("prompt part=%+v", message.UserInputMultiContent[0])
	}
	if message.UserInputMultiContent[1].Type != schema.ChatMessagePartTypeText ||
		!strings.Contains(message.UserInputMultiContent[1].Text, "om_image") ||
		!strings.Contains(message.UserInputMultiContent[1].Text, "Ada") {
		t.Fatalf("annotation part=%+v", message.UserInputMultiContent[1])
	}
	image := message.UserInputMultiContent[2]
	if image.Type != schema.ChatMessagePartTypeImageURL ||
		image.Image == nil ||
		image.Image.URL == nil ||
		*image.Image.URL != "data:image/png;base64,cG5n" {
		t.Fatalf("image part=%+v", image)
	}
}

package runtime

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
)

func initialUserMessage(bundle agentcontext.Bundle) *schema.Message {
	prompt := agentcontext.AgentUserPrompt(bundle)
	parts := []schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText,
		Text: prompt,
	}}
	appendImages := func(event domain.NormalizedEvent) {
		for _, attachment := range event.Attachments {
			if !attachment.Readable ||
				attachment.Type != "image" ||
				!strings.HasPrefix(attachment.DataURL, "data:image/") {
				continue
			}
			label := strings.TrimSpace(event.SenderName)
			if label == "" {
				label = strings.TrimSpace(event.SenderID)
			}
			dataURL := attachment.DataURL
			parts = append(parts,
				schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: fmt.Sprintf(
						"Image evidence for same-chat message %s from %s:",
						event.MessageID,
						label,
					),
				},
				schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{URL: &dataURL},
						Detail:            schema.ImageURLDetailHigh,
					},
				},
			)
		}
	}
	appendImages(bundle.Event)
	for _, event := range bundle.Conversation {
		if event.MessageID == bundle.Event.MessageID {
			continue
		}
		appendImages(event)
	}
	if len(parts) == 1 {
		return schema.UserMessage(prompt)
	}
	return &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
	}
}

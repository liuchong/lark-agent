package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
)

type fakeLarkContextProvider struct {
	request LarkContextRequest
	result  LarkContextResult
}

func (f *fakeLarkContextProvider) RecentMessages(
	_ context.Context,
	request LarkContextRequest,
) (LarkContextResult, error) {
	f.request = request
	return f.result, nil
}

func (f *fakeLarkContextProvider) SearchMessages(
	context.Context,
	string,
	[]string,
	int,
) ([]domain.NormalizedEvent, error) {
	return nil, nil
}

func TestGetLarkContextReturnsRelationSelectionMetadata(t *testing.T) {
	provider := &fakeLarkContextProvider{result: LarkContextResult{
		Messages: []domain.NormalizedEvent{{
			MessageID: "om_target",
			ChatID:    "oc_lobster",
			Content:   "为什么？",
		}},
		Selection: domain.ContextSelection{
			Mode:             domain.ContextModeThread,
			AnchorMessageID:  "om_target",
			RootMessageID:    "om_root",
			ReplyToMessageID: "om_parent",
			Truncated:        true,
		},
	}}
	definition := LarkContextDefinitions(provider)[0]
	execution, err := definition.Execute(context.Background(), json.RawMessage(`{
		"chat_id":"oc_lobster",
		"message_id":"om_target",
		"mode":"thread",
		"limit":30
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.Mode != domain.ContextModeThread ||
		provider.request.ChatID != "oc_lobster" ||
		provider.request.MessageID != "om_target" ||
		provider.request.Limit != 30 {
		t.Fatalf("request=%+v", provider.request)
	}
	content := execution.Content
	for _, want := range []string{
		`"mode":"thread"`,
		`"anchor_message_id":"om_target"`,
		`"root_id":"om_root"`,
		`"reply_to":"om_parent"`,
		`"truncated":true`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("tool content missing %q: %s", want, content)
		}
	}
}

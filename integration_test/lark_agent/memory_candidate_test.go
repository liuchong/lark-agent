package larkagent_test

import (
	"context"
	"path/filepath"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/control"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/memory"
	"github.com/liuchong/lark-agent/agent/storage"
)

type memoryCandidateModel struct{}

func (memoryCandidateModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return schema.AssistantMessage(`{
		"kind":"not_command",
		"confidence":0.98,
		"memory_candidate":{
			"kind":"response_feedback",
			"content":"判断修复状态时必须核对已合入的 PR，不能只根据旧聊天推测",
			"confidence":0.94
		}
	}`, nil), nil
}

func TestOwnerCorrectionCandidateRequiresConfirmationBeforeModelRetrieval(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_owner_memory_correction",
		SenderID:  "ou_owner",
		Content:   "这个已经修了。以后判断状态要先核对已经合入的 PR，不要只看旧聊天。",
	})
	resolver := control.NewSemanticResolver(memoryCandidateModel{}, store, "zh-CN")
	if _, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListMemories(context.Background(), "global", false, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 ||
		candidates[0].Status != memory.StatusCandidate ||
		candidates[0].SourceMessageID != item.Event.MessageID {
		t.Fatalf("candidates=%+v", candidates)
	}
	visible, err := store.SearchMemories(context.Background(), memory.Query{
		Text:   "核对已合入的 PR",
		Scopes: []string{"global"},
		Limit:  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 0 {
		t.Fatalf("unconfirmed candidate entered model context=%+v", visible)
	}
	if _, err := store.RecordMemoryFeedback(context.Background(), memory.Feedback{
		MemoryEntryID: candidates[0].ID,
		Verdict:       memory.FeedbackConfirm,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	visible, err = store.SearchMemories(context.Background(), memory.Query{
		Text:   "核对已合入的 PR",
		Scopes: []string{"global"},
		Limit:  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].ID != candidates[0].ID {
		t.Fatalf("confirmed memory did not survive restart=%+v", visible)
	}
}

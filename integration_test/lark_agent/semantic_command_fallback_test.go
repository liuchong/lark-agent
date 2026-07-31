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
	"github.com/liuchong/lark-agent/agent/storage"
)

type lowConfidenceSemanticCommandModel struct{}

func (lowConfidenceSemanticCommandModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return schema.AssistantMessage(`{
		"kind":"command",
		"command":"/approval approve 453 confirm",
		"confidence":0.60
	}`, nil), nil
}

func TestLowConfidenceCommandShapeFallsThroughToOrdinaryBusinessAnswer(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	resolver := control.NewSemanticResolver(lowConfidenceSemanticCommandModel{}, store, "zh-CN")
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_semantic_business_question",
		SenderID:  "ou_owner",
		Content:   "确认一下这个审批流程为什么需要二次校验？",
	})
	resolution, err := resolver.Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlNotCommand {
		t.Fatalf("business question was intercepted by control plane: %+v", resolution)
	}
}

package larkagent_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestOrdinaryPolicyQuestionReceivesTrustedRuntimeFacts(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck

	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_runtime_policy_question",
		SenderID:  "ou_owner",
		Content:   "再确认一下：当前高置信度自动发送的具体数值阈值是多少？0.85 和 0.70 分别管什么？",
	})
	resolution, err := control.NewSemanticResolver(
		lowConfidenceSemanticCommandModel{},
		store,
		"zh-CN",
	).Resolve(context.Background(), item, agentcontext.Bundle{
		User:         agentcontext.UserProfile{OpenID: "ou_owner"},
		Conversation: []domain.NormalizedEvent{item.Event},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Kind != domain.SemanticControlNotCommand {
		t.Fatalf("policy question was intercepted by control plane: %+v", resolution)
	}

	bundle, err := (agentcontext.Builder{
		User: agentcontext.UserProfile{OpenID: "ou_owner", Name: "测试负责人"},
		RuntimePolicy: agentcontext.RuntimePolicySnapshot{
			Authoritative:           true,
			MustNotInferFromRules:   true,
			Mode:                    domain.ModeAuto,
			AssistantReplyScope:     domain.ReplyScopeAllGroups,
			DelegatedReplyScope:     domain.ReplyScopeAllGroups,
			PrivateReplyScope:       domain.PrivateReplyScopeAll,
			OwnerWait:               (3 * time.Minute).String(),
			OwnerReplyConfidenceMin: 0.85,
			OwnerReplyRetry:         (5 * time.Minute).String(),
			ReplyConfidenceMin:      0.70,
			InvestigationProgress:   "enabled",
		},
	}).Build(item)
	if err != nil {
		t.Fatal(err)
	}
	prompt := agentcontext.AgentUserPrompt(bundle)
	for _, want := range []string{
		`"owner_reply_confidence_min":0.85`,
		`"reply_confidence_min":0.7`,
		`"owner_wait":"3m0s"`,
		`"authoritative":true`,
		`"must_not_infer_from_workspace_rules":true`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("model prompt missing %q:\n%s", want, prompt)
		}
	}
}

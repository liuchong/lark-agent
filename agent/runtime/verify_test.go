package runtime

import (
	"strings"
	"testing"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
)

func TestVerifyCodingDecisionRejectsUnsupportedCodeClaim(t *testing.T) {
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "POST /api/sample/items 每次都会直接访问 SampleDB 吗？"},
	}, domain.Decision{
		Kind:      domain.DecisionReply,
		Risk:      domain.RiskLow,
		ReplyText: "这个接口每次都会直接访问 SampleDB，没有缓存。",
	}, map[string]bool{})
	if err == nil {
		t.Fatal("accepted unsupported code claim")
	}
	if !strings.Contains(err.Error(), "coding reply has no cited code evidence") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestVerifyCodingDecisionAllowsExplicitUnknownWithoutSources(t *testing.T) {
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "POST /api/sample/items 每次都会直接访问 SampleDB 吗？"},
	}, domain.Decision{
		Kind:      domain.DecisionReply,
		Risk:      domain.RiskLow,
		ReplyText: "我现在没有足够代码证据确认它是否每次直接访问 SampleDB，需要Owner继续确认具体实现路径。",
	}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCodingDecisionRejectsSupportingOnlyEvidenceForProductionClaim(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "examples/upload/main.go",
		Digest:       "sha256:example",
		Kind:         "workspace_file",
	}
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "生产代码的图片审核在哪里执行？"},
	}, domain.Decision{
		Kind:      domain.DecisionReply,
		Risk:      domain.RiskLow,
		ReplyText: "生产实现会在上传时调用审核接口。",
		Sources:   []domain.SourceRef{source},
	}, map[string]bool{sourceKey(source): true})
	if err == nil {
		t.Fatal("accepted an example file as proof of production behavior")
	}
	if !strings.Contains(err.Error(), "supporting evidence") {
		t.Fatalf("wrong error: %v", err)
	}
}

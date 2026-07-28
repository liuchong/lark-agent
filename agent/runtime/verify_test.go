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
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceVerified,
		ReplyText:      "这个接口每次都会直接访问 SampleDB，没有缓存。",
	}, map[string]bool{}, map[string]bool{}, 0)
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
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "我现在没有足够代码证据确认它是否每次直接访问 SampleDB，需要Owner继续确认具体实现路径。",
	}, map[string]bool{}, map[string]bool{}, 1)
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
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceVerified,
		ReplyText:      "生产实现会在上传时调用审核接口。",
		Sources:        []domain.SourceRef{source},
	}, map[string]bool{sourceKey(source): true}, map[string]bool{}, 1)
	if err == nil {
		t.Fatal("accepted an example file as proof of production behavior")
	}
	if !strings.Contains(err.Error(), "supporting evidence") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestVerifyCodingDecisionRejectsSearchCandidateWithoutAuthoritativeRead(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "service/content_type.go",
		Digest:       "sha256:candidate",
		Kind:         "workspace_file",
	}
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码 GetType 未命中时返回什么？"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceVerified,
		ReplyText:      "GetType 未命中时返回 image/png。",
		Sources:        []domain.SourceRef{source},
	}, map[string]bool{sourceKey(source): true}, map[string]bool{}, 1)
	if err == nil || !strings.Contains(err.Error(), "authoritative production read") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyCodingDecisionDoesNotTreatOrdinaryNeedPhraseAsUnknown(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "service/content_type.go",
		Digest:       "sha256:candidate",
		Kind:         "workspace_file",
	}
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码 GetType 未命中时返回什么？"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceVerified,
		ReplyText:      "GetType 未命中时需要返回 image/png。",
		Sources:        []domain.SourceRef{source},
	}, map[string]bool{sourceKey(source): true}, map[string]bool{}, 1)
	if err == nil || !strings.Contains(err.Error(), "authoritative production read") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyCodingDecisionAllowsExplicitNotFoundWithoutProductionRead(t *testing.T) {
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "在配置的工作区中没有找到 NormalizeContentType。",
	}, map[string]bool{}, map[string]bool{}, 1)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCodingDecisionRejectsInsufficientWithoutCodeInvestigation(t *testing.T) {
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		Risk:           domain.RiskLow,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      canonicalInsufficientCodingReply,
	}, map[string]bool{}, map[string]bool{}, 0)
	if err == nil || !strings.Contains(err.Error(), "successful workspace code investigation") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyCodingDecisionRejectsApprovalBypass(t *testing.T) {
	err := verifyCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionRequestApproval,
		Risk:           domain.RiskMedium,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType，需要测试负责人确认。",
	}, map[string]bool{}, map[string]bool{}, 0)
	if err == nil || !strings.Contains(err.Error(), "cannot finish as request_approval") {
		t.Fatalf("err=%v", err)
	}
}

func TestNormalizeCodingDecisionCanonicalizesInsufficientMixedClaim(t *testing.T) {
	decision := normalizeCodingDecision(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType，所以它一定会回退到 GetType。",
	})
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

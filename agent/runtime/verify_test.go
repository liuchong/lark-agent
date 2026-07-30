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

func TestVerifyGroundedCodingReplyRejectsUncitedRepositoryPath(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/SampleRequest.java",
		Digest:       "sha256:request",
		Kind:         "workspace_file",
	}
	err := verifyGroundedCodingReply(
		"请核对示例事件行为",
		domain.Decision{
			Kind:           domain.DecisionReply,
			EvidenceStatus: domain.EvidenceVerified,
			ReplyText:      "依据：sample-project/sample-module/sample-client/SampleWrapperTest.java",
			Sources:        []domain.SourceRef{source},
		},
		map[string]string{
			sourceKey(source): "class SampleRequest { private String sampleContent; }",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not backed by a current-run read") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyGroundedCodingReplyRejectsSearchOnlyRepositoryPath(t *testing.T) {
	readSource := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/SampleRequest.java",
		Digest:       "sha256:request",
		Kind:         "workspace_file",
	}
	searchOnlySource := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/SampleWrapperTest.java",
		Digest:       "sha256:test",
		Kind:         "workspace_file",
	}
	err := verifyGroundedCodingReply(
		"请核对示例事件行为",
		domain.Decision{
			Kind:           domain.DecisionReply,
			EvidenceStatus: domain.EvidenceVerified,
			ReplyText:      "依据：sample-project/sample-module/sample-client/SampleWrapperTest.java",
			Sources:        []domain.SourceRef{readSource, searchOnlySource},
		},
		map[string]string{
			sourceKey(readSource): "class SampleRequest { private String sampleContent; }",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not backed by a current-run read") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyGroundedCodingReplyRejectsDirectoryAndExtensionlessPaths(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/Message.java",
		Digest:       "sha256:message",
		Kind:         "workspace_file",
	}
	for _, reply := range []string{
		"依据：sample-project/sample-module/sample-client",
		"依据：sample-project/sample-module/Makefile",
		"依据：sample-project/sample-module/Dockerfile",
		"依据：Makefile",
	} {
		err := verifyGroundedCodingReply(
			"请核对示例事件行为",
			domain.Decision{
				Kind:           domain.DecisionReply,
				EvidenceStatus: domain.EvidenceVerified,
				ReplyText:      reply,
				Sources:        []domain.SourceRef{source},
			},
			map[string]string{
				sourceKey(source): "class Message {}",
			},
		)
		if err == nil || !strings.Contains(err.Error(), "repository path") {
			t.Fatalf("reply=%q err=%v", reply, err)
		}
	}
}

func TestVerifyGroundedCodingReplyAllowsMIMETypeAndAbsoluteAPIRoute(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "service/router.go",
		Digest:       "sha256:router",
		Kind:         "workspace_file",
	}
	err := verifyGroundedCodingReply(
		"请核对接口返回值",
		domain.Decision{
			Kind:           domain.DecisionReply,
			EvidenceStatus: domain.EvidenceVerified,
			ReplyText: "结论：/api/sample/items 返回 image/jpeg。" +
				"依据：service/router.go。",
			Sources: []domain.SourceRef{source},
		},
		map[string]string{
			sourceKey(source): `router.POST("/api/sample/items", handler)
const result = "image/jpeg"`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyGroundedCodingReplyRejectsOpaqueDeclarationForConcreteShape(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/SampleRequest.java",
		Digest:       "sha256:request",
		Kind:         "workspace_file",
	}
	for _, content := range []string{
		"class SampleRequest { private String sampleContent; }",
		"type SampleRequest struct { sampleContent string }",
		"type SampleRequest struct { sampleContent []byte }",
	} {
		err := verifyGroundedCodingReply(
			"Sample-Client SampleRequest 的 sampleContent 是什么结构？",
			domain.Decision{
				Kind:           domain.DecisionReply,
				EvidenceStatus: domain.EvidenceVerified,
				ReplyText:      "结论：sampleContent 是 JSON 结构。",
				Sources:        []domain.SourceRef{source},
			},
			map[string]string{
				sourceKey(source): content,
			},
		)
		if err == nil || !strings.Contains(err.Error(), "concrete serialized shape") {
			t.Fatalf("content=%q err=%v", content, err)
		}
	}
}

func TestVerifyGroundedCodingReplyRejectsUnsupportedConcreteJSONExample(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "sample-project/sample-module/docs/sample-protocol-guide.md",
		Digest:       "sha256:guide",
		Kind:         "workspace_file",
	}
	err := verifyGroundedCodingReply(
		"Sample-Client SampleRequest 的 sampleContent 是什么结构？",
		domain.Decision{
			Kind:           domain.DecisionReply,
			EvidenceStatus: domain.EvidenceVerified,
			ReplyText:      `结论：sampleContent 具体为 {"text":"invented"}。`,
			Sources:        []domain.SourceRef{source},
		},
		map[string]string{
			sourceKey(source): `sampleContent example: {\"content\":\"sample value\"}`,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported serialized example") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyGroundedCodingReplyRejectsIdentifierAbsentFromCitedReads(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/Message.java",
		Digest:       "sha256:message",
		Kind:         "workspace_file",
	}
	err := verifyGroundedCodingReply(
		"请核对示例事件行为",
		domain.Decision{
			Kind:           domain.DecisionReply,
			EvidenceStatus: domain.EvidenceVerified,
			ReplyText: "结论：本地通过 sampleFlag、modifyTime 和 sampleVersion 收敛。" +
				"依据：sample-project/sample-module/sample-client/Message.java",
			Sources: []domain.SourceRef{source},
		},
		map[string]string{
			sourceKey(source): "class Message { boolean sampleFlag; long sampleTimestamp; long sampleVersion; }",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "modifyTime") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyGroundedCodingReplyAcceptsCitedPathsAndIdentifiers(t *testing.T) {
	source := domain.SourceRef{
		RelativePath: "sample-project/sample-module/sample-client/Message.java",
		Digest:       "sha256:message",
		Kind:         "workspace_file",
	}
	err := verifyGroundedCodingReply(
		"Sample-Client SampleRequest 的 sampleContent 是什么结构？",
		domain.Decision{
			Kind:           domain.DecisionReply,
			EvidenceStatus: domain.EvidenceVerified,
			ReplyText: "结论：本地通过 sampleFlag、sampleTimestamp 和 sampleVersion 收敛。" +
				"依据：sample-project/sample-module/sample-client/Message.java",
			Sources: []domain.SourceRef{source},
		},
		map[string]string{
			sourceKey(source): `class Message {
    boolean sampleFlag;
    long sampleTimestamp;
    long sampleVersion;
}
Example payload: {"content":"sample value"}`,
		},
	)
	if err != nil {
		t.Fatal(err)
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

func TestNormalizeCodingDecisionDoesNotRenderNegativeResultAfterAnyMatch(t *testing.T) {
	searches := codingSearchEvidence{}
	searches.Record(
		"search_workspace",
		`{"query":"NormalizeContentType"}`,
		`{"results":[],"truncated":false,"files_scanned":40,"directories_scanned":8}`,
	)
	searches.Record(
		"search_workspace",
		`{"query":"NormalizeContentType definition"}`,
		`{"results":[{"source":{"relative_path":"content.go"}}],"truncated":false,"files_scanned":40,"directories_scanned":8}`,
	)
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestNormalizeCodingDecisionDoesNotTrustUnparseableSearchReport(t *testing.T) {
	searches := codingSearchEvidence{}
	searches.Record(
		"search_workspace",
		`{"query":"NormalizeContentType"}`,
		`{"unexpected":"shape"}`,
	)
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestNormalizeCodingDecisionDoesNotInventMissingSearchMetadata(t *testing.T) {
	searches := codingSearchEvidence{}
	searches.Record(
		"search_workspace",
		`{"query":"NormalizeContentType"}`,
		`{"results":[],"files_scanned":40,"directories_scanned":8}`,
	)
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestNormalizeCodingDecisionRendersFiveCompleteSearchReceipts(t *testing.T) {
	searches := codingSearchEvidence{}
	for _, query := range []string{"one", "two", "three", "four", "five"} {
		searches.Record(
			"search_workspace",
			`{"query":"`+query+`"}`,
			`{"results":[],"truncated":false,"files_scanned":40,"directories_scanned":8}`,
		)
	}
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText == canonicalInsufficientCodingReply ||
		!strings.Contains(decision.ReplyText, "查询“five”") {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestNormalizeCodingDecisionDoesNotTreatBareCodeIndexMissAsBoundedScan(t *testing.T) {
	searches := codingSearchEvidence{}
	searches.Record(
		"search_code_symbols",
		`{"query":"NormalizeContentType"}`,
		`{"index_available":true,"query":"NormalizeContentType","results":[]}`,
	)
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestNormalizeCodingDecisionCountsDuplicateSearchReceiptsAgainstDisplayBound(t *testing.T) {
	searches := codingSearchEvidence{}
	for range 17 {
		searches.Record(
			"search_workspace",
			`{"query":"NormalizeContentType"}`,
			`{"results":[],"truncated":false,"files_scanned":40,"directories_scanned":8}`,
		)
	}
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestNormalizeCodingDecisionDoesNotAcceptNullSearchMetadata(t *testing.T) {
	searches := codingSearchEvidence{}
	searches.Record(
		"search_workspace",
		`{"query":"NormalizeContentType"}`,
		`{"results":[],"truncated":null,"files_scanned":40,"directories_scanned":8}`,
	)
	decision := normalizeCodingDecisionWithSearchEvidence(agentcontext.Bundle{
		Event: domain.NormalizedEvent{Content: "请检查生产源码是否存在 NormalizeContentType"},
	}, domain.Decision{
		Kind:           domain.DecisionReply,
		EvidenceStatus: domain.EvidenceInsufficient,
		ReplyText:      "没有找到 NormalizeContentType。",
	}, searches)
	if decision.ReplyText != canonicalInsufficientCodingReply {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

package locale

import (
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestResolveLanguageHonorsConfiguredPreference(t *testing.T) {
	got := Resolve(LanguageChinese, LanguageEnglish, "Please answer in English")
	if got != LanguageChinese {
		t.Fatalf("language=%q", got)
	}
}

func TestResolveLanguageInfersConversationThenFallsBack(t *testing.T) {
	if got := Resolve(LanguageAuto, LanguageEnglish, "请检查这个接口为什么没有返回结果"); got != LanguageChinese {
		t.Fatalf("Chinese conversation language=%q", got)
	}
	if got := Resolve(LanguageAuto, LanguageChinese, "Please check why this API returns no result"); got != LanguageEnglish {
		t.Fatalf("English conversation language=%q", got)
	}
	if got := Resolve(LanguageAuto, LanguageChinese, "HTTP 500"); got != LanguageChinese {
		t.Fatalf("fallback language=%q", got)
	}
}

func TestValidateReplyLanguageAllowsIdentifiersButRejectsEnglishParagraph(t *testing.T) {
	if err := ValidateProse("我检查了 agent/runtime/loop.go，当前返回 HTTP 500，原因仍需继续核对。", LanguageChinese); err != nil {
		t.Fatalf("Chinese reply with identifiers rejected: %v", err)
	}
	if err := ValidateProse(
		"This is a direct mention and the context selection is incomplete, so no useful evidence-backed response is possible.",
		LanguageChinese,
	); err == nil {
		t.Fatal("English paragraph must be rejected for Chinese output")
	}
}

func TestRenderDelegatedReplyNamesAssistantAndOwner(t *testing.T) {
	got, err := RenderDelegatedReply(
		LanguageChinese,
		"测试负责人",
		"我已检查公开代码，当前确认事件入口会先经过只读权限判断；尚未确认生产配置值。",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"🤖 智能助手：", "已检查", "已将处理结果通知测试负责人"} {
		if !strings.Contains(got, want) {
			t.Fatalf("reply missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "用户") {
		t.Fatalf("reply used generic user identity: %s", got)
	}
}

func TestRenderDelegatedReplyRequiresOwnerName(t *testing.T) {
	if _, err := RenderDelegatedReply(LanguageChinese, "", "已检查当前公开代码。"); err == nil {
		t.Fatal("missing owner name must fail")
	}
}

func TestDelegatedPresenterDoesNotClaimOwnerNoticeForResourceHandoff(t *testing.T) {
	presenter := DelegatedPresenter{
		OwnerOpenID: "ou_owner",
		OwnerName:   "测试负责人",
		Preferred:   LanguageChinese,
		Fallback:    LanguageChinese,
	}
	decision, err := presenter.Present(
		domain.NewWorkItem(domain.NormalizedEvent{
			SenderID: "ou_teammate",
			Content:  "@测试负责人 修复后改下状态",
			Mentions: []domain.Mention{{OpenID: "ou_owner", Name: "测试负责人"}},
		}),
		domain.Decision{
			Kind:      domain.DecisionReply,
			Relevance: domain.RelevanceDirectMention,
			WorkKind:  domain.WorkKindResourceHandoff,
			ReplyText: "当前缺少 Base 记录读取权限。",
			Language:  string(LanguageChinese),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(decision.ReplyText, "🤖 智能助手：") ||
		strings.Contains(decision.ReplyText, "通知测试负责人") {
		t.Fatalf("reply=%q", decision.ReplyText)
	}
}

func TestLocalizedReasonDistinguishesReactionReadFailure(t *testing.T) {
	got := LocalizedReason(LanguageChinese, "owner_reaction_read_failed")
	if got != "无法读取负责人确认表情" {
		t.Fatalf("reason=%q", got)
	}
}

func TestLocalizedReasonDoesNotCallRetryCeilingAContextGap(t *testing.T) {
	got := LocalizedReason(
		LanguageChinese,
		"delegated reply context did not converge before the retry ceiling: owner_reply_ambiguous",
	)
	if got != "在重试上限内未能确认这条委托回复" {
		t.Fatalf("reason=%q", got)
	}
	if strings.Contains(got, "上下文") {
		t.Fatalf("retry ceiling localized as a context gap: %q", got)
	}
	got = LocalizedReason(LanguageChinese, "task_rules_unavailable: missing")
	if got != "已启用的私人任务规则文件当前无法读取" {
		t.Fatalf("task-rules reason=%q", got)
	}
}

func TestLocalizedReasonPreservesTypedProviderFailure(t *testing.T) {
	for _, testCase := range []struct {
		reason string
		want   string
	}{
		{reason: "model provider rate_limit: synthetic", want: "持续限流"},
		{reason: "model provider overloaded: synthetic", want: "持续过载"},
		{reason: "model provider authentication: synthetic", want: "拒绝了当前配置的凭据"},
		{reason: "model provider quota_exhausted: synthetic", want: "额度已耗尽"},
	} {
		if got := LocalizedReason(LanguageChinese, testCase.reason); !strings.Contains(got, testCase.want) {
			t.Fatalf("reason=%q got=%q want substring %q", testCase.reason, got, testCase.want)
		}
	}
}

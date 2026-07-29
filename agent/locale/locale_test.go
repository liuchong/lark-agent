package locale

import (
	"strings"
	"testing"
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

package control

import (
	"regexp"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestLocalizedValidationReasonDoesNotPasteEnglishIntoChinese(t *testing.T) {
	got := localizedValidationReason(
		"only interrupted or terminal work can be acknowledged",
		false,
	)
	if got != "只有已中断或已终止的任务可以确认收口" {
		t.Fatalf("reason=%q", got)
	}
}

func TestEnglishHelpAndTaskCommandsContainNoChineseExplanation(t *testing.T) {
	chinese := regexp.MustCompile(`[\p{Han}]`)
	help := HelpText("en-US", "task")
	if chinese.MatchString(help) {
		t.Fatalf("english help contains Chinese: %q", help)
	}
	commands := strings.Join(ownerTaskCommands(domain.OwnerTaskSummary{
		WorkItem: domain.WorkItem{ID: 42},
		State:    domain.WorkInspectionState{Uncertain: true},
	}, true), "\n")
	if chinese.MatchString(commands) {
		t.Fatalf("english commands contain Chinese: %q", commands)
	}
}

func TestTaskHelpExplainsInvestigationFieldsAndRefreshCommand(t *testing.T) {
	help := HelpText("zh-CN", "task")
	for _, want := range []string{
		"调查主题",
		"调查状态",
		"上下文证据",
		"最近错误",
		"`/task <工作号>`",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help=%q missing=%q", help, want)
		}
	}
}

func TestSanitizeEventTextMapsLarkMentionPlaceholders(t *testing.T) {
	event := domain.NormalizedEvent{
		Content: "@_user_1 看看这个问题，顺便问 @_user_2",
		Mentions: []domain.Mention{{
			Key: "@_user_1", Name: "测试负责人",
		}},
	}
	got := sanitizeEventText(event, 160, false)
	if got != "@测试负责人 看看这个问题，顺便问 @某人" {
		t.Fatalf("text=%q", got)
	}
	if strings.Contains(got, "@_user_") {
		t.Fatalf("unmapped mention remained: %q", got)
	}
	got = sanitizeEventText(domain.NormalizedEvent{
		Content: "ask @_user_9 to check",
	}, 160, true)
	if got != "ask @someone to check" {
		t.Fatalf("english text=%q", got)
	}
	got = sanitizeEventText(domain.NormalizedEvent{
		Content: "@_user_10 请检查",
		Mentions: []domain.Mention{{
			Key: "@_user_1", Name: "测试负责人",
		}},
	}, 160, false)
	if got != "@某人 请检查" {
		t.Fatalf("prefix-collision text=%q", got)
	}
}

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

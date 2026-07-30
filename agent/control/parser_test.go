package control

import (
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestParseReadOnlyCommandsAndAliases(t *testing.T) {
	tests := []struct {
		input string
		want  domain.OwnerControlCommand
	}{
		{"/help", domain.OwnerControlCommand{Name: domain.OwnerControlHelp}},
		{"/帮助 task", domain.OwnerControlCommand{Name: domain.OwnerControlHelp, Topic: "task"}},
		{"/status", domain.OwnerControlCommand{Name: domain.OwnerControlStatus}},
		{"/任务 action 2", domain.OwnerControlCommand{Name: domain.OwnerControlTasks, View: domain.OwnerTaskViewAction, Page: 2}},
		{"/task 42", domain.OwnerControlCommand{Name: domain.OwnerControlTask, WorkItemID: 42}},
		{"/approvals 3", domain.OwnerControlCommand{Name: domain.OwnerControlApprovals, Page: 3}},
		{"/approval 17", domain.OwnerControlCommand{Name: domain.OwnerControlApproval, ActionID: 17}},
		{"/recent 5", domain.OwnerControlCommand{Name: domain.OwnerControlRecent, Count: 5}},
		{"/version", domain.OwnerControlCommand{Name: domain.OwnerControlVersion}},
		{"/ping", domain.OwnerControlCommand{Name: domain.OwnerControlPing}},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, matched, err := Parse(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !matched || got != test.want {
				t.Fatalf("matched=%v got=%+v want=%+v", matched, got, test.want)
			}
		})
	}
}

func TestParseMutationCommands(t *testing.T) {
	tests := []struct {
		input string
		want  domain.OwnerControlCommand
	}{
		{
			"/task retry 42",
			domain.OwnerControlCommand{Name: domain.OwnerControlTaskRetry, WorkItemID: 42},
		},
		{
			"/task resume 42 confirm",
			domain.OwnerControlCommand{Name: domain.OwnerControlTaskResume, WorkItemID: 42, Confirm: true},
		},
		{
			"/task cancel 42 已由后续讨论取代",
			domain.OwnerControlCommand{
				Name: domain.OwnerControlTaskCancel, WorkItemID: 42, Reason: "已由后续讨论取代",
			},
		},
		{
			"/task acknowledge 42 已人工检查",
			domain.OwnerControlCommand{
				Name: domain.OwnerControlTaskAcknowledge, WorkItemID: 42, Reason: "已人工检查",
			},
		},
		{
			"/task reconcile 42 not-completed 已确认没有发送",
			domain.OwnerControlCommand{
				Name:        domain.OwnerControlTaskReconcile,
				WorkItemID:  42,
				Disposition: domain.OwnerResolutionNotCompleted,
				Reason:      "已确认没有发送",
			},
		},
		{
			"/approval approve 17 confirm",
			domain.OwnerControlCommand{
				Name: domain.OwnerControlApprovalApprove, ActionID: 17, Confirm: true,
			},
		},
		{
			"/approval reject 17 内容不准确",
			domain.OwnerControlCommand{
				Name: domain.OwnerControlApprovalReject, ActionID: 17, Reason: "内容不准确",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, matched, err := Parse("@_user_1 " + test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !matched || got != test.want {
				t.Fatalf("matched=%v got=%+v want=%+v", matched, got, test.want)
			}
		})
	}
}

func TestParseUnknownSlashCommandMatchesWithoutEnteringModel(t *testing.T) {
	got, matched, err := Parse("/unknown")
	if !matched || err == nil {
		t.Fatalf("matched=%v command=%+v err=%v", matched, got, err)
	}
	if _, matched, err := Parse("帮我看看任务"); matched || err != nil {
		t.Fatalf("free-form message matched=%v err=%v", matched, err)
	}
}

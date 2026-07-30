package control

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
)

// Parse recognizes only explicit slash commands. matched remains true for an
// invalid slash command so it cannot fall through to the model.
func Parse(raw string) (domain.OwnerControlCommand, bool, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return domain.OwnerControlCommand{}, false, nil
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	args := fields[1:]
	switch name {
	case "help", "帮助":
		return domain.OwnerControlCommand{
			Name:  domain.OwnerControlHelp,
			Topic: firstArg(args),
		}, true, nil
	case "status", "状态":
		return noArgs(domain.OwnerControlStatus, args)
	case "doctor", "诊断":
		return noArgs(domain.OwnerControlDoctor, args)
	case "tasks", "任务":
		return parseTasks(args)
	case "task":
		return parseTask(args)
	case "approvals", "审批":
		return parsePaged(domain.OwnerControlApprovals, args)
	case "approval":
		return parseApproval(args)
	case "recent", "最近":
		return parseRecent(args)
	case "version", "版本":
		return noArgs(domain.OwnerControlVersion, args)
	case "ping":
		return noArgs(domain.OwnerControlPing, args)
	default:
		return domain.OwnerControlCommand{}, true, fmt.Errorf("unknown command /%s", name)
	}
}

func noArgs(
	name domain.OwnerControlName,
	args []string,
) (domain.OwnerControlCommand, bool, error) {
	if len(args) != 0 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf("/%s does not accept arguments", name)
	}
	return domain.OwnerControlCommand{Name: name}, true, nil
}

func parseTasks(args []string) (domain.OwnerControlCommand, bool, error) {
	command := domain.OwnerControlCommand{
		Name: domain.OwnerControlTasks,
		View: domain.OwnerTaskViewAction,
	}
	if len(args) > 0 {
		command.View = domain.OwnerTaskView(strings.ToLower(args[0]))
		switch command.View {
		case domain.OwnerTaskViewAction,
			domain.OwnerTaskViewRunning,
			domain.OwnerTaskViewRecent,
			domain.OwnerTaskViewAll:
		default:
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"task view must be action, running, recent, or all",
			)
		}
	}
	if len(args) > 1 {
		page, err := positiveInt(args[1], "page")
		if err != nil {
			return domain.OwnerControlCommand{}, true, err
		}
		command.Page = page
	}
	if len(args) > 2 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf("/tasks accepts at most view and page")
	}
	return command, true, nil
}

func parseTask(args []string) (domain.OwnerControlCommand, bool, error) {
	if len(args) == 0 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf("/task requires a work id or operation")
	}
	if id, err := positiveInt64(args[0], "work id"); err == nil {
		if len(args) != 1 {
			return domain.OwnerControlCommand{}, true, fmt.Errorf("/task <work-id> accepts one id")
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlTask, WorkItemID: id,
		}, true, nil
	}
	operation := strings.ToLower(args[0])
	if len(args) < 2 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf("/task %s requires a work id", operation)
	}
	id, err := positiveInt64(args[1], "work id")
	if err != nil {
		return domain.OwnerControlCommand{}, true, err
	}
	switch operation {
	case "retry":
		if len(args) != 2 {
			return domain.OwnerControlCommand{}, true, fmt.Errorf("/task retry accepts one work id")
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlTaskRetry, WorkItemID: id,
		}, true, nil
	case "resume":
		if len(args) > 3 || (len(args) == 3 && strings.ToLower(args[2]) != "confirm") {
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"/task resume accepts an optional confirm",
			)
		}
		return domain.OwnerControlCommand{
			Name:       domain.OwnerControlTaskResume,
			WorkItemID: id,
			Confirm:    len(args) == 3,
		}, true, nil
	case "cancel":
		reason := joinedTail(args, 2)
		if reason == "" {
			return domain.OwnerControlCommand{}, true, fmt.Errorf("/task cancel requires a reason")
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlTaskCancel, WorkItemID: id, Reason: reason,
		}, true, nil
	case "acknowledge":
		reason := joinedTail(args, 2)
		if reason == "" {
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"/task acknowledge requires a note",
			)
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlTaskAcknowledge, WorkItemID: id, Reason: reason,
		}, true, nil
	case "reconcile":
		if len(args) < 4 {
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"/task reconcile requires a result and reason",
			)
		}
		disposition := domain.OwnerResolutionDisposition(
			strings.ReplaceAll(strings.ToLower(args[2]), "-", "_"),
		)
		switch disposition {
		case domain.OwnerResolutionCompleted,
			domain.OwnerResolutionNotCompleted,
			domain.OwnerResolutionUnknown:
		default:
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"reconciliation result must be completed, not-completed, or unknown",
			)
		}
		return domain.OwnerControlCommand{
			Name:        domain.OwnerControlTaskReconcile,
			WorkItemID:  id,
			Disposition: disposition,
			Reason:      joinedTail(args, 3),
		}, true, nil
	default:
		return domain.OwnerControlCommand{}, true, fmt.Errorf("unknown /task operation %s", operation)
	}
}

func parseApproval(args []string) (domain.OwnerControlCommand, bool, error) {
	if len(args) == 0 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf(
			"/approval requires an action id or operation",
		)
	}
	if id, err := positiveInt64(args[0], "action id"); err == nil {
		if len(args) != 1 {
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"/approval <action-id> accepts one id",
			)
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlApproval, ActionID: id,
		}, true, nil
	}
	if len(args) < 2 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf(
			"/approval %s requires an action id",
			args[0],
		)
	}
	id, err := positiveInt64(args[1], "action id")
	if err != nil {
		return domain.OwnerControlCommand{}, true, err
	}
	switch strings.ToLower(args[0]) {
	case "approve":
		if len(args) != 3 || strings.ToLower(args[2]) != "confirm" {
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"/approval approve requires confirm",
			)
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlApprovalApprove, ActionID: id, Confirm: true,
		}, true, nil
	case "reject":
		reason := joinedTail(args, 2)
		if reason == "" {
			return domain.OwnerControlCommand{}, true, fmt.Errorf(
				"/approval reject requires a reason",
			)
		}
		return domain.OwnerControlCommand{
			Name: domain.OwnerControlApprovalReject, ActionID: id, Reason: reason,
		}, true, nil
	default:
		return domain.OwnerControlCommand{}, true, fmt.Errorf(
			"unknown /approval operation %s",
			args[0],
		)
	}
}

func parsePaged(
	name domain.OwnerControlName,
	args []string,
) (domain.OwnerControlCommand, bool, error) {
	if len(args) > 1 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf("/%s accepts one page", name)
	}
	command := domain.OwnerControlCommand{Name: name}
	if len(args) == 1 {
		page, err := positiveInt(args[0], "page")
		if err != nil {
			return domain.OwnerControlCommand{}, true, err
		}
		command.Page = page
	}
	return command, true, nil
}

func parseRecent(args []string) (domain.OwnerControlCommand, bool, error) {
	if len(args) > 1 {
		return domain.OwnerControlCommand{}, true, fmt.Errorf("/recent accepts one count")
	}
	command := domain.OwnerControlCommand{Name: domain.OwnerControlRecent}
	if len(args) == 1 {
		count, err := positiveInt(args[0], "count")
		if err != nil {
			return domain.OwnerControlCommand{}, true, err
		}
		if count > 20 {
			return domain.OwnerControlCommand{}, true, fmt.Errorf("count must be at most 20")
		}
		command.Count = count
	}
	return command, true, nil
}

func positiveInt(raw, name string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func positiveInt64(raw, name string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func joinedTail(args []string, start int) string {
	if len(args) <= start {
		return ""
	}
	return strings.TrimSpace(strings.Join(args[start:], " "))
}

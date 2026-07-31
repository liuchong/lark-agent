package control

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/memory"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

const defaultPageSize = 10

var absolutePathPattern = regexp.MustCompile(
	`(?:/Users|/private|/var|/tmp)/[^\s，。；！？,;!?]+|~/[^\s，。；！？,;!?]+`,
)

var larkMentionPlaceholderPattern = regexp.MustCompile(`@_user_\d+`)

type Store interface {
	QueueSummary() (domain.QueueSummary, error)
	CurrentSession() domain.OnlineSession
	ListOwnerTasks(context.Context, domain.OwnerTaskQuery) (domain.OwnerTaskPage, error)
	InspectWork(context.Context, domain.WorkInspectionQuery) (domain.WorkInspection, error)
	ListPendingOwnerApprovals(context.Context, int, int) (domain.OwnerApprovalPage, error)
	GetActionAttempt(int64) (domain.ActionAttempt, error)
	ListMemories(context.Context, string, bool, int) ([]memory.Record, error)
	ExecuteOwnerMutation(
		context.Context,
		string,
		domain.OwnerControlCommand,
	) (domain.OwnerMutationResult, error)
}

type Config struct {
	OwnerName string
	Language  string
	Version   string
}

type Handler struct {
	store Store
	cfg   Config
}

func New(store Store, cfg Config) *Handler {
	return &Handler{store: store, cfg: cfg}
}

func (h *Handler) Handle(
	ctx context.Context,
	item domain.WorkItem,
	command domain.OwnerControlCommand,
) (domain.Decision, error) {
	text, err := h.execute(ctx, item, command)
	if err != nil {
		if problem, ok := errs.ProblemOf(err); ok && problem.Category == errs.CategoryValidation {
			text = h.validationText(command, problem.Message)
		} else {
			return domain.Decision{}, err
		}
	}
	return domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 1,
		Risk:       domain.RiskLow,
		Reason:     "owner_control_" + string(command.Name),
		ReplyText:  text,
		Language:   h.language(),
	}, nil
}

func (h *Handler) execute(
	ctx context.Context,
	item domain.WorkItem,
	command domain.OwnerControlCommand,
) (string, error) {
	switch command.Name {
	case domain.OwnerControlHelp:
		return HelpText(h.language(), command.Topic), nil
	case domain.OwnerControlStatus:
		return h.statusText(ctx)
	case domain.OwnerControlDoctor:
		return h.doctorText(ctx)
	case domain.OwnerControlTasks:
		return h.tasksText(ctx, domain.OwnerTaskQuery{
			View: command.View, Page: command.Page, PageSize: defaultPageSize,
		})
	case domain.OwnerControlTask:
		return h.taskText(ctx, command.WorkItemID)
	case domain.OwnerControlApprovals:
		return h.approvalsText(ctx, command.Page)
	case domain.OwnerControlApproval:
		return h.approvalText(command.ActionID)
	case domain.OwnerControlRecent:
		count := command.Count
		if count <= 0 {
			count = defaultPageSize
		}
		return h.tasksText(ctx, domain.OwnerTaskQuery{
			View: domain.OwnerTaskViewRecent, Page: 1, PageSize: count,
		})
	case domain.OwnerControlMemoryList:
		return h.memoriesText(ctx, command.Page)
	case domain.OwnerControlVersion:
		version := strings.TrimSpace(h.cfg.Version)
		if version == "" {
			version = "development"
		}
		if h.english() {
			return "Intelligent Assistant version: " + version, nil
		}
		return "智能助手版本：" + version, nil
	case domain.OwnerControlPing:
		return "pong", nil
	default:
		result, err := h.store.ExecuteOwnerMutation(
			ctx,
			item.Event.MessageID,
			command,
		)
		if err != nil {
			return "", err
		}
		return h.mutationText(result), nil
	}
}

func (h *Handler) memoriesText(ctx context.Context, page int) (string, error) {
	if page <= 0 {
		page = 1
	}
	records, err := h.store.ListMemories(ctx, "global", false, page*defaultPageSize)
	if err != nil {
		return "", err
	}
	start := (page - 1) * defaultPageSize
	if start >= len(records) {
		if h.english() {
			return fmt.Sprintf("No memories on page %d. Add one with `/memory add <kind> <content>`.", page), nil
		}
		return fmt.Sprintf("记忆第 %d 页没有内容。可用 `/memory add <类型> <内容>` 添加。", page), nil
	}
	end := min(start+defaultPageSize, len(records))
	var out strings.Builder
	if h.english() {
		fmt.Fprintf(&out, "Memories, page %d:\n", page)
	} else {
		fmt.Fprintf(&out, "记忆，第 %d 页：\n", page)
	}
	for _, record := range records[start:end] {
		fmt.Fprintf(
			&out,
			"- #%s [%s/%s/%s] %s\n",
			record.ID,
			record.Status,
			record.Kind,
			record.Scope,
			sanitizeText(record.Text, 240, h.english()),
		)
		if record.Status == memory.StatusCandidate {
			fmt.Fprintf(&out, "  `/memory feedback %s confirm`\n", record.ID)
		}
		fmt.Fprintf(&out, "  `/memory delete %s confirm`\n", record.ID)
	}
	return strings.TrimSpace(out.String()), nil
}

func (h *Handler) statusText(ctx context.Context) (string, error) {
	action, err := h.store.ListOwnerTasks(ctx, domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewAction, Page: 1, PageSize: 1,
	})
	if err != nil {
		return "", err
	}
	running, err := h.store.ListOwnerTasks(ctx, domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewRunning, Page: 1, PageSize: 1,
	})
	if err != nil {
		return "", err
	}
	session := h.store.CurrentSession()
	if h.english() {
		return fmt.Sprintf(
			"Intelligent Assistant is running. Session: %s. Needs your action: %d. In progress or waiting automatically: %d.\nUse `/tasks` to inspect actionable work.",
			session.Status,
			action.Total,
			running.Total,
		), nil
	}
	return fmt.Sprintf(
		"智能助手正在运行。当前会话：%s；需要你处理：%d 条；正在执行或自动等待：%d 条。\n发送 `/tasks` 查看需要处理的任务。",
		sessionStatusText(session.Status),
		action.Total,
		running.Total,
	), nil
}

func (h *Handler) doctorText(ctx context.Context) (string, error) {
	summary, err := h.store.QueueSummary()
	if err != nil {
		return "", err
	}
	action, err := h.store.ListOwnerTasks(ctx, domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewAction, Page: 1, PageSize: 1,
	})
	if err != nil {
		return "", err
	}
	if h.english() {
		return fmt.Sprintf(
			"Database and owner-private control are available. Actionable work: %d. Stale processing items: %d.\nUse `/tasks` for work and `/status` for the current session.",
			action.Total,
			summary.StaleProcessing,
		), nil
	}
	return fmt.Sprintf(
		"数据库和私聊控制命令可用。需要处理 %d 条；疑似长时间处理中的任务 %d 条。\n发送 `/tasks` 查看任务，发送 `/status` 查看当前会话。",
		action.Total,
		summary.StaleProcessing,
	), nil
}

func (h *Handler) tasksText(ctx context.Context, query domain.OwnerTaskQuery) (string, error) {
	page, err := h.store.ListOwnerTasks(ctx, query)
	if err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		if h.english() {
			return "There is currently no work that needs your action. Use `/recent` to view recent history.", nil
		}
		return "当前没有需要你处理的任务。发送 `/recent` 可以查看最近记录。", nil
	}
	var out strings.Builder
	if h.english() {
		fmt.Fprintf(&out, "Tasks (%d total, page %d):\n", page.Total, page.Page)
	} else {
		fmt.Fprintf(&out, "任务（共 %d 条，第 %d 页）：\n", page.Total, page.Page)
	}
	for index, task := range page.Items {
		if index > 0 {
			out.WriteString("\n")
		}
		out.WriteString(h.taskSummaryText(task))
	}
	if page.Page*page.PageSize < page.Total {
		next := page.Page + 1
		if h.english() {
			fmt.Fprintf(&out, "\nNext page: `/tasks %s %d`", query.View, next)
		} else {
			fmt.Fprintf(&out, "\n下一页：`/tasks %s %d`", query.View, next)
		}
	}
	return out.String(), nil
}

func (h *Handler) taskSummaryText(task domain.OwnerTaskSummary) string {
	id := task.WorkItem.ID
	subject := sanitizeEventText(task.WorkItem.Event, 72, h.english())
	state := ownerTaskStateText(task, h.ownerName())
	next := ownerTaskCommands(task, h.english())
	if h.english() {
		text := fmt.Sprintf("#%d %s\nStatus: %s", id, subject, state.en)
		text += investigationTaskText(task.Investigation, true, false)
		if state.fact != "" {
			text += "\nLast fact: " + sanitizeText(state.fact, 120, true)
		}
		if len(next) > 0 {
			text += "\nNext: " + strings.Join(next, " or ")
		}
		return text
	}
	text := fmt.Sprintf("#%d %s\n状态：%s", id, subject, state.zh)
	text += investigationTaskText(task.Investigation, false, false)
	if state.fact != "" {
		text += "\n最新事实：" + sanitizeText(state.fact, 120, false)
	}
	if len(next) > 0 {
		text += "\n建议操作：" + strings.Join(next, " 或 ")
	}
	return text
}

func (h *Handler) taskText(ctx context.Context, workItemID int64) (string, error) {
	inspection, err := h.store.InspectWork(ctx, domain.WorkInspectionQuery{
		WorkItemID: workItemID,
	})
	if err != nil {
		return "", err
	}
	if inspection.WorkItem == nil {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"work item %d was not found",
			workItemID,
		)
	}
	task := domain.OwnerTaskSummary{
		WorkItem:           *inspection.WorkItem,
		State:              inspection.State,
		LatestRun:          inspection.LatestRun,
		LatestStep:         inspection.LatestStep,
		LatestAction:       inspection.LatestAction,
		LatestInterruption: inspection.LatestInterruption,
		Investigation:      inspection.Investigation,
	}
	state := ownerTaskStateText(task, h.ownerName())
	commands := ownerTaskCommands(task, h.english())
	if h.english() {
		text := fmt.Sprintf(
			"Task #%d\nSubject: %s\nStatus: %s",
			workItemID,
			sanitizeEventText(task.WorkItem.Event, 160, true),
			state.en,
		)
		text += investigationTaskText(task.Investigation, true, true)
		if state.fact != "" {
			text += "\nLatest durable fact: " + sanitizeText(state.fact, 240, true)
		}
		if len(commands) > 0 {
			text += "\nAvailable next commands:\n" + strings.Join(commands, "\n")
		} else {
			text += "\nNo manual action is currently available."
		}
		return text, nil
	}
	text := fmt.Sprintf(
		"任务 #%d\n内容：%s\n状态：%s",
		workItemID,
		sanitizeEventText(task.WorkItem.Event, 160, false),
		state.zh,
	)
	text += investigationTaskText(task.Investigation, false, true)
	if state.fact != "" {
		text += "\n最新可靠事实：" + sanitizeText(state.fact, 240, false)
	}
	if len(commands) > 0 {
		text += "\n可执行的下一步：\n" + strings.Join(commands, "\n")
	} else {
		text += "\n当前没有需要人工执行的操作。"
	}
	return text, nil
}

func (h *Handler) approvalsText(ctx context.Context, pageNumber int) (string, error) {
	page, err := h.store.ListPendingOwnerApprovals(ctx, pageNumber, defaultPageSize)
	if err != nil {
		return "", err
	}
	if len(page.Items) == 0 {
		if h.english() {
			return "There are no pending approvals.", nil
		}
		return "当前没有待审批动作。", nil
	}
	var out strings.Builder
	if h.english() {
		fmt.Fprintf(&out, "Pending approvals (%d total, page %d):", page.Total, page.Page)
	} else {
		fmt.Fprintf(&out, "待审批动作（共 %d 条，第 %d 页）：", page.Total, page.Page)
	}
	for _, action := range page.Items {
		fmt.Fprintf(
			&out,
			"\n#%d · task #%d · %s · `/approval %d`",
			action.ID,
			action.WorkItemID,
			sanitizeText(action.Kind, 40, h.english()),
			action.ID,
		)
	}
	return out.String(), nil
}

func (h *Handler) approvalText(actionID int64) (string, error) {
	action, err := h.store.GetActionAttempt(actionID)
	if err != nil {
		return "", err
	}
	detail := safeActionDetail(action.RequestJSON, h.english())
	if h.english() {
		text := fmt.Sprintf(
			"Approval #%d\nTask: #%d\nType: %s\nStatus: %s",
			action.ID,
			action.WorkItemID,
			sanitizeText(action.Kind, 40, true),
			action.Status,
		)
		if detail != "" {
			text += "\nProposed content: " + detail
		}
		if action.Status == domain.ActionAwaitingApproval {
			text += fmt.Sprintf(
				"\nApprove: `/approval approve %d confirm`\nReject: `/approval reject %d <reason>`",
				action.ID,
				action.ID,
			)
		}
		return text, nil
	}
	text := fmt.Sprintf(
		"审批 #%d\n任务：#%d\n类型：%s\n状态：%s",
		action.ID,
		action.WorkItemID,
		sanitizeText(action.Kind, 40, false),
		actionStatusText(action.Status),
	)
	if detail != "" {
		text += "\n拟执行内容：" + detail
	}
	if action.Status == domain.ActionAwaitingApproval {
		text += fmt.Sprintf(
			"\n批准：`/approval approve %d confirm`\n拒绝：`/approval reject %d <原因>`",
			action.ID,
			action.ID,
		)
	}
	return text, nil
}

func (h *Handler) mutationText(result domain.OwnerMutationResult) string {
	replay := ""
	if result.Replayed {
		if h.english() {
			replay = " This command was already completed; no mutation was repeated."
		} else {
			replay = " 这条命令此前已经完成，本次没有重复修改。"
		}
	}
	if h.english() {
		switch result.Name {
		case domain.OwnerControlTaskRetry:
			return fmt.Sprintf("Task #%d is ready to retry.%s", result.WorkItemID, replay)
		case domain.OwnerControlTaskResume:
			return fmt.Sprintf("Task #%d was resumed.%s", result.WorkItemID, replay)
		case domain.OwnerControlTaskCancel:
			return fmt.Sprintf("Task #%d was cancelled. Reason: %s.%s", result.WorkItemID, result.Reason, replay)
		case domain.OwnerControlTaskAcknowledge:
			return fmt.Sprintf("Task #%d was acknowledged and closed.%s", result.WorkItemID, replay)
		case domain.OwnerControlTaskReconcile:
			return fmt.Sprintf(
				"Task #%d was reconciled as %s. No uncertain action was replayed.%s",
				result.WorkItemID,
				result.Disposition,
				replay,
			)
		case domain.OwnerControlApprovalApprove:
			return fmt.Sprintf("Approval #%d was approved.%s", result.ActionID, replay)
		case domain.OwnerControlApprovalReject:
			return fmt.Sprintf("Approval #%d was rejected. Reason: %s.%s", result.ActionID, result.Reason, replay)
		case domain.OwnerControlMemoryAdd:
			return fmt.Sprintf("Memory #%s was saved and confirmed.%s", result.MemoryID, replay)
		case domain.OwnerControlMemoryDelete:
			return fmt.Sprintf("Memory #%s was deleted.%s", result.MemoryID, replay)
		case domain.OwnerControlMemoryFeedback:
			return fmt.Sprintf("Feedback %s was recorded for memory #%s.%s", result.Reason, result.MemoryID, replay)
		}
	}
	switch result.Name {
	case domain.OwnerControlTaskRetry:
		return fmt.Sprintf("任务 #%d 已进入立即重试队列。%s", result.WorkItemID, replay)
	case domain.OwnerControlTaskResume:
		return fmt.Sprintf("任务 #%d 已恢复。%s", result.WorkItemID, replay)
	case domain.OwnerControlTaskCancel:
		return fmt.Sprintf("任务 #%d 已取消，原因：%s。%s", result.WorkItemID, result.Reason, replay)
	case domain.OwnerControlTaskAcknowledge:
		return fmt.Sprintf("任务 #%d 已确认并收口。%s", result.WorkItemID, replay)
	case domain.OwnerControlTaskReconcile:
		return fmt.Sprintf(
			"任务 #%d 已按“%s”完成核对；结果不确定的动作没有被重放。%s",
			result.WorkItemID,
			resolutionText(result.Disposition),
			replay,
		)
	case domain.OwnerControlApprovalApprove:
		return fmt.Sprintf("审批 #%d 已批准。%s", result.ActionID, replay)
	case domain.OwnerControlApprovalReject:
		return fmt.Sprintf("审批 #%d 已拒绝，原因：%s。%s", result.ActionID, result.Reason, replay)
	case domain.OwnerControlMemoryAdd:
		return fmt.Sprintf("记忆 #%s 已保存并确认。%s", result.MemoryID, replay)
	case domain.OwnerControlMemoryDelete:
		return fmt.Sprintf("记忆 #%s 已删除。%s", result.MemoryID, replay)
	case domain.OwnerControlMemoryFeedback:
		return fmt.Sprintf("已记录记忆 #%s 的“%s”反馈。%s", result.MemoryID, result.Reason, replay)
	default:
		return "命令已完成。" + replay
	}
}

func (h *Handler) validationText(command domain.OwnerControlCommand, reason string) string {
	reason = localizedValidationReason(reason, h.english())
	reason = sanitizeText(reason, 180, h.english())
	if h.english() {
		text := "The command was not applied: " + reason
		if command.WorkItemID > 0 {
			text += fmt.Sprintf("\nInspect the current state with `/task %d`.", command.WorkItemID)
		} else if command.ActionID > 0 {
			text += fmt.Sprintf("\nInspect the approval with `/approval %d`.", command.ActionID)
		} else {
			text += "\nUse `/help` for exact syntax."
		}
		return text
	}
	text := "命令没有执行：" + reason
	if command.WorkItemID > 0 {
		text += fmt.Sprintf("\n请先发送 `/task %d` 查看当前状态和可用操作。", command.WorkItemID)
	} else if command.ActionID > 0 {
		text += fmt.Sprintf("\n请先发送 `/approval %d` 查看审批状态。", command.ActionID)
	} else {
		text += "\n发送 `/help` 查看准确用法。"
	}
	return text
}

func localizedValidationReason(reason string, english bool) string {
	reason = strings.TrimSpace(reason)
	if english {
		return reason
	}
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "only interrupted or terminal work can be acknowledged"):
		return "只有已中断或已终止的任务可以确认收口"
	case strings.Contains(lower, "not found"):
		return "没有找到指定任务或动作"
	case strings.Contains(lower, "uncertain"):
		return "该任务仍有结果不确定的外部动作，必须先核对"
	case strings.Contains(lower, "not eligible"),
		strings.Contains(lower, "cannot be"),
		strings.Contains(lower, "must be"):
		return "当前任务状态不允许执行此操作"
	case strings.Contains(lower, "requires"):
		return "命令缺少必填参数"
	default:
		return "当前状态不允许执行这条命令"
	}
}

func HelpText(language, topic string) string {
	english := language == "en-US"
	topic = strings.ToLower(strings.TrimSpace(topic))
	if topic != "" {
		if detail := detailedHelp(english, topic); detail != "" {
			return detail
		}
	}
	lines := make([]string, 0, len(commandCatalog)+2)
	if english {
		lines = append(lines, "Owner-private commands:")
		for _, spec := range commandCatalog {
			lines = append(lines, fmt.Sprintf("- `%s`: %s.", spec.UsageEN, spec.PurposeEN))
		}
		lines = append(lines, "Natural-language equivalents are accepted only in the owner's assistant private chat when context identifies one exact command.")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "智能助手私聊命令：")
	for _, spec := range commandCatalog {
		lines = append(lines, fmt.Sprintf("- `%s`：%s。", spec.UsageZH, spec.PurposeZH))
	}
	lines = append(lines, "只有用户与智能助手私聊且上下文能唯一确定命令时，才接受自然语言等价表达。")
	return strings.Join(lines, "\n")
}

func detailedHelp(english bool, topic string) string {
	switch topic {
	case "task", "tasks", "任务":
		if english {
			return strings.Join([]string{
				"Task commands:",
				"`/tasks [action|running|recent|all] [page]`",
				"`/task <work-id>`",
				"`/task retry <work-id>`",
				"`/task resume <work-id> [confirm]`",
				"`/task cancel <work-id> <reason>`",
				"`/task acknowledge <work-id> <note>`",
				"`/task reconcile <work-id> completed|not-completed|unknown <reason>`",
				"Uncertain external actions must be reconciled and are never replayed automatically.",
			}, "\n")
		}
		return strings.Join([]string{
			"任务命令：",
			"`/tasks [action|running|recent|all] [页码]`",
			"`/task <工作号>`",
			"`/task retry <工作号>`",
			"`/task resume <工作号> [confirm]`",
			"`/task cancel <工作号> <原因>`",
			"`/task acknowledge <工作号> <说明>`",
			"`/task reconcile <工作号> completed|not-completed|unknown <核对说明>`",
			"`/tasks` 和 `/task <工作号>` 会显示调查主题、调查状态、上下文证据和最近错误。",
			"正在自动调查时可再次发送 `/task <工作号>` 刷新；中断、失败或结果不确定时按详情给出的精确命令处理。",
			"外部结果不确定时必须先核对，智能助手不会自动重放。",
		}, "\n")
	case "approval", "approvals", "审批":
		if english {
			return strings.Join([]string{
				"Approval commands:",
				"`/approvals [page]`",
				"`/approval <action-id>`",
				"`/approval approve <action-id> confirm`",
				"`/approval reject <action-id> <reason>`",
			}, "\n")
		}
		return strings.Join([]string{
			"审批命令：",
			"`/approvals [页码]`",
			"`/approval <动作号>`",
			"`/approval approve <动作号> confirm`",
			"`/approval reject <动作号> <原因>`",
		}, "\n")
	case "memory", "记忆":
		if english {
			return strings.Join([]string{
				"Memory commands:",
				"`/memory list [page]`",
				"`/memory add fact|preference|project|response_feedback <content>`",
				"`/memory delete <memory-id> confirm`",
				"`/memory feedback <memory-id> confirm|reject|helpful|unhelpful [note]`",
				"Only confirmed, non-deleted memories enter later model context.",
			}, "\n")
		}
		return strings.Join([]string{
			"记忆命令：",
			"`/memory list [页码]`",
			"`/memory add fact|preference|project|response_feedback <内容>`",
			"`/memory delete <记忆号> confirm`",
			"`/memory feedback <记忆号> confirm|reject|helpful|unhelpful [说明]`",
			"只有已确认且未删除的记忆会进入后续模型上下文。",
		}, "\n")
	default:
		return ""
	}
}

type taskStateText struct {
	zh   string
	en   string
	fact string
}

func ownerTaskStateText(task domain.OwnerTaskSummary, ownerName string) taskStateText {
	if task.State.Uncertain {
		fact := ""
		if task.LatestInterruption != nil {
			fact = task.LatestInterruption.Reason
		}
		return taskStateText{
			zh:   "外部动作结果不确定，禁止自动重放",
			en:   "external result is uncertain and must not be replayed",
			fact: fact,
		}
	}
	fact := ""
	if task.LatestRun != nil && task.LatestRun.LastError != "" {
		fact = task.LatestRun.LastError
	} else if task.LatestInterruption != nil && task.LatestInterruption.Reason != "" {
		fact = task.LatestInterruption.Reason
	} else if task.LatestAction != nil && task.LatestAction.Error != "" {
		fact = task.LatestAction.Error
	}
	switch task.WorkItem.Status {
	case domain.StatusAwaitingApproval:
		return taskStateText{
			zh: "等待" + ownerName + "审批", en: "waiting for owner approval", fact: fact,
		}
	case domain.StatusInterrupted:
		return taskStateText{zh: "已中断，等待恢复或收口", en: "interrupted; resume or close it", fact: fact}
	case domain.StatusDeadLetter:
		return taskStateText{zh: "已停止，需要检查原因", en: "stopped; inspect the reason", fact: fact}
	case domain.StatusRetryWait:
		return taskStateText{zh: "等待自动重试", en: "waiting for automatic retry", fact: fact}
	case domain.StatusProcessing, domain.StatusExecuting:
		return taskStateText{zh: "正在执行", en: "running", fact: fact}
	case domain.StatusWaitingUser:
		return taskStateText{
			zh:   "等待观察" + ownerName + "是否已经回复",
			en:   "waiting to see whether the owner replied",
			fact: fact,
		}
	case domain.StatusReceived, domain.StatusRouted, domain.StatusReady:
		return taskStateText{zh: "已排队", en: "queued", fact: fact}
	case domain.StatusCompleted:
		return taskStateText{zh: "已完成", en: "completed", fact: fact}
	case domain.StatusCancelled:
		return taskStateText{zh: "已取消", en: "cancelled", fact: fact}
	case domain.StatusIgnored:
		return taskStateText{zh: "无需处理", en: "no action needed", fact: fact}
	default:
		return taskStateText{zh: string(task.WorkItem.Status), en: string(task.WorkItem.Status), fact: fact}
	}
}

func ownerTaskCommands(task domain.OwnerTaskSummary, english bool) []string {
	id := task.WorkItem.ID
	commands := investigationRefreshCommand(task.Investigation, id)
	if task.State.Uncertain {
		if english {
			return append(commands, fmt.Sprintf(
				"`/task reconcile %d completed|not-completed|unknown <verification-note>`",
				id,
			))
		}
		return append(commands, fmt.Sprintf(
			"`/task reconcile %d completed|not-completed|unknown <核对说明>`",
			id,
		))
	}
	if task.LatestAction != nil &&
		task.LatestAction.Status == domain.ActionAwaitingApproval {
		return append(commands, fmt.Sprintf("`/approval %d`", task.LatestAction.ID))
	}
	switch task.WorkItem.Status {
	case domain.StatusRetryWait:
		return append(commands, fmt.Sprintf("`/task retry %d`", id))
	case domain.StatusInterrupted:
		if english {
			return append(commands,
				fmt.Sprintf("`/task resume %d`", id),
				fmt.Sprintf("`/task cancel %d <reason>`", id),
			)
		}
		return append(commands,
			fmt.Sprintf("`/task resume %d`", id),
			fmt.Sprintf("`/task cancel %d <原因>`", id),
		)
	case domain.StatusDeadLetter:
		if english {
			return append(commands,
				fmt.Sprintf("`/task resume %d confirm`", id),
				fmt.Sprintf("`/task acknowledge %d <note>`", id),
			)
		}
		return append(commands,
			fmt.Sprintf("`/task resume %d confirm`", id),
			fmt.Sprintf("`/task acknowledge %d <说明>`", id),
		)
	default:
		return commands
	}
}

func investigationRefreshCommand(
	investigation *domain.DelegatedInvestigation,
	workItemID int64,
) []string {
	if investigation == nil {
		return nil
	}
	switch investigation.Status {
	case domain.InvestigationPendingProgress,
		domain.InvestigationInvestigating,
		domain.InvestigationFinalizing:
		return []string{fmt.Sprintf("`/task %d`", workItemID)}
	default:
		return nil
	}
}

func investigationTaskText(
	investigation *domain.DelegatedInvestigation,
	english bool,
	detail bool,
) string {
	if investigation == nil {
		return ""
	}
	statusZH, statusEN := investigationStatusText(investigation.Status)
	evidenceZH, evidenceEN := "尚未固定", "not fixed"
	if strings.TrimSpace(investigation.ContextDigest) != "" {
		digest := sanitizeText(investigation.ContextDigest, 80, english)
		evidenceZH = "已固定（" + digest + "）"
		evidenceEN = "fixed (" + digest + ")"
	}
	if english {
		text := "\nInvestigation subject: " +
			sanitizeText(investigation.TaskSummary, 160, true) +
			"\nInvestigation status: " + statusEN +
			"\nContext evidence: " + evidenceEN
		if detail {
			lastError := sanitizeText(investigation.LastError, 240, true)
			if lastError == "" {
				lastError = "none"
			}
			text += "\nInvestigation type: " + string(investigation.TaskClass) +
				"\nInvestigation last error: " + lastError
		}
		return text
	}
	text := "\n调查主题：" +
		sanitizeText(investigation.TaskSummary, 160, false) +
		"\n调查状态：" + statusZH +
		"\n上下文证据：" + evidenceZH
	if detail {
		lastError := sanitizeText(investigation.LastError, 240, false)
		if lastError == "" {
			lastError = "无"
		}
		text += "\n调查类型：" + investigationClassText(investigation.TaskClass) +
			"\n调查最近错误：" + lastError
	}
	return text
}

func investigationStatusText(status domain.DelegatedInvestigationStatus) (string, string) {
	switch status {
	case domain.InvestigationPendingProgress:
		return "准备发送进度", "preparing progress"
	case domain.InvestigationInvestigating:
		return "正在调查", "investigating"
	case domain.InvestigationFinalizing:
		return "正在发送最终结果", "sending final result"
	case domain.InvestigationCompleted:
		return "已完成", "completed"
	case domain.InvestigationBlocked:
		return "已阻塞", "blocked"
	default:
		return string(status), string(status)
	}
}

func investigationClassText(class domain.TaskClass) string {
	switch class {
	case domain.TaskClassSimple:
		return "简单问答"
	case domain.TaskClassInvestigation:
		return "业务调查"
	case domain.TaskClassCoding:
		return "代码调查"
	default:
		return string(class)
	}
}

func sanitizeText(raw string, limit int, english bool) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if english {
		text = absolutePathPattern.ReplaceAllString(text, "[path hidden]")
		text = larkMentionPlaceholderPattern.ReplaceAllString(text, "@someone")
	} else {
		text = absolutePathPattern.ReplaceAllString(text, "[路径已隐藏]")
		text = larkMentionPlaceholderPattern.ReplaceAllString(text, "@某人")
	}
	if text == "" {
		if english {
			text = "(no displayable summary)"
		} else {
			text = "（无可显示摘要）"
		}
	}
	runes := []rune(text)
	if limit > 0 && len(runes) > limit {
		text = string(runes[:limit]) + "…"
	}
	return text
}

func sanitizeEventText(event domain.NormalizedEvent, limit int, english bool) string {
	fallback := "某人"
	if english {
		fallback = "someone"
	}
	names := make(map[string]string, len(event.Mentions))
	for _, mention := range event.Mentions {
		key := strings.TrimSpace(mention.Key)
		if key == "" {
			continue
		}
		name := strings.Join(strings.Fields(strings.TrimSpace(mention.Name)), " ")
		if name == "" {
			name = fallback
		}
		names[key] = name
	}
	text := larkMentionPlaceholderPattern.ReplaceAllStringFunc(
		event.Content,
		func(key string) string {
			if name, ok := names[key]; ok {
				return "@" + name
			}
			return "@" + fallback
		},
	)
	return sanitizeText(text, limit, english)
}

func safeActionDetail(raw string, english bool) string {
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return ""
	}
	for _, key := range []string{"reply_text", "command", "owner_action"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return sanitizeText(value, 240, english)
		}
	}
	return ""
}

func (h *Handler) language() string {
	if h.english() {
		return "en-US"
	}
	return "zh-CN"
}

func (h *Handler) english() bool {
	return h.cfg.Language == "en-US"
}

func (h *Handler) ownerName() string {
	name := strings.TrimSpace(h.cfg.OwnerName)
	if name != "" {
		return name
	}
	if h.english() {
		return "Owner"
	}
	return "负责人"
}

func sessionStatusText(status domain.OnlineSessionStatus) string {
	switch status {
	case domain.OnlineSessionReady:
		return "已就绪"
	case domain.OnlineSessionStarting:
		return "正在启动"
	case domain.OnlineSessionStopped:
		return "已停止"
	default:
		return "未知"
	}
}

func actionStatusText(status domain.ActionStatus) string {
	switch status {
	case domain.ActionAwaitingApproval:
		return "等待审批"
	case domain.ActionReady:
		return "已批准，等待执行"
	case domain.ActionCompleted:
		return "已完成"
	case domain.ActionCancelled:
		return "已取消"
	case domain.ActionExecuting:
		return "正在执行"
	case domain.ActionBlocked:
		return "已阻止"
	default:
		return string(status)
	}
}

func resolutionText(disposition domain.OwnerResolutionDisposition) string {
	switch disposition {
	case domain.OwnerResolutionCompleted:
		return "已确认完成"
	case domain.OwnerResolutionNotCompleted:
		return "已确认未完成"
	case domain.OwnerResolutionUnknown:
		return "仍无法确认"
	case domain.OwnerResolutionAcknowledged:
		return "已知晓并收口"
	default:
		return string(disposition)
	}
}

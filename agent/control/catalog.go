package control

import (
	"fmt"
	"slices"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
)

// CommandSpec is the shared contract for parsing, help, and semantic control.
type CommandSpec struct {
	Name      domain.OwnerControlName
	Aliases   []string
	UsageZH   string
	UsageEN   string
	PurposeZH string
	PurposeEN string
	Mutation  bool
	Candidate string
}

var commandCatalog = []CommandSpec{
	{Name: domain.OwnerControlHelp, Aliases: []string{"help", "帮助"}, UsageZH: "/help [主题]", UsageEN: "/help [topic]", PurposeZH: "查看命令帮助", PurposeEN: "show command help"},
	{Name: domain.OwnerControlStatus, Aliases: []string{"status", "状态"}, UsageZH: "/status", UsageEN: "/status", PurposeZH: "查看智能助手运行状态", PurposeEN: "show assistant status"},
	{Name: domain.OwnerControlDoctor, Aliases: []string{"doctor", "诊断"}, UsageZH: "/doctor", UsageEN: "/doctor", PurposeZH: "检查配置和运行依赖", PurposeEN: "check configuration and runtime dependencies"},
	{Name: domain.OwnerControlTasks, Aliases: []string{"tasks", "任务"}, UsageZH: "/tasks [action|running|recent|all] [页码]", UsageEN: "/tasks [action|running|recent|all] [page]", PurposeZH: "分页查看任务", PurposeEN: "list tasks"},
	{Name: domain.OwnerControlTask, Aliases: []string{"task"}, UsageZH: "/task <工作号>", UsageEN: "/task <work-id>", PurposeZH: "查看一项任务详情", PurposeEN: "show one task"},
	{Name: domain.OwnerControlTaskRetry, UsageZH: "/task retry <工作号>", UsageEN: "/task retry <work-id>", PurposeZH: "重试可安全重算的任务", PurposeEN: "retry safely recomputable work", Mutation: true, Candidate: "work"},
	{Name: domain.OwnerControlTaskResume, UsageZH: "/task resume <工作号> [confirm]", UsageEN: "/task resume <work-id> [confirm]", PurposeZH: "恢复暂停的任务", PurposeEN: "resume paused work", Mutation: true, Candidate: "work"},
	{Name: domain.OwnerControlTaskCancel, UsageZH: "/task cancel <工作号> <原因>", UsageEN: "/task cancel <work-id> <reason>", PurposeZH: "取消未完成任务", PurposeEN: "cancel unfinished work", Mutation: true, Candidate: "work"},
	{Name: domain.OwnerControlTaskAcknowledge, UsageZH: "/task acknowledge <工作号> <说明>", UsageEN: "/task acknowledge <work-id> <note>", PurposeZH: "确认已人工处理的终态任务", PurposeEN: "acknowledge manually handled terminal work", Mutation: true, Candidate: "work"},
	{Name: domain.OwnerControlTaskReconcile, UsageZH: "/task reconcile <工作号> completed|not-completed|unknown <核对说明>", UsageEN: "/task reconcile <work-id> completed|not-completed|unknown <reason>", PurposeZH: "核对结果不确定的外部动作", PurposeEN: "reconcile an uncertain external action", Mutation: true, Candidate: "work"},
	{Name: domain.OwnerControlApprovals, Aliases: []string{"approvals", "审批"}, UsageZH: "/approvals [页码]", UsageEN: "/approvals [page]", PurposeZH: "查看待审批动作", PurposeEN: "list pending approvals"},
	{Name: domain.OwnerControlApproval, Aliases: []string{"approval"}, UsageZH: "/approval <动作号>", UsageEN: "/approval <action-id>", PurposeZH: "查看一项审批详情", PurposeEN: "show one approval"},
	{Name: domain.OwnerControlApprovalApprove, UsageZH: "/approval approve <动作号> confirm", UsageEN: "/approval approve <action-id> confirm", PurposeZH: "批准并执行一项准确草稿", PurposeEN: "approve and execute one exact draft", Mutation: true, Candidate: "approval"},
	{Name: domain.OwnerControlApprovalReject, UsageZH: "/approval reject <动作号> <原因>", UsageEN: "/approval reject <action-id> <reason>", PurposeZH: "拒绝一项待审批动作", PurposeEN: "reject one pending approval", Mutation: true, Candidate: "approval"},
	{Name: domain.OwnerControlRecent, Aliases: []string{"recent", "最近"}, UsageZH: "/recent [数量]", UsageEN: "/recent [count]", PurposeZH: "查看最近处理统计", PurposeEN: "show recent processing metrics"},
	{Name: domain.OwnerControlMemoryList, Aliases: []string{"memory", "记忆"}, UsageZH: "/memory list [页码]", UsageEN: "/memory list [page]", PurposeZH: "查看已保存和待确认的记忆", PurposeEN: "list saved and candidate memories"},
	{Name: domain.OwnerControlMemoryAdd, UsageZH: "/memory add fact|preference|project|response_feedback <内容>", UsageEN: "/memory add fact|preference|project|response_feedback <content>", PurposeZH: "保存一条已确认记忆", PurposeEN: "save one confirmed memory", Mutation: true},
	{Name: domain.OwnerControlMemoryDelete, UsageZH: "/memory delete <记忆号> confirm", UsageEN: "/memory delete <memory-id> confirm", PurposeZH: "删除一条记忆", PurposeEN: "delete one memory", Mutation: true, Candidate: "memory"},
	{Name: domain.OwnerControlMemoryFeedback, UsageZH: "/memory feedback <记忆号> confirm|reject|helpful|unhelpful [说明]", UsageEN: "/memory feedback <memory-id> confirm|reject|helpful|unhelpful [note]", PurposeZH: "确认、拒绝或评价一条记忆", PurposeEN: "confirm, reject, or rate one memory", Mutation: true, Candidate: "memory"},
	{Name: domain.OwnerControlVersion, Aliases: []string{"version", "版本"}, UsageZH: "/version", UsageEN: "/version", PurposeZH: "查看当前版本", PurposeEN: "show current version"},
	{Name: domain.OwnerControlPing, Aliases: []string{"ping"}, UsageZH: "/ping", UsageEN: "/ping", PurposeZH: "检查助手是否在线", PurposeEN: "check whether the assistant is online"},
}

// Catalog returns a defensive copy of the canonical command catalog.
func Catalog() []CommandSpec {
	out := make([]CommandSpec, len(commandCatalog))
	copy(out, commandCatalog)
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
	}
	return out
}

func commandForAlias(alias string) (domain.OwnerControlName, bool) {
	for _, spec := range commandCatalog {
		if slices.Contains(spec.Aliases, alias) {
			return spec.Name, true
		}
	}
	return "", false
}

func commandSpec(name domain.OwnerControlName) (CommandSpec, bool) {
	for _, spec := range commandCatalog {
		if spec.Name == name {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

// SemanticCatalog renders the same catalog used by parsing and help.
func SemanticCatalog(language string) string {
	english := language == "en-US"
	lines := []string{
		"Only classify a message as a command when the owner clearly intends to operate this control plane.",
		"Questions that merely contain words such as confirm, status, task, or approval are not commands.",
		"Use memory add as a command only when the owner explicitly asks to remember or save the content; an ordinary correction may become an unconfirmed memory candidate instead.",
	}
	for _, spec := range commandCatalog {
		usage, purpose := spec.UsageZH, spec.PurposeZH
		if english {
			usage, purpose = spec.UsageEN, spec.PurposeEN
		}
		risk := "read-only"
		if spec.Mutation {
			risk = "mutation"
		}
		lines = append(lines, fmt.Sprintf("- `%s`: %s (%s)", usage, purpose, risk))
	}
	return strings.Join(lines, "\n")
}

// SemanticCandidates bounds IDs a semantic resolver is allowed to select.
type SemanticCandidates struct {
	WorkIDs     []int64
	ApprovalIDs []int64
	MemoryIDs   []string
}

// ValidateSemanticCommand applies confidence and candidate gates to one exact
// slash-form command emitted by the semantic resolver.
func ValidateSemanticCommand(
	raw string,
	confidence float64,
	candidates SemanticCandidates,
) (domain.OwnerControlCommand, error) {
	command, matched, err := Parse(raw)
	if err != nil {
		return domain.OwnerControlCommand{}, err
	}
	if !matched {
		return domain.OwnerControlCommand{}, fmt.Errorf("semantic command must use canonical slash syntax")
	}
	spec, ok := commandSpec(command.Name)
	if !ok {
		return domain.OwnerControlCommand{}, fmt.Errorf("command %q is not in the catalog", command.Name)
	}
	minConfidence := 0.85
	if spec.Mutation {
		minConfidence = 0.95
	}
	if confidence < minConfidence {
		return domain.OwnerControlCommand{}, fmt.Errorf(
			"semantic command confidence %.2f is below %.2f",
			confidence,
			minConfidence,
		)
	}
	switch spec.Candidate {
	case "work":
		if command.WorkItemID == 0 || !slices.Contains(candidates.WorkIDs, command.WorkItemID) {
			return domain.OwnerControlCommand{}, fmt.Errorf(
				"work item %d is not an eligible semantic candidate",
				command.WorkItemID,
			)
		}
	case "approval":
		if command.ActionID == 0 || !slices.Contains(candidates.ApprovalIDs, command.ActionID) {
			return domain.OwnerControlCommand{}, fmt.Errorf(
				"approval %d is not an eligible semantic candidate",
				command.ActionID,
			)
		}
	case "memory":
		if command.MemoryID == "" || !slices.Contains(candidates.MemoryIDs, command.MemoryID) {
			return domain.OwnerControlCommand{}, fmt.Errorf(
				"memory %q is not an eligible semantic candidate",
				command.MemoryID,
			)
		}
	}
	return command, nil
}

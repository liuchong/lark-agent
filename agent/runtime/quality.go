package runtime

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
	"github.com/liuchong/lark-agent/internal/apperr"
)

type responseEvidence struct {
	SuccessfulReads int
	digests         map[string]struct{}
}

func (e *responseEvidence) RecordRelevantRead(digest string, nonEmpty bool) {
	if e == nil || !nonEmpty {
		return
	}
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return
	}
	if e.digests == nil {
		e.digests = make(map[string]struct{})
	}
	if _, exists := e.digests[digest]; exists {
		return
	}
	e.digests[digest] = struct{}{}
	e.SuccessfulReads++
}

func evidenceDigest(toolName, content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return fmt.Sprintf("%s:sha256:%x", strings.TrimSpace(toolName), sum[:])
}

func guardedRequestDecision(bundle agentcontext.Bundle) (domain.Decision, bool) {
	content := strings.TrimSpace(bundle.Event.Content)
	if content == "" || !isGuardedRequest(content, bundle.Environment.WorkspaceRealRoot) {
		return domain.Decision{}, false
	}
	relevance := domain.RelevanceAssistantRequest
	switch {
	case bundle.Event.MentionsUser(bundle.User.OpenID):
		relevance = domain.RelevanceDirectMention
	case bundle.User.OpenID != "" && bundle.Event.SenderID == bundle.User.OpenID:
		relevance = domain.RelevanceOwnerRequest
	}
	language := resolvedBundleLanguage(bundle)
	replyText := "这个请求涉及工作环境或工作目录之外的信息，我不能处理。请改成具体业务问题，并把范围限定在已配置的工作目录内。"
	reason := "request asks for out-of-workspace or descriptive environment reconnaissance"
	if language == agentlocale.LanguageEnglish {
		replyText = "I cannot handle requests for work-environment details or paths outside the configured workspace. Please ask a concrete business question scoped to the configured workspace."
		reason = "request is outside the configured business workspace boundary"
	}
	return domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  relevance,
		WorkKind:   bundle.WorkKind,
		Priority:   bundle.Priority,
		Confidence: 1,
		Risk:       domain.RiskLow,
		Reason:     reason,
		ReplyText:  replyText,
		Language:   string(language),
	}, true
}

func isGuardedRequest(content, workspaceRoot string) bool {
	lower := strings.ToLower(content)
	if requestsOutsideWorkspace(content, workspaceRoot) {
		return true
	}
	if !containsAny(lower, reconnaissanceVerbs...) {
		return false
	}
	if containsAny(lower, reconnaissanceTargets...) {
		return true
	}
	return false
}

var reconnaissanceVerbs = []string{
	"列出", "枚举", "打印", "显示", "发出来", "告诉我", "看看", "查看", "读取", "打开",
	"分析", "检查", "研究", "扫描", "搜索", "查找", "描述", "探查", "修改", "删除", "写入",
	"list", "enumerate", "print", "show", "describe", "read", "open",
	"analyze", "inspect", "scan", "search", "find", "modify", "delete", "write",
}

var reconnaissanceTargets = []string{
	"环境变量", "用户名", "用户目录", "home 目录", "home目录", "主机信息", "机器信息",
	"系统信息", "进程列表", "网络配置", "ip 地址", "ip地址", "已安装的工具",
	"installed tools", "process list", "network config", "environment variables",
	"keychain", "/.ssh", ".ssh/", "当前凭据", "当前密钥", "当前令牌",
	"whoami", "uname", "printenv",
}

func requestsOutsideWorkspace(content, workspaceRoot string) bool {
	lower := strings.ToLower(content)
	if strings.Contains(content, "../") ||
		strings.Contains(content, "~/") ||
		strings.Contains(lower, "$home/") ||
		strings.Contains(lower, "${home}/") {
		return true
	}
	for _, field := range strings.Fields(content) {
		candidate := strings.Trim(field, "，。；：、,;:'\"`()[]{}<>")
		if !looksLikeLocalAbsolutePath(candidate) {
			continue
		}
		if workspaceRoot == "" {
			return true
		}
		relative, err := filepath.Rel(workspaceRoot, filepath.Clean(candidate))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func looksLikeLocalAbsolutePath(value string) bool {
	if !filepath.IsAbs(value) {
		return false
	}
	for _, prefix := range []string{
		"/Users/", "/home/", "/root/", "/etc/", "/var/", "/private/", "/tmp/", "/opt/",
		"/usr/", "/Library/", "/Applications/", "/System/", "/bin/", "/sbin/", "/dev/", "/Volumes/",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func validateResponseQuality(bundle agentcontext.Bundle, decision domain.Decision, evidence responseEvidence) error {
	if !isDelegatedInvocation(bundle) {
		return nil
	}
	if decision.Kind == domain.DecisionRequestApproval {
		return nil
	}
	if decision.Kind == domain.DecisionReply && containsFutureCommitment(decision.ReplyText) {
		return qualityError("delegated automatic reply contains an unapproved future commitment")
	}
	if decision.Kind == domain.DecisionReply &&
		decision.ReplyOutcome == domain.ReplyOutcomeClarification {
		if len(decision.Progress.Unknowns) == 0 || strings.TrimSpace(decision.Progress.NextStep) == "" {
			return qualityError("clarification reply requires structured progress with exact unknowns and next_step")
		}
		if isAcknowledgementOnly(decision.ReplyText) {
			return qualityError("clarification reply is acknowledgement-only; ask for the exact missing input")
		}
		requestText := strings.TrimSpace(bundle.TaskSummary)
		if requestText == "" {
			requestText = bundle.Event.Content
		}
		if highRestatementSimilarity(requestText, decision.ReplyText) {
			return qualityError("clarification reply mostly restates the request without asking for exact missing input")
		}
		return nil
	}
	if !bundleRequiresRelevantWork(bundle) {
		return nil
	}
	if decision.Kind != domain.DecisionReply && decision.Kind != domain.DecisionNotify && decision.Kind != domain.DecisionRecord {
		return nil
	}
	if evidence.SuccessfulReads == 0 {
		return qualityError("delegated work response requires at least one successful relevant read before a terminal decision")
	}
	if decision.Kind != domain.DecisionReply {
		return nil
	}
	structuredProgress := hasUsefulStructuredProgress(decision.Progress)
	if decision.ReplyOutcome == domain.ReplyOutcomePartial && !structuredProgress {
		return qualityError("partial reply requires structured progress with completed checks, a finding or unknown, and next_step")
	}
	if isAcknowledgementOnly(decision.ReplyText) && !structuredProgress {
		return qualityError("delegated reply is acknowledgement-only; state completed work and an initial finding or explicit unknown")
	}
	requestText := strings.TrimSpace(bundle.TaskSummary)
	if requestText == "" {
		requestText = bundle.Event.Content
	}
	if highRestatementSimilarity(requestText, decision.ReplyText) &&
		!structuredProgress &&
		!containsCompletedWorkSignal(decision.ReplyText) {
		return qualityError("delegated reply mostly restates the request without completed relevant work")
	}
	if structuredProgress {
		return nil
	}
	if !containsCompletedWorkSignal(decision.ReplyText) {
		return qualityError("delegated reply must state completed relevant work and an initial finding or explicit unknown")
	}
	return nil
}

func hasUsefulStructuredProgress(progress domain.DecisionProgress) bool {
	return len(progress.CompletedChecks) > 0 &&
		(strings.TrimSpace(progress.InitialFinding) != "" || len(progress.Unknowns) > 0) &&
		strings.TrimSpace(progress.NextStep) != ""
}

func bundleRequiresRelevantWork(bundle agentcontext.Bundle) bool {
	if bundle.TaskClass == domain.TaskClassInvestigation ||
		bundle.TaskClass == domain.TaskClassCoding ||
		bundle.WorkKind == domain.WorkKindCodingQuestion {
		return true
	}
	return requiresRelevantWork(bundle.TaskSummary) ||
		requiresRelevantWork(bundle.Event.Content)
}

func isDelegatedInvocation(bundle agentcontext.Bundle) bool {
	if bundle.User.OpenID != "" && bundle.Event.SenderID == bundle.User.OpenID {
		return false
	}
	if bundle.Event.MentionsUser(bundle.User.OpenID) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(bundle.Event.ChatType), "p2p") &&
		strings.TrimSpace(bundle.Event.SenderID) != ""
}

func requiresRelevantWork(content string) bool {
	lower := strings.ToLower(content)
	return domain.IsCodingQuestion(content) || containsAny(lower,
		"调研", "检查", "确认", "核对", "分析", "排查", "研究", "对齐", "处理", "实现",
		"修改", "删除", "发布", "部署", "同步", "review", "check", "investigate", "analyze",
	)
}

func containsFutureCommitment(reply string) bool {
	lower := strings.ToLower(reply)
	return containsAny(lower,
		"我会", "我们会", "将会", "稍后", "后续会", "之后会", "忙完后", "尽快",
		"马上安排", "对齐后同步", "确认后同步", "完成后同步", "之后同步", "后续同步",
		"稍后同步", "我来确认", "我来调查", "有结果再", "有结果会", "will follow up",
		"will deliver", "will coordinate",
	)
}

func isAcknowledgementOnly(reply string) bool {
	compact := compactText(reply)
	if len([]rune(compact)) < 20 {
		return true
	}
	return containsAny(compact,
		"收到已提醒测试负责人",
		"已提醒测试负责人请他处理",
		"收到我会让owner确认",
		"收到我们会",
	) && !containsCompletedWorkSignal(reply)
}

func containsCompletedWorkSignal(reply string) bool {
	lower := strings.ToLower(reply)
	return containsAny(lower,
		"我查了", "已查", "我检查了", "已检查", "我核对了", "已核对", "我读了", "已读取",
		"初步发现", "目前确认", "当前确认", "未找到", "仍未确认", "依据", "结论",
		"i checked", "i reviewed", "initial finding", "not yet verified",
	)
}

func highRestatementSimilarity(request, reply string) bool {
	left := runeBigrams(compactText(request))
	right := runeBigrams(compactText(reply))
	if len(left) < 4 || len(right) < 4 {
		return false
	}
	intersection := 0
	union := make(map[string]bool, len(left)+len(right))
	for token := range left {
		union[token] = true
		if right[token] {
			intersection++
		}
	}
	for token := range right {
		union[token] = true
	}
	return float64(intersection)/float64(len(union)) >= 0.72
}

func compactText(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func runeBigrams(value string) map[string]bool {
	runes := []rune(value)
	out := make(map[string]bool)
	for index := 0; index+1 < len(runes); index++ {
		out[string(runes[index:index+2])] = true
	}
	return out
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func qualityError(format string, args ...any) error {
	return errs.NewInternalError(errs.SubtypeInvalidResponse, "%s", fmt.Sprintf(format, args...))
}

func hasProductionSource(sources []domain.SourceRef) bool {
	for _, source := range sources {
		path := strings.ToLower(filepath.ToSlash(strings.TrimSpace(source.RelativePath)))
		if path == "" || source.Kind == "lark_message" || source.Kind == "rule" {
			continue
		}
		if isSupportingSourcePath(path) {
			continue
		}
		return true
	}
	return false
}

func isSupportingSourcePath(path string) bool {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".md") {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		switch segment {
		case "example", "examples", "test", "tests", "testdata", "fixture", "fixtures", "doc", "docs":
			return true
		}
	}
	return strings.HasPrefix(base, "readme")
}

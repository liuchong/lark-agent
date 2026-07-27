// Package router applies deterministic gates before the model sees work.
package router

import (
	"context"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

// Config controls deterministic routing.
type Config struct {
	OwnerOpenID       string
	AssistantOpenIDs  []string
	AssistantNames    []string
	OwnerDirect       bool
	Mode              domain.Mode
	AllowChats        []string
	BlockChats        []string
	BlockUsers        []string
	Sensitivity       domain.Sensitivity
	Now               func() time.Time
	DisableFastPath   bool
	DisableCodingGoal bool
	StatusText        func() string
	DoctorText        func() string
	QueueSummaryText  func() string
	HelpText          string
}

// Router decides whether a work item should enter the agent loop.
type Router struct {
	cfg Config
}

// New creates a Router.
func New(cfg Config) *Router {
	if cfg.Mode == "" {
		cfg.Mode = domain.ModeAuto
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if !cfg.OwnerDirect && (len(cfg.AssistantOpenIDs) > 0 || len(cfg.AssistantNames) > 0) {
		cfg.OwnerDirect = true
	}
	return &Router{cfg: cfg}
}

// Route applies deterministic policy and a small lexical fallback for tests.
// Full model relevance can be layered behind this boundary.
func (r *Router) Route(_ context.Context, item domain.WorkItem) (domain.Decision, error) {
	decision := domain.Decision{
		Mode:       r.cfg.Mode,
		Kind:       domain.DecisionIgnore,
		Relevance:  domain.RelevanceNone,
		Confidence: 0,
		Risk:       domain.RiskLow,
		Reason:     "not_relevant",
	}
	if r.cfg.Mode == domain.ModePaused {
		decision.Reason = "agent_paused"
		return decision, nil
	}
	if contains(r.cfg.BlockChats, item.Event.ChatID) || contains(r.cfg.BlockUsers, item.Event.SenderID) {
		decision.Reason = "blocked"
		return decision, nil
	}
	if len(r.cfg.AllowChats) > 0 && !contains(r.cfg.AllowChats, item.Event.ChatID) {
		decision.Reason = "chat_not_allowed"
		return decision, nil
	}
	if r.assistantMentioned(item.Event) || r.assistantPrivateChat(item.Event) || r.assistantTextMentioned(item.Event) {
		if !r.cfg.OwnerDirect || item.Event.SenderID != r.cfg.OwnerOpenID {
			decision.Reason = "assistant_request_from_non_owner"
			return decision, nil
		}
		decision.Kind = domain.DecisionNotify
		decision.Relevance = domain.RelevanceOwnerRequest
		decision.WorkKind = domain.WorkKindSimpleQuestion
		decision.Priority = domain.PrioritySimpleQuestion
		decision.Confidence = 1
		if r.assistantPrivateChat(item.Event) {
			decision.Reason = "owner_assistant_private_chat"
		} else if r.assistantTextMentioned(item.Event) && !r.assistantMentioned(item.Event) {
			decision.Reason = "owner_assistant_text_mention"
		} else {
			decision.Reason = "owner_assistant_mention"
		}
		if !r.cfg.DisableFastPath {
			if fast, ok := r.fastPathDecision(item.Event, decision); ok {
				return fast, nil
			}
		}
		if !r.cfg.DisableCodingGoal && isCodingGoal(item.Event.Content) {
			decision.WorkKind = domain.WorkKindCodingGoal
			decision.Priority = domain.PriorityBackground
		} else if isCodingQuestion(item.Event.Content) {
			decision.WorkKind = domain.WorkKindCodingQuestion
			decision.Priority = domain.PriorityCodingQuestion
		}
		return decision, nil
	}
	if item.Event.MentionsUser(r.cfg.OwnerOpenID) {
		decision.Kind = domain.DecisionNotify
		decision.Relevance = domain.RelevanceDirectMention
		decision.WorkKind = domain.WorkKindDirectMention
		decision.Priority = domain.PriorityDirectMention
		decision.Confidence = 1
		decision.Reason = "direct_mention"
		return decision, nil
	}
	if inferredRelevant(item.Event.Content, r.cfg.Sensitivity) {
		decision.Kind = domain.DecisionRecord
		decision.Relevance = domain.RelevanceInferred
		decision.WorkKind = domain.WorkKindGeneric
		decision.Priority = domain.PriorityBackground
		decision.Confidence = 0.7
		decision.Reason = "inferred_relevance"
		return decision, nil
	}
	return decision, nil
}

func (r *Router) fastPathDecision(event domain.NormalizedEvent, base domain.Decision) (domain.Decision, bool) {
	content := strings.ToLower(strings.TrimSpace(event.Content))
	for _, name := range r.cfg.AssistantNames {
		name = strings.TrimSpace(name)
		if name != "" {
			content = strings.TrimSpace(strings.TrimPrefix(content, strings.ToLower("@"+name)))
		}
	}
	if fields := strings.Fields(content); len(fields) > 1 && strings.HasPrefix(fields[0], "@_user_") {
		content = strings.TrimSpace(strings.TrimPrefix(content, fields[0]))
	}
	content = strings.TrimSpace(strings.TrimRight(content, "?？。！!"))
	if oneOf(content, "在吗", "你好", "您好", "hi", "hello") {
		return fastPathReply(base, "在的。", "fast_path_availability"), true
	}
	if oneOf(content, "几点了", "现在几点", "现在几点了", "现在时间", "time") {
		now := r.cfg.Now()
		base.Kind = domain.DecisionReply
		base.WorkKind = domain.WorkKindFastPath
		base.Priority = domain.PriorityFastPath
		base.Confidence = 1
		base.Risk = domain.RiskLow
		base.ReplyText = "现在是 " + now.Format("15:04") + "。"
		base.Reason = appendReason(base.Reason, "fast_path_time")
		return base, true
	}
	if oneOf(content, "今天几号", "今天日期", "日期", "date") {
		return fastPathReply(base, r.cfg.Now().Format("2006-01-02"), "fast_path_date"), true
	}
	if content == "ping" {
		return fastPathReply(base, "pong", "fast_path_ping"), true
	}
	if isResponseStatusQuestion(content) {
		text := "lark-agent 正在运行。"
		if r.cfg.StatusText != nil {
			text = r.cfg.StatusText()
		}
		return fastPathReply(base, text, "fast_path_response_status"), true
	}
	switch content {
	case "状态", "状态如何", "status":
		text := "lark-agent 正在运行。"
		if r.cfg.StatusText != nil {
			text = r.cfg.StatusText()
		}
		return fastPathReply(base, text, "fast_path_status"), true
	case "doctor", "诊断":
		text := "doctor 可用；请运行 lark-agent doctor 查看完整诊断。"
		if r.cfg.DoctorText != nil {
			text = r.cfg.DoctorText()
		}
		return fastPathReply(base, text, "fast_path_doctor"), true
	case "队列", "队列摘要", "queue", "queue summary":
		text := "当前队列可用。"
		if r.cfg.QueueSummaryText != nil {
			text = r.cfg.QueueSummaryText()
		}
		return fastPathReply(base, text, "fast_path_queue_summary"), true
	case "help", "帮助":
		text := r.cfg.HelpText
		if strings.TrimSpace(text) == "" {
			text = "可直接问时间、日期、状态、doctor、队列摘要，或提出代码问题。"
		}
		return fastPathReply(base, text, "fast_path_help"), true
	}
	return domain.Decision{}, false
}

func isResponseStatusQuestion(content string) bool {
	for _, keyword := range []string{"为什么不说话", "为什么不回答", "为什么没回答", "为什么不回应", "怎么不说话", "怎么不回答"} {
		if strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func fastPathReply(base domain.Decision, text, reason string) domain.Decision {
	base.Kind = domain.DecisionReply
	base.WorkKind = domain.WorkKindFastPath
	base.Priority = domain.PriorityFastPath
	base.Confidence = 1
	base.Risk = domain.RiskLow
	base.ReplyText = text
	base.Reason = appendReason(base.Reason, reason)
	return base
}

func isCodingQuestion(content string) bool {
	content = strings.ToLower(content)
	keywords := []string{"代码", "接口", "sampledb", "mysql", "redis", "函数", "类", "基于代码", "为什么每次", "bug", "报错", "实现"}
	for _, keyword := range keywords {
		if strings.Contains(content, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func isCodingGoal(content string) bool {
	content = strings.ToLower(content)
	keywords := []string{"持续跟进", "后台处理", "完成后通知", "长期任务", "分多轮"}
	for _, keyword := range keywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

func appendReason(reason, next string) string {
	if reason == "" {
		return next
	}
	if next == "" {
		return reason
	}
	return reason + ";" + next
}

func (r *Router) assistantMentioned(event domain.NormalizedEvent) bool {
	for _, mention := range event.Mentions {
		if mention.OpenID != "" && contains(r.cfg.AssistantOpenIDs, mention.OpenID) {
			return true
		}
		if mention.Name != "" && containsFold(r.cfg.AssistantNames, mention.Name) {
			return true
		}
	}
	return false
}

func (r *Router) assistantPrivateChat(event domain.NormalizedEvent) bool {
	chatType := strings.ToLower(event.ChatType)
	if chatType != "p2p" && chatType != "private" {
		return false
	}
	if event.ChatPartnerID != "" && contains(r.cfg.AssistantOpenIDs, event.ChatPartnerID) {
		return true
	}
	return containsFold(r.cfg.AssistantNames, event.ChatName)
}

func (r *Router) assistantTextMentioned(event domain.NormalizedEvent) bool {
	content := strings.TrimSpace(event.Content)
	for _, name := range r.cfg.AssistantNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(content, "@"+name) {
			return true
		}
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func inferredRelevant(content string, sensitivity domain.Sensitivity) bool {
	content = strings.ToLower(content)
	keywords := []string{"owner", "负责", "项目", "任务", "deadline", "blocker"}
	if sensitivity == domain.SensitivityHigh {
		keywords = append(keywords, "help", "check", "看看")
	}
	for _, keyword := range keywords {
		if strings.Contains(content, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

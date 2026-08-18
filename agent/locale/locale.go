// Package locale resolves the language of one bounded Lark interaction and
// renders the small set of deterministic outward messages owned by runtime.
package locale

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
)

// Language is one supported outward-message language policy.
type Language string

const (
	LanguageAuto    Language = "auto"
	LanguageChinese Language = "zh-CN"
	LanguageEnglish Language = "en-US"
)

var latinWord = regexp.MustCompile(`[A-Za-z]{2,}`)

// ParsePreferred validates a configured preferred language.
func ParsePreferred(value string) (Language, error) {
	language := normalize(value)
	switch language {
	case LanguageAuto, LanguageChinese, LanguageEnglish:
		return language, nil
	default:
		return "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported preferred language: %s",
			value,
		)
	}
}

// ParseConcrete validates a non-auto fallback language.
func ParseConcrete(value string) (Language, error) {
	language := normalize(value)
	switch language {
	case LanguageChinese, LanguageEnglish:
		return language, nil
	default:
		return "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported fallback language: %s",
			value,
		)
	}
}

func normalize(value string) Language {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return LanguageAuto
	case "zh", "zh-cn", "zh_hans", "zh-hans":
		return LanguageChinese
	case "en", "en-us", "en_us":
		return LanguageEnglish
	default:
		return Language(strings.TrimSpace(value))
	}
}

// Resolve applies configuration first, then deterministic script inference.
func Resolve(preferred, fallback Language, samples ...string) Language {
	if preferred == LanguageChinese || preferred == LanguageEnglish {
		return preferred
	}
	if fallback != LanguageChinese && fallback != LanguageEnglish {
		fallback = LanguageChinese
	}
	var han, latin int
	for _, sample := range samples {
		for _, r := range sample {
			if unicode.Is(unicode.Han, r) {
				han++
			}
		}
		latin += len(latinWord.FindAllString(sample, -1))
	}
	if han >= 4 && han >= latin {
		return LanguageChinese
	}
	if latin >= 4 && latin*2 > han {
		return LanguageEnglish
	}
	return fallback
}

// ValidateProse rejects paragraph-scale language mismatches while allowing
// identifiers, paths, error codes, and short quoted terms.
func ValidateProse(text string, target Language) error {
	var han int
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			han++
		}
	}
	latin := len(latinWord.FindAllString(text, -1))
	switch target {
	case LanguageChinese:
		if latin >= 8 && (han == 0 || latin > han) {
			return errs.NewValidationError(
				errs.SubtypeInvalidResponse,
				"reply prose does not match required language zh-CN",
			)
		}
	case LanguageEnglish:
		if han >= 8 && han > latin {
			return errs.NewValidationError(
				errs.SubtypeInvalidResponse,
				"reply prose does not match required language en-US",
			)
		}
	}
	return nil
}

// RenderDelegatedReply gives sender-facing delegated replies an explicit
// assistant identity and owner-notification disclosure.
func RenderDelegatedReply(language Language, ownerName, content string) (string, error) {
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidConfig,
			"owner.name is required for delegated replies",
		).WithField("owner.name")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidResponse,
			"delegated reply content is required",
		)
	}
	if language == LanguageEnglish {
		return fmt.Sprintf(
			"🤖 Intelligent Assistant: %s\n\nI have shared this result with %s.",
			content,
			ownerName,
		), nil
	}
	return fmt.Sprintf(
		"🤖 智能助手：%s\n\n我已将处理结果通知%s。",
		content,
		ownerName,
	), nil
}

func renderDelegatedReplyWithoutOwnerNotice(language Language, content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", errs.NewValidationError(
			errs.SubtypeInvalidResponse,
			"delegated reply content is required",
		)
	}
	if language == LanguageEnglish {
		return "🤖 Intelligent Assistant: " + content, nil
	}
	return "🤖 智能助手：" + content, nil
}

// DelegatedPresenter applies the deterministic delegated identity immediately
// before the exact reply draft is persisted or sent.
type DelegatedPresenter struct {
	OwnerOpenID string
	OwnerName   string
	Preferred   Language
	Fallback    Language
}

// Present returns an unchanged owner/assistant reply or a wrapped delegated
// reply. Mention metadata may supply a missing configured name for group work.
func (p DelegatedPresenter) Present(item domain.WorkItem, decision domain.Decision) (domain.Decision, error) {
	if decision.Kind != domain.DecisionReply && decision.Kind != domain.DecisionRequestApproval {
		return decision, nil
	}
	language := Language(decision.Language)
	if language != LanguageChinese && language != LanguageEnglish {
		language = Resolve(p.Preferred, p.Fallback, item.Event.Content)
		decision.Language = string(language)
	}
	if err := ValidateProse(decision.ReplyText, language); err != nil {
		return domain.Decision{}, err
	}
	if item.Event.SenderID == p.OwnerOpenID ||
		(decision.Relevance != domain.RelevanceDirectMention &&
			decision.Relevance != domain.RelevancePrivateMessage) {
		return decision, nil
	}
	if strings.HasPrefix(strings.TrimSpace(decision.ReplyText), "🤖 智能助手：") ||
		strings.HasPrefix(strings.TrimSpace(decision.ReplyText), "🤖 Intelligent Assistant:") {
		return decision, nil
	}
	ownerName := strings.TrimSpace(p.OwnerName)
	if ownerName == "" {
		for _, mention := range item.Event.Mentions {
			if mention.OpenID == p.OwnerOpenID && strings.TrimSpace(mention.Name) != "" {
				ownerName = strings.TrimSpace(mention.Name)
				break
			}
		}
	}
	var rendered string
	var err error
	if decision.WorkKind == domain.WorkKindResourceHandoff {
		rendered, err = renderDelegatedReplyWithoutOwnerNotice(
			language,
			decision.ReplyText,
		)
	} else {
		rendered, err = RenderDelegatedReply(
			language,
			ownerName,
			decision.ReplyText,
		)
	}
	if err != nil {
		return domain.Decision{}, err
	}
	decision.ReplyText = rendered
	return decision, nil
}

// LocalizedReason returns a safe outward summary instead of exposing internal
// model prose verbatim.
func LocalizedReason(language Language, reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "task_rules_unavailable"):
		if language == LanguageEnglish {
			return "The private task-rules file is enabled but cannot be read."
		}
		return "已启用的私人任务规则文件当前无法读取"
	case strings.Contains(lower, "task_rules_changed"):
		if language == LanguageEnglish {
			return "The private task-rules file changed; the previous draft was cancelled."
		}
		return "私人任务规则已更新，旧草稿已取消"
	case strings.Contains(lower, "owner_reply_ambiguous") ||
		strings.Contains(lower, "did not converge"):
		if language == LanguageEnglish {
			return "The delegated reply could not be confirmed before the retry limit."
		}
		return "在重试上限内未能确认这条委托回复"
	case strings.Contains(lower, "owner_reaction_read_failed") ||
		strings.Contains(lower, "owner reaction read failed"):
		if language == LanguageEnglish {
			return "Owner acknowledgement reactions could not be read."
		}
		return "无法读取负责人确认表情"
	case strings.Contains(lower, "context") || strings.Contains(reason, "上下文"):
		if language == LanguageEnglish {
			return "The referenced conversation context is incomplete."
		}
		return "引用的会话上下文不完整"
	case strings.Contains(lower, "non_convergence") ||
		strings.Contains(lower, "terminal decision") ||
		strings.Contains(lower, "maximum turns"):
		if language == LanguageEnglish {
			return "The model did not converge within the configured budget."
		}
		return "模型未能在配置预算内形成有效结论"
	case strings.Contains(lower, "model provider authentication"):
		if language == LanguageEnglish {
			return "The model provider rejected its configured credential."
		}
		return "模型供应商拒绝了当前配置的凭据"
	case strings.Contains(lower, "model provider permission"):
		if language == LanguageEnglish {
			return "The model provider denied this request."
		}
		return "模型供应商拒绝了本次请求权限"
	case strings.Contains(lower, "model provider quota_exhausted"):
		if language == LanguageEnglish {
			return "The model provider quota is exhausted."
		}
		return "模型供应商额度已耗尽"
	case strings.Contains(lower, "model provider overloaded"):
		if language == LanguageEnglish {
			return "The model provider remained overloaded after bounded retries."
		}
		return "模型供应商持续过载，已用尽有界重试"
	case strings.Contains(lower, "model provider rate_limit"):
		if language == LanguageEnglish {
			return "The model provider remained rate-limited after bounded retries."
		}
		return "模型供应商持续限流，已用尽有界重试"
	case strings.Contains(lower, "timeout"):
		if language == LanguageEnglish {
			return "The model provider timed out after bounded retries."
		}
		return "模型供应商持续超时，已用尽有界重试"
	default:
		if language == LanguageEnglish {
			return "Processing stopped before a reliable result could be produced."
		}
		return "处理在形成可靠结果前停止"
	}
}

package github

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const mentionNeedle = "@lark-agent"

var (
	slashCommandPattern = regexp.MustCompile(`^/[a-z][a-z0-9-]*$`)
	bareFlagPattern     = regexp.MustCompile(`^--[a-z][a-z0-9-]*$`)
	knownSlashCommands  = map[string]bool{
		"review": true,
		"title":  true,
		"check":  true,
	}
)

// Mention is the parsed @lark-agent grammar from a GitHub comment body.
type Mention struct {
	Matched        bool
	Command        string
	DryRun         bool
	ExtraPrompt    string
	UnknownCommand bool
	FlagError      string
}

// ParseMention implements SC-06…SC-09 and SC-17/23–27/43–47/74–76.
func ParseMention(body string) Mention {
	_, end, ok := findMention(body)
	if !ok {
		return Mention{}
	}
	rest := body[end:]
	if rest != "" {
		r, size := utf8.DecodeRuneInString(rest)
		if strings.ContainsRune(".,!?;:", r) {
			rest = rest[size:]
		}
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if rest == "" {
		return Mention{Matched: true}
	}
	tokens, spans := tokenize(rest)
	index := 0
	command := ""
	if len(tokens) > 0 && slashCommandPattern.MatchString(tokens[0]) {
		command = tokens[0][1:]
		index = 1
	}
	dryRun := false
	for index < len(tokens) {
		token := tokens[index]
		if !strings.HasPrefix(token, "--") {
			break
		}
		if !bareFlagPattern.MatchString(token) || token != "--dry-run" {
			return Mention{
				Matched:   true,
				FlagError: "invalid or unknown flag; only --dry-run is allowed",
			}
		}
		dryRun = true
		index++
	}
	extra := ""
	if index < len(tokens) {
		extra = rest[spans[index][0]:]
		if strings.HasPrefix(extra, " ") || strings.HasPrefix(extra, "\n") {
			extra = extra[1:]
		}
	}
	parsed := Mention{
		Matched:     true,
		Command:     command,
		DryRun:      dryRun,
		ExtraPrompt: extra,
	}
	if command != "" && !knownSlashCommands[command] {
		parsed.UnknownCommand = true
	}
	return parsed
}

func findMention(body string) (start, end int, ok bool) {
	from := 0
	lower := strings.ToLower(body)
	for from < len(lower) {
		rel := strings.Index(lower[from:], mentionNeedle)
		if rel < 0 {
			return 0, 0, false
		}
		idx := from + rel
		if idx > 0 {
			r, _ := utf8.DecodeLastRuneInString(body[:idx])
			if r == utf8.RuneError || !unicode.IsSpace(r) {
				from = idx + 1
				continue
			}
		}
		end = idx + len(mentionNeedle)
		if end == len(body) {
			return idx, end, true
		}
		r, _ := utf8.DecodeRuneInString(body[end:])
		if unicode.IsSpace(r) || strings.ContainsRune(".,!?;:", r) {
			return idx, end, true
		}
		from = idx + 1
	}
	return 0, 0, false
}

func tokenize(s string) (tokens []string, spans [][2]int) {
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				tokens = append(tokens, s[start:i])
				spans = append(spans, [2]int{start, i})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		tokens = append(tokens, s[start:])
		spans = append(spans, [2]int{start, len(s)})
	}
	return tokens, spans
}

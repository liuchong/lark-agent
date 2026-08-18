package github

import (
	"fmt"
	"strings"
)

const (
	ActionPostGitHubComment      = "post_github_comment"
	ActionUpsertGitHubCheck      = "upsert_github_check"
	ActionUpdateGitHubIssueTitle = "update_github_issue_title"
	ActionSendLarkMessage        = "send_lark_message"
	ActionWriteJobOutput         = "write_job_output"
)

var knownWriteActions = map[string]bool{
	ActionPostGitHubComment:      true,
	ActionUpsertGitHubCheck:      true,
	ActionUpdateGitHubIssueTitle: true,
	ActionSendLarkMessage:        true,
	ActionWriteJobOutput:         true,
}

// ParseAllowedActions splits a comma-separated allowlist. Unknown names fail.
func ParseAllowedActions(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if !knownWriteActions[name] {
			return nil, fmt.Errorf("unknown allowed action %q", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// SlashExtras returns write names added by a parsed slash command.
func SlashExtras(command string, hasPR bool) []string {
	switch command {
	case "review", "check":
		if hasPR {
			return []string{ActionUpsertGitHubCheck}
		}
		return nil
	case "title":
		return []string{ActionUpdateGitHubIssueTitle}
	default:
		return nil
	}
}

// EffectiveAllowlist unions CLI names with slash extras, then clears writes on dry-run.
func EffectiveAllowlist(base []string, command string, hasPR, dryRun bool) []string {
	if dryRun {
		return []string{}
	}
	seen := map[string]bool{}
	var out []string
	add := func(names []string) {
		for _, name := range names {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	add(base)
	add(SlashExtras(command, hasPR))
	if out == nil {
		return []string{}
	}
	return out
}

func AllowlistContains(actions []string, name string) bool {
	for _, action := range actions {
		if action == name {
			return true
		}
	}
	return false
}

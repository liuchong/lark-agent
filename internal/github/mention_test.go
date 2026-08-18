package github

import (
	"strings"
	"testing"
)

func TestParseMentionFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id        string
		body      string
		matched   bool
		command   string
		dryRun    bool
		extra     string
		unknown   bool
		flagError bool
	}{
		{id: "SC-06", body: "@lark-agent why is this failing", matched: true, extra: "why is this failing"},
		{id: "SC-07", body: "@lark-agent /review focus on auth", matched: true, command: "review", extra: "focus on auth"},
		{id: "SC-08", body: "@lark-agent review now", matched: true, extra: "review now"},
		{id: "SC-09", body: "@lark-agent /nope", matched: true, command: "nope", unknown: true},
		{id: "SC-23", body: "@lark-agent /review --dry-run", matched: true, command: "review", dryRun: true},
		{id: "SC-24", body: "@lark-agent --dry-run hello", matched: true, dryRun: true, extra: "hello"},
		{id: "SC-25", body: "@lark-agent", matched: true},
		{id: "SC-26", body: "please @LARK-AGENT /title", matched: true, command: "title"},
		{id: "SC-27", body: "foo@lark-agent /review"},
		{id: "SC-08b", body: "@lark-agent /review\n重点看权限", matched: true, command: "review", extra: "重点看权限"},
		{id: "SC-43", body: "@lark-agent /REVIEW now", matched: true, extra: "/REVIEW now"},
		{id: "SC-44", body: "@lark-agent /review --force", matched: true, flagError: true},
		{id: "SC-45", body: "@lark-agent[bot] /review"},
		{id: "SC-46", body: "see `@lark-agent` please"},
		{id: "SC-47", body: "@lark-agent first @lark-agent /title", matched: true, extra: "first @lark-agent /title"},
		{id: "SC-74", body: "@lark-agent --dry-run --dry-run hi", matched: true, dryRun: true, extra: "hi"},
		{id: "SC-75", body: "@lark-agent /check", matched: true, command: "check"},
		{id: "SC-76", body: "@lark-agent /title make it shorter", matched: true, command: "title", extra: "make it shorter"},
		{id: "SC-08c", body: "@lark-agent.", matched: true},
		{id: "SC-08d", body: "@lark-agent. why", matched: true, extra: "why"},
		{id: "SC-23b", body: "@lark-agent /review --dry-run=true", matched: true, flagError: true},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got := ParseMention(tc.body)
			if got.Matched != tc.matched {
				t.Fatalf("matched=%v want %v %+v", got.Matched, tc.matched, got)
			}
			if !tc.matched {
				return
			}
			if tc.flagError {
				if got.FlagError == "" ||
					!strings.Contains(got.FlagError, "--dry-run") ||
					(!strings.Contains(got.FlagError, "unknown") && !strings.Contains(got.FlagError, "invalid")) {
					t.Fatalf("flag error=%q", got.FlagError)
				}
				return
			}
			if got.FlagError != "" {
				t.Fatalf("unexpected flag error=%q", got.FlagError)
			}
			if got.Command != tc.command || got.DryRun != tc.dryRun || got.ExtraPrompt != tc.extra || got.UnknownCommand != tc.unknown {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

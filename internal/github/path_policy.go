package github

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const CheckRunName = "lark-agent-gate"

var zeroSHA = strings.Repeat("0", 40)

var (
	reRepoRoot      = regexp.MustCompile(`^/repos/[^/]+/[^/]+$`)
	reActionsRun    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/actions/runs/[0-9]+$`)
	reActionsJobs   = regexp.MustCompile(`^/repos/[^/]+/[^/]+/actions/runs/[0-9]+/jobs$`)
	reCommitChecks  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/commits/[0-9a-fA-F]+/check-runs$`)
	reCheckAnnot    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/check-runs/[0-9]+/annotations$`)
	rePull          = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/[0-9]+$`)
	rePullFiles     = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/[0-9]+/files$`)
	rePullReviews   = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/[0-9]+/reviews$`)
	reIssue         = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/[0-9]+$`)
	reIssueComments = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/[0-9]+/comments$`)
	reContents      = regexp.MustCompile(`^/repos/[^/]+/[^/]+/contents/`)
	reCompare       = regexp.MustCompile(`^/repos/[^/]+/[^/]+/compare/`)
	reReleases      = regexp.MustCompile(`^/repos/[^/]+/[^/]+/releases$`)
	reCheckRuns     = regexp.MustCompile(`^/repos/[^/]+/[^/]+/check-runs$`)
	reCheckRunID    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/check-runs/[0-9]+$`)
)

func normalizeGitHubPath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	return path
}

func assertAllowedGitHubHTTP(method, urlPath, repository string) error {
	path := normalizeGitHubPath(urlPath)
	lower := strings.ToLower(path)
	for _, forbidden := range []string{
		"/merge", "/git/", "/actions/runners", "/installation", "/orgs/",
		"/actions/secrets", "/actions/variables",
	} {
		if strings.Contains(lower, forbidden) {
			return Failure{Kind: FailureInvalidData, Message: "forbidden github path"}
		}
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 {
		return Failure{Kind: FailureInvalidData, Message: "forbidden github path"}
	}
	prefix := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	if path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return Failure{Kind: FailureInvalidData, Message: "forbidden github path"}
	}
	switch strings.ToUpper(method) {
	case "GET":
		if reRepoRoot.MatchString(path) || reActionsRun.MatchString(path) || reActionsJobs.MatchString(path) ||
			reCommitChecks.MatchString(path) || reCheckAnnot.MatchString(path) || rePull.MatchString(path) ||
			rePullFiles.MatchString(path) || rePullReviews.MatchString(path) || reIssue.MatchString(path) ||
			reIssueComments.MatchString(path) || reContents.MatchString(path) || reCompare.MatchString(path) ||
			reReleases.MatchString(path) {
			return nil
		}
	case "POST":
		if reIssueComments.MatchString(path) || reCheckRuns.MatchString(path) {
			return nil
		}
	case "PATCH":
		if reCheckRunID.MatchString(path) || reIssue.MatchString(path) {
			return nil
		}
	}
	return Failure{Kind: FailureInvalidData, Message: fmt.Sprintf("forbidden github path %s %s", method, path)}
}

func IsZeroSHA(sha string) bool {
	return strings.TrimSpace(sha) == zeroSHA
}

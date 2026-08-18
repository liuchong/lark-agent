package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Section string

const (
	SectionSummary Section = "summary"
	SectionChecks  Section = "checks"
	SectionFiles   Section = "files"
	SectionReviews Section = "reviews"
)

type Limits struct {
	MaxFiles       int
	MaxPatchBytes  int
	MaxAnnotations int
	MaxReviews     int
}

func DefaultLimits() Limits {
	return Limits{
		MaxFiles:       50,
		MaxPatchBytes:  64 * 1024,
		MaxAnnotations: 50,
		MaxReviews:     50,
	}
}

type ClientConfig struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Limits     Limits
}

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
	limits     Limits
}

type Truncation struct {
	Jobs        bool `json:"jobs,omitempty"`
	Files       bool `json:"files,omitempty"`
	Patches     bool `json:"patches,omitempty"`
	Annotations bool `json:"annotations,omitempty"`
	Reviews     bool `json:"reviews,omitempty"`
}

func (t Truncation) Any() bool {
	return t.Jobs || t.Files || t.Patches || t.Annotations || t.Reviews
}

type OmittedCounts struct {
	Jobs        int `json:"jobs,omitempty"`
	Files       int `json:"files,omitempty"`
	PatchBytes  int `json:"patch_bytes,omitempty"`
	Annotations int `json:"annotations,omitempty"`
	Reviews     int `json:"reviews,omitempty"`
}

func (o OmittedCounts) Any() bool {
	return o.Jobs > 0 || o.Files > 0 || o.PatchBytes > 0 || o.Annotations > 0 || o.Reviews > 0
}

type ContextResult struct {
	Reference   Reference           `json:"reference"`
	Name        string              `json:"name,omitempty"`
	Status      string              `json:"status,omitempty"`
	Conclusion  string              `json:"conclusion,omitempty"`
	Jobs        []JobSummary        `json:"jobs,omitempty"`
	Files       []FileSummary       `json:"files,omitempty"`
	Reviews     []ReviewSummary     `json:"reviews,omitempty"`
	Annotations []AnnotationSummary `json:"annotations,omitempty"`
	Truncated   Truncation          `json:"truncated,omitempty"`
	Omitted     OmittedCounts       `json:"omitted,omitempty"`
	Partial     bool                `json:"partial,omitempty"`
}

type FailureKind string

const (
	FailureInvalidData FailureKind = "invalid_data"
	FailureNotFound    FailureKind = "not_found"
	FailureForbidden   FailureKind = "forbidden"
	FailureRateLimited FailureKind = "rate_limited"
	FailureTransport   FailureKind = "transport"
)

type Failure struct {
	Kind       FailureKind
	StatusCode int
	Message    string
	RetryAfter time.Duration
}

func (f Failure) Error() string {
	if f.Message != "" {
		return fmt.Sprintf("github %s: %s", f.Kind, f.Message)
	}
	return fmt.Sprintf("github %s", f.Kind)
}

func FailureOf(err error) (Failure, bool) {
	var failure Failure
	if !errors.As(err, &failure) {
		return Failure{}, false
	}
	return failure, true
}

func NewClient(cfg ClientConfig) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = "https://api.github.com"
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid github API base URL")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("github token is required")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	limits := cfg.Limits
	if limits.MaxFiles <= 0 || limits.MaxPatchBytes <= 0 || limits.MaxAnnotations <= 0 || limits.MaxReviews <= 0 {
		return nil, fmt.Errorf("github limits must be positive")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:    parsed,
		token:      cfg.Token,
		httpClient: httpClient,
		limits:     limits,
	}, nil
}

// FetchContext reads only objects selected by the verified reference.
func (c *Client) FetchContext(ctx context.Context, ref Reference, sections []Section) (ContextResult, error) {
	if err := ref.Validate(); err != nil {
		return ContextResult{}, Failure{Kind: FailureInvalidData, Message: err.Error()}
	}
	sections, err := normalizeSections(sections)
	if err != nil {
		return ContextResult{}, err
	}
	result := ContextResult{Reference: ref}
	for _, section := range sections {
		switch section {
		case SectionSummary:
			if err := c.fetchSummary(ctx, ref, &result); err != nil {
				return ContextResult{}, err
			}
		case SectionChecks:
			if ref.WorkflowRunID <= 0 {
				continue
			}
			if err := c.fetchJobs(ctx, ref, &result); err != nil {
				return ContextResult{}, err
			}
			if ref.HeadSHA != "" {
				if err := c.fetchAnnotations(ctx, ref, &result); err != nil {
					result.Partial = true
				}
			}
		case SectionFiles:
			if ref.PullRequestNumber <= 0 {
				continue
			}
			if err := c.fetchFiles(ctx, ref, &result); err != nil {
				return ContextResult{}, err
			}
		case SectionReviews:
			if ref.PullRequestNumber <= 0 {
				continue
			}
			if err := c.fetchReviews(ctx, ref, &result); err != nil {
				return ContextResult{}, err
			}
		}
	}
	return result, nil
}

func normalizeSections(sections []Section) ([]Section, error) {
	if len(sections) == 0 {
		return []Section{SectionSummary, SectionChecks}, nil
	}
	seen := map[Section]bool{}
	out := make([]Section, 0, len(sections))
	for _, section := range sections {
		switch section {
		case SectionSummary, SectionChecks, SectionFiles, SectionReviews:
		default:
			return nil, Failure{Kind: FailureInvalidData, Message: fmt.Sprintf("unsupported github section %q", section)}
		}
		if !seen[section] {
			seen[section] = true
			out = append(out, section)
		}
	}
	return out, nil
}

func (c *Client) fetchSummary(ctx context.Context, ref Reference, result *ContextResult) error {
	switch ref.Kind {
	case ReferenceIssue:
		var issue struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
		}
		if err := c.getJSON(ctx, c.repoPath(ref.Repository, "issues/"+strconv.Itoa(ref.IssueNumber)), &issue); err != nil {
			return err
		}
		if issue.Number != ref.IssueNumber {
			return Failure{Kind: FailureInvalidData, Message: "issue response has a mismatched number"}
		}
		result.Name = issue.Title
		result.Status = issue.State
		return nil
	case ReferenceRelease:
		var rel struct {
			TagName string `json:"tag_name"`
			HTMLURL string `json:"html_url"`
			Name    string `json:"name"`
		}
		if err := c.getJSON(ctx, c.repoPath(ref.Repository, "releases/tags/"+url.PathEscape(ref.TagName)), &rel); err != nil {
			return err
		}
		result.Name = firstNonEmpty(rel.Name, rel.TagName)
		return nil
	case ReferencePullRequest, ReferenceWorkflowDispatch:
		if ref.PullRequestNumber <= 0 {
			return nil
		}
		return c.fetchPullSummary(ctx, ref, result)
	case ReferencePush:
		return nil
	}
	if ref.WorkflowRunID > 0 {
		var run workflowRunEnvelope
		if err := c.getJSON(ctx, c.repoPath(ref.Repository, "actions/runs/"+strconv.FormatInt(ref.WorkflowRunID, 10)), &run); err != nil {
			return err
		}
		if run.ID <= 0 || run.ID != ref.WorkflowRunID {
			return Failure{Kind: FailureInvalidData, Message: "workflow run response has a mismatched id"}
		}
		result.Name = run.Name
		result.Status = run.Status
		result.Conclusion = run.Conclusion
		return nil
	}
	if ref.PullRequestNumber > 0 {
		return c.fetchPullSummary(ctx, ref, result)
	}
	return nil
}

func (c *Client) fetchPullSummary(ctx context.Context, ref Reference, result *ContextResult) error {
	var pull struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "pulls/"+strconv.Itoa(ref.PullRequestNumber)), &pull); err != nil {
		return err
	}
	if pull.Number != ref.PullRequestNumber {
		return Failure{Kind: FailureInvalidData, Message: "pull request response has a mismatched number"}
	}
	result.Name = pull.Title
	result.Status = pull.State
	if pull.Merged {
		result.Conclusion = "merged"
	}
	return nil
}

func (c *Client) fetchJobs(ctx context.Context, ref Reference, result *ContextResult) error {
	var response struct {
		TotalCount *int         `json:"total_count"`
		Jobs       []JobSummary `json:"jobs"`
	}
	path := c.repoPath(ref.Repository, "actions/runs/"+strconv.FormatInt(ref.WorkflowRunID, 10)+"/jobs?per_page=100")
	if err := c.getJSON(ctx, path, &response); err != nil {
		return err
	}
	if response.TotalCount == nil || response.Jobs == nil || *response.TotalCount < len(response.Jobs) {
		return Failure{Kind: FailureInvalidData, Message: "workflow jobs response is missing or contradicts required fields"}
	}
	if *response.TotalCount > len(response.Jobs) {
		result.Truncated.Jobs = true
		result.Omitted.Jobs = *response.TotalCount - len(response.Jobs)
	}
	result.Jobs = response.Jobs
	return nil
}

func (c *Client) fetchFiles(ctx context.Context, ref Reference, result *ContextResult) error {
	var files []FileSummary
	path := c.repoPath(ref.Repository, "pulls/"+strconv.Itoa(ref.PullRequestNumber)+"/files?per_page=100")
	if err := c.getJSON(ctx, path, &files); err != nil {
		return err
	}
	if len(files) > c.limits.MaxFiles {
		result.Omitted.Files = len(files) - c.limits.MaxFiles
		files = files[:c.limits.MaxFiles]
		result.Truncated.Files = true
	}
	remaining := c.limits.MaxPatchBytes
	for i := range files {
		originalBytes := len(files[i].Patch)
		if len(files[i].Patch) <= remaining {
			remaining -= len(files[i].Patch)
			continue
		}
		if remaining > 0 {
			files[i].Patch = files[i].Patch[:remaining]
			remaining = 0
		} else {
			files[i].Patch = ""
		}
		result.Omitted.PatchBytes += originalBytes - len(files[i].Patch)
		result.Truncated.Patches = true
	}
	result.Files = files
	return nil
}

func (c *Client) fetchReviews(ctx context.Context, ref Reference, result *ContextResult) error {
	var raw []struct {
		State    string `json:"state"`
		Body     string `json:"body"`
		HTMLURL  string `json:"html_url"`
		CommitID string `json:"commit_id"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	path := c.repoPath(ref.Repository, "pulls/"+strconv.Itoa(ref.PullRequestNumber)+"/reviews?per_page=100")
	if err := c.getJSON(ctx, path, &raw); err != nil {
		return err
	}
	if len(raw) > c.limits.MaxReviews {
		result.Omitted.Reviews = len(raw) - c.limits.MaxReviews
		raw = raw[:c.limits.MaxReviews]
		result.Truncated.Reviews = true
	}
	for _, review := range raw {
		result.Reviews = append(result.Reviews, ReviewSummary{
			User: review.User.Login, State: review.State, Body: review.Body,
			HTMLURL: review.HTMLURL, CommitID: review.CommitID,
		})
	}
	return nil
}

func (c *Client) fetchAnnotations(ctx context.Context, ref Reference, result *ContextResult) error {
	var checks struct {
		CheckRuns []struct {
			ID int64 `json:"id"`
		} `json:"check_runs"`
	}
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "commits/"+ref.HeadSHA+"/check-runs?per_page=100"), &checks); err != nil {
		return err
	}
	for _, check := range checks.CheckRuns {
		var annotations []AnnotationSummary
		path := c.repoPath(ref.Repository, "check-runs/"+strconv.FormatInt(check.ID, 10)+"/annotations?per_page=100")
		if err := c.getJSON(ctx, path, &annotations); err != nil {
			return err
		}
		remaining := c.limits.MaxAnnotations - len(result.Annotations)
		if remaining <= 0 {
			result.Truncated.Annotations = true
			return nil
		}
		if len(annotations) > remaining {
			result.Annotations = append(result.Annotations, annotations[:remaining]...)
			result.Omitted.Annotations += len(annotations) - remaining
			result.Truncated.Annotations = true
			return nil
		}
		result.Annotations = append(result.Annotations, annotations...)
	}
	return nil
}

func (c *Client) repoPath(repository, suffix string) string {
	parts := strings.Split(repository, "/")
	return "repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/" + suffix
}

func (c *Client) getJSON(ctx context.Context, path string, target any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, target)
}

func failureFromResponse(response *http.Response, body []byte) Failure {
	kind := FailureTransport
	switch response.StatusCode {
	case http.StatusNotFound:
		kind = FailureNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = FailureForbidden
	case http.StatusTooManyRequests:
		kind = FailureRateLimited
	}
	if response.StatusCode == http.StatusForbidden &&
		(response.Header.Get("X-RateLimit-Remaining") == "0" || response.Header.Get("Retry-After") != "") {
		kind = FailureRateLimited
	}
	var envelope struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &envelope)
	retryAfter := time.Duration(0)
	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		retryAfter = time.Duration(seconds) * time.Second
	}
	return Failure{
		Kind:       kind,
		StatusCode: response.StatusCode,
		Message:    firstNonEmpty(envelope.Message, http.StatusText(response.StatusCode)),
		RetryAfter: retryAfter,
	}
}

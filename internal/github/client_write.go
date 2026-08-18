package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

type CheckUpsert struct {
	Conclusion string
	Title      string
	Summary    string
	Text       string
}

type FileContent struct {
	Path    string `json:"path"`
	SHA     string `json:"sha,omitempty"`
	Content string `json:"content"`
}

type CompareResult struct {
	Unavailable bool          `json:"unavailable,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	HTMLURL     string        `json:"html_url,omitempty"`
	Files       []FileSummary `json:"files,omitempty"`
}

type CommentResult struct {
	ID int64 `json:"id"`
}

type CheckResult struct {
	ID int64 `json:"id"`
}

func (c *Client) GetPull(ctx context.Context, ref Reference) (Reference, error) {
	if err := ref.Validate(); err != nil {
		return Reference{}, Failure{Kind: FailureInvalidData, Message: err.Error()}
	}
	if ref.PullRequestNumber <= 0 {
		return Reference{}, Failure{Kind: FailureInvalidData, Message: "pull request number is required"}
	}
	var pull struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "pulls/"+strconv.Itoa(ref.PullRequestNumber)), &pull); err != nil {
		return Reference{}, err
	}
	if pull.Number != ref.PullRequestNumber {
		return Reference{}, Failure{Kind: FailureInvalidData, Message: "pull request response has a mismatched number"}
	}
	ref.HeadSHA = pull.Head.SHA
	if pull.HTMLURL != "" {
		ref.HTMLURL = pull.HTMLURL
	}
	if err := ref.Validate(); err != nil {
		return Reference{}, Failure{Kind: FailureInvalidData, Message: err.Error()}
	}
	return ref, nil
}

func (c *Client) GetFile(ctx context.Context, ref Reference, filePath string) (FileContent, error) {
	if err := ref.Validate(); err != nil {
		return FileContent{}, Failure{Kind: FailureInvalidData, Message: err.Error()}
	}
	if ref.HeadSHA == "" {
		return FileContent{}, Failure{Kind: FailureInvalidData, Message: "head_sha is required"}
	}
	cleaned, err := sanitizeRepoPath(filePath)
	if err != nil {
		return FileContent{}, err
	}
	var payload struct {
		Type     string `json:"type"`
		Path     string `json:"path"`
		SHA      string `json:"sha"`
		Size     int    `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "contents/"+cleaned+"?ref="+url.QueryEscape(ref.HeadSHA)), &payload); err != nil {
		return FileContent{}, err
	}
	if payload.Type != "file" {
		return FileContent{}, Failure{Kind: FailureInvalidData, Message: "github content is not a file"}
	}
	if payload.Size > 1<<20 {
		return FileContent{}, Failure{Kind: FailureInvalidData, Message: "github file exceeds 1 MiB"}
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return FileContent{}, Failure{Kind: FailureInvalidData, Message: "decode github file: " + err.Error()}
	}
	if len(decoded) > 1<<20 {
		return FileContent{}, Failure{Kind: FailureInvalidData, Message: "github file exceeds 1 MiB"}
	}
	return FileContent{Path: payload.Path, SHA: payload.SHA, Content: string(decoded)}, nil
}

func (c *Client) Compare(ctx context.Context, ref Reference) (CompareResult, error) {
	if err := ref.Validate(); err != nil {
		return CompareResult{}, Failure{Kind: FailureInvalidData, Message: err.Error()}
	}
	if ref.Kind == ReferenceRelease {
		return c.compareRelease(ctx, ref)
	}
	if IsZeroSHA(ref.BeforeSHA) || ref.BeforeSHA == "" || ref.HeadSHA == "" {
		return CompareResult{Unavailable: true, Reason: "no previous commit"}, nil
	}
	var payload struct {
		HTMLURL string        `json:"html_url"`
		Files   []FileSummary `json:"files"`
	}
	spec := url.PathEscape(ref.BeforeSHA) + "..." + url.PathEscape(ref.HeadSHA)
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "compare/"+spec), &payload); err != nil {
		return CompareResult{}, err
	}
	return CompareResult{HTMLURL: payload.HTMLURL, Files: payload.Files}, nil
}

func (c *Client) compareRelease(ctx context.Context, ref Reference) (CompareResult, error) {
	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "releases"), &releases); err != nil {
		return CompareResult{}, err
	}
	previous := ""
	for i, item := range releases {
		if item.TagName == ref.TagName && i+1 < len(releases) {
			previous = releases[i+1].TagName
			break
		}
	}
	if previous == "" {
		return CompareResult{Unavailable: true, Reason: "no previous commit"}, nil
	}
	var payload struct {
		HTMLURL string        `json:"html_url"`
		Files   []FileSummary `json:"files"`
	}
	spec := url.PathEscape(previous) + "..." + url.PathEscape(ref.TagName)
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "compare/"+spec), &payload); err != nil {
		return CompareResult{}, err
	}
	return CompareResult{HTMLURL: payload.HTMLURL, Files: payload.Files}, nil
}

func (c *Client) PostIssueComment(ctx context.Context, ref Reference, body string) (CommentResult, error) {
	number := issueOrPRNumber(ref)
	if number <= 0 {
		return CommentResult{}, Failure{Kind: FailureInvalidData, Message: "issue number is required"}
	}
	var result CommentResult
	if err := c.doJSON(ctx, http.MethodPost, c.repoPath(ref.Repository, "issues/"+strconv.Itoa(number)+"/comments"), map[string]string{"body": body}, &result); err != nil {
		return CommentResult{}, err
	}
	if result.ID <= 0 {
		return CommentResult{}, Failure{Kind: FailureInvalidData, Message: "github comment response is missing id"}
	}
	return result, nil
}

func (c *Client) UpdateIssueTitle(ctx context.Context, ref Reference, title string) error {
	number := issueOrPRNumber(ref)
	if number <= 0 {
		return Failure{Kind: FailureInvalidData, Message: "issue number is required"}
	}
	return c.doJSON(ctx, http.MethodPatch, c.repoPath(ref.Repository, "issues/"+strconv.Itoa(number)), map[string]string{"title": title}, &struct{}{})
}

func (c *Client) UpsertCheck(ctx context.Context, ref Reference, input CheckUpsert) (CheckResult, error) {
	if ref.HeadSHA == "" {
		return CheckResult{}, Failure{Kind: FailureInvalidData, Message: "head_sha is required"}
	}
	var listing struct {
		CheckRuns []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"check_runs"`
	}
	if err := c.getJSON(ctx, c.repoPath(ref.Repository, "commits/"+ref.HeadSHA+"/check-runs?check_name="+url.QueryEscape(CheckRunName)), &listing); err != nil {
		return CheckResult{}, err
	}
	output := map[string]string{"title": input.Title, "summary": input.Summary}
	if strings.TrimSpace(input.Text) != "" {
		output["text"] = input.Text
	}
	payload := map[string]any{
		"status":     "completed",
		"conclusion": input.Conclusion,
		"output":     output,
	}
	for _, run := range listing.CheckRuns {
		if run.Name == CheckRunName && run.ID > 0 {
			var result CheckResult
			if err := c.doJSON(ctx, http.MethodPatch, c.repoPath(ref.Repository, "check-runs/"+strconv.FormatInt(run.ID, 10)), payload, &result); err != nil {
				return CheckResult{}, err
			}
			if result.ID == 0 {
				result.ID = run.ID
			}
			return result, nil
		}
	}
	payload["name"] = CheckRunName
	payload["head_sha"] = ref.HeadSHA
	var created CheckResult
	if err := c.doJSON(ctx, http.MethodPost, c.repoPath(ref.Repository, "check-runs"), payload, &created); err != nil {
		return CheckResult{}, err
	}
	if created.ID <= 0 {
		return CheckResult{}, Failure{Kind: FailureInvalidData, Message: "github check-run response is missing id"}
	}
	return created, nil
}

func issueOrPRNumber(ref Reference) int {
	if ref.PullRequestNumber > 0 {
		return ref.PullRequestNumber
	}
	return ref.IssueNumber
}

func sanitizeRepoPath(filePath string) (string, error) {
	cleaned := strings.TrimSpace(filePath)
	if cleaned == "" || strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "\x00") {
		return "", Failure{Kind: FailureInvalidData, Message: "invalid github file path"}
	}
	if path.IsAbs(cleaned) || strings.Contains(cleaned, "..") {
		return "", Failure{Kind: FailureInvalidData, Message: "invalid github file path"}
	}
	return cleaned, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, target any) error {
	if err := assertAllowedGitHubHTTP(method, path, repositoryFromRepoPath(path)); err != nil {
		return err
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: path})
	if index := strings.IndexByte(path, '?'); index >= 0 {
		endpoint = c.baseURL.ResolveReference(&url.URL{Path: path[:index], RawQuery: path[index+1:]})
	}
	var body io.Reader
	if payload != nil && method != http.MethodGet {
		data, err := json.Marshal(payload)
		if err != nil {
			return Failure{Kind: FailureInvalidData, Message: err.Error()}
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return Failure{Kind: FailureInvalidData, Message: err.Error()}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil && method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Failure{Kind: FailureTransport, Message: err.Error()}
	}
	defer response.Body.Close() //nolint:errcheck
	respBody, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return Failure{Kind: FailureTransport, Message: err.Error()}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return failureFromResponse(response, respBody)
	}
	if target == nil || len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, target); err != nil {
		return Failure{Kind: FailureInvalidData, StatusCode: response.StatusCode, Message: "decode github response: " + err.Error()}
	}
	return nil
}

func repositoryFromRepoPath(path string) string {
	normalized := normalizeGitHubPath(path)
	parts := strings.Split(strings.TrimPrefix(normalized, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		return ""
	}
	owner, err1 := url.PathUnescape(parts[1])
	name, err2 := url.PathUnescape(parts[2])
	if err1 != nil || err2 != nil {
		return parts[1] + "/" + parts[2]
	}
	return owner + "/" + name
}

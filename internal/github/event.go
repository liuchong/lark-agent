package github

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JobSummary is the bounded workflow job projection used by notifications and
// model evidence.
type JobSummary struct {
	ID         int64         `json:"id,omitempty"`
	Name       string        `json:"name"`
	Status     string        `json:"status,omitempty"`
	Conclusion string        `json:"conclusion,omitempty"`
	HTMLURL    string        `json:"html_url,omitempty"`
	Steps      []StepSummary `json:"steps,omitempty"`
}

type StepSummary struct {
	Name       string `json:"name"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

type FileSummary struct {
	Filename  string `json:"filename"`
	Status    string `json:"status,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	Changes   int    `json:"changes,omitempty"`
	Patch     string `json:"patch,omitempty"`
}

type ReviewSummary struct {
	User     string `json:"user,omitempty"`
	State    string `json:"state,omitempty"`
	Body     string `json:"body,omitempty"`
	HTMLURL  string `json:"html_url,omitempty"`
	CommitID string `json:"commit_id,omitempty"`
}

type AnnotationSummary struct {
	Path            string `json:"path,omitempty"`
	StartLine       int    `json:"start_line,omitempty"`
	EndLine         int    `json:"end_line,omitempty"`
	AnnotationLevel string `json:"annotation_level,omitempty"`
	Message         string `json:"message,omitempty"`
	Title           string `json:"title,omitempty"`
}

// Snapshot is the trusted event projection before optional API enrichment.
type Snapshot struct {
	Reference   Reference           `json:"reference"`
	Name        string              `json:"name,omitempty"`
	Status      string              `json:"status,omitempty"`
	Conclusion  string              `json:"conclusion,omitempty"`
	Action      string              `json:"action,omitempty"`
	Title       string              `json:"title,omitempty"`
	FailedJobs  []JobSummary        `json:"failed_jobs,omitempty"`
	Files       []FileSummary       `json:"files,omitempty"`
	Reviews     []ReviewSummary     `json:"reviews,omitempty"`
	Annotations []AnnotationSummary `json:"annotations,omitempty"`
	Omitted     OmittedCounts       `json:"omitted,omitempty"`
	Partial     bool                `json:"partial,omitempty"`
	CommentBody string              `json:"comment_body,omitempty"`
}

type repositoryEnvelope struct {
	FullName string `json:"full_name"`
}

type workflowRunEnvelope struct {
	ID           int64  `json:"id"`
	RunAttempt   int    `json:"run_attempt"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HeadSHA      string `json:"head_sha"`
	HTMLURL      string `json:"html_url"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

type workflowEvent struct {
	Action      string              `json:"action"`
	Repository  repositoryEnvelope  `json:"repository"`
	WorkflowRun workflowRunEnvelope `json:"workflow_run"`
}

type pullRequestEvent struct {
	Action      string             `json:"action"`
	Repository  repositoryEnvelope `json:"repository"`
	Number      int                `json:"number"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

// ParseEvent converts a supported GitHub event into a validated typed snapshot.
func ParseEvent(eventName string, data []byte) (Snapshot, error) {
	switch strings.TrimSpace(eventName) {
	case "workflow_run":
		var event workflowEvent
		if err := decodeExactJSON(data, &event); err != nil {
			return Snapshot{}, err
		}
		ref := Reference{
			SchemaVersion:      1,
			Repository:         event.Repository.FullName,
			Kind:               ReferenceWorkflowRun,
			WorkflowRunID:      event.WorkflowRun.ID,
			WorkflowRunAttempt: event.WorkflowRun.RunAttempt,
			HeadSHA:            event.WorkflowRun.HeadSHA,
			HTMLURL:            event.WorkflowRun.HTMLURL,
		}
		if len(event.WorkflowRun.PullRequests) > 0 {
			ref.PullRequestNumber = event.WorkflowRun.PullRequests[0].Number
		}
		if err := ref.Validate(); err != nil {
			return Snapshot{}, fmt.Errorf("invalid workflow_run event: %w", err)
		}
		return Snapshot{
			Reference:  ref,
			Name:       event.WorkflowRun.Name,
			Status:     event.WorkflowRun.Status,
			Conclusion: event.WorkflowRun.Conclusion,
			Action:     event.Action,
		}, nil
	case "pull_request":
		var event pullRequestEvent
		if err := decodeExactJSON(data, &event); err != nil {
			return Snapshot{}, err
		}
		number := event.PullRequest.Number
		if number == 0 {
			number = event.Number
		}
		ref := Reference{
			SchemaVersion:     1,
			Repository:        event.Repository.FullName,
			Kind:              ReferencePullRequest,
			PullRequestNumber: number,
			HeadSHA:           event.PullRequest.Head.SHA,
			HTMLURL:           event.PullRequest.HTMLURL,
		}
		if err := ref.Validate(); err != nil {
			return Snapshot{}, fmt.Errorf("invalid pull_request event: %w", err)
		}
		return Snapshot{
			Reference: ref,
			Name:      "pull request",
			Action:    event.Action,
			Title:     event.PullRequest.Title,
		}, nil
	case "issues":
		return parseIssuesEvent(data)
	case "issue_comment":
		return parseIssueCommentEvent(data)
	case "pull_request_review_comment":
		return parseReviewCommentEvent(data)
	case "push":
		return parsePushEvent(data)
	case "release":
		return parseReleaseEvent(data)
	case "workflow_dispatch":
		return parseWorkflowDispatchEvent(data)
	default:
		return Snapshot{}, fmt.Errorf("unsupported github event %q", eventName)
	}
}

type issuesEvent struct {
	Action     string             `json:"action"`
	Repository repositoryEnvelope `json:"repository"`
	Issue      struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
	} `json:"issue"`
}

func parseIssuesEvent(data []byte) (Snapshot, error) {
	var event issuesEvent
	if err := decodeExactJSON(data, &event); err != nil {
		return Snapshot{}, err
	}
	ref := Reference{
		SchemaVersion: 1,
		Repository:    event.Repository.FullName,
		Kind:          ReferenceIssue,
		IssueNumber:   event.Issue.Number,
		HTMLURL:       event.Issue.HTMLURL,
	}
	if err := ref.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid issues event: %w", err)
	}
	return Snapshot{Reference: ref, Action: event.Action, Title: event.Issue.Title, Name: "issue"}, nil
}

type issueCommentEvent struct {
	Action     string             `json:"action"`
	Repository repositoryEnvelope `json:"repository"`
	Issue      struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		HTMLURL     string `json:"html_url"`
		PullRequest *struct {
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	} `json:"comment"`
}

func parseIssueCommentEvent(data []byte) (Snapshot, error) {
	var event issueCommentEvent
	if err := decodeExactJSON(data, &event); err != nil {
		return Snapshot{}, err
	}
	if event.Comment.ID <= 0 {
		return Snapshot{}, fmt.Errorf("invalid issue_comment event: comment id is required")
	}
	ref := Reference{
		SchemaVersion: 1,
		Repository:    event.Repository.FullName,
		IssueNumber:   event.Issue.Number,
		CommentID:     event.Comment.ID,
		HTMLURL:       event.Issue.HTMLURL,
	}
	if event.Issue.PullRequest != nil {
		ref.Kind = ReferencePullRequest
		ref.PullRequestNumber = event.Issue.Number
		if event.Issue.PullRequest.HTMLURL != "" {
			ref.HTMLURL = event.Issue.PullRequest.HTMLURL
		}
	} else {
		ref.Kind = ReferenceIssue
	}
	if err := ref.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid issue_comment event: %w", err)
	}
	return Snapshot{
		Reference:   ref,
		Action:      event.Action,
		Title:       event.Issue.Title,
		Name:        "issue comment",
		CommentBody: event.Comment.Body,
	}, nil
}

type reviewCommentEvent struct {
	Action      string             `json:"action"`
	Repository  repositoryEnvelope `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	} `json:"comment"`
}

func parseReviewCommentEvent(data []byte) (Snapshot, error) {
	var event reviewCommentEvent
	if err := decodeExactJSON(data, &event); err != nil {
		return Snapshot{}, err
	}
	if event.Comment.ID <= 0 {
		return Snapshot{}, fmt.Errorf("invalid pull_request_review_comment event: comment id is required")
	}
	ref := Reference{
		SchemaVersion:     1,
		Repository:        event.Repository.FullName,
		Kind:              ReferencePullRequest,
		PullRequestNumber: event.PullRequest.Number,
		IssueNumber:       event.PullRequest.Number,
		CommentID:         event.Comment.ID,
		HeadSHA:           event.PullRequest.Head.SHA,
		HTMLURL:           event.PullRequest.HTMLURL,
	}
	if err := ref.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid pull_request_review_comment event: %w", err)
	}
	return Snapshot{
		Reference:   ref,
		Action:      event.Action,
		Title:       event.PullRequest.Title,
		Name:        "review comment",
		CommentBody: event.Comment.Body,
	}, nil
}

type pushEvent struct {
	Ref        string             `json:"ref"`
	Before     string             `json:"before"`
	After      string             `json:"after"`
	Repository repositoryEnvelope `json:"repository"`
	HeadCommit struct {
		ID string `json:"id"`
	} `json:"head_commit"`
}

func parsePushEvent(data []byte) (Snapshot, error) {
	var event pushEvent
	if err := decodeExactJSON(data, &event); err != nil {
		return Snapshot{}, err
	}
	head := strings.TrimSpace(event.After)
	if head == "" {
		head = strings.TrimSpace(event.HeadCommit.ID)
	}
	ref := Reference{
		SchemaVersion: 1,
		Repository:    event.Repository.FullName,
		Kind:          ReferencePush,
		HeadSHA:       head,
		BeforeSHA:     strings.TrimSpace(event.Before),
		Ref:           event.Ref,
	}
	if err := ref.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid push event: %w", err)
	}
	return Snapshot{Reference: ref, Name: "push", Action: "push"}, nil
}

type releaseEvent struct {
	Action     string             `json:"action"`
	Repository repositoryEnvelope `json:"repository"`
	Release    struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	} `json:"release"`
}

func parseReleaseEvent(data []byte) (Snapshot, error) {
	var event releaseEvent
	if err := decodeExactJSON(data, &event); err != nil {
		return Snapshot{}, err
	}
	ref := Reference{
		SchemaVersion: 1,
		Repository:    event.Repository.FullName,
		Kind:          ReferenceRelease,
		TagName:       event.Release.TagName,
		HTMLURL:       event.Release.HTMLURL,
	}
	if err := ref.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid release event: %w", err)
	}
	return Snapshot{Reference: ref, Action: event.Action, Title: event.Release.TagName, Name: "release"}, nil
}

type workflowDispatchEvent struct {
	Inputs     map[string]any     `json:"inputs"`
	Repository repositoryEnvelope `json:"repository"`
}

func parseWorkflowDispatchEvent(data []byte) (Snapshot, error) {
	var event workflowDispatchEvent
	if err := decodeExactJSON(data, &event); err != nil {
		return Snapshot{}, err
	}
	ref := Reference{
		SchemaVersion: 1,
		Repository:    event.Repository.FullName,
		Kind:          ReferenceWorkflowDispatch,
	}
	if event.Inputs != nil {
		if raw, ok := event.Inputs["pr_number"]; ok && raw != nil {
			number, err := parseDispatchPRNumber(raw)
			if err != nil {
				return Snapshot{}, fmt.Errorf("invalid workflow_dispatch event: %w", err)
			}
			ref.PullRequestNumber = number
		}
	}
	if err := ref.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("invalid workflow_dispatch event: %w", err)
	}
	return Snapshot{Reference: ref, Name: "workflow dispatch", Action: "workflow_dispatch"}, nil
}

func parseDispatchPRNumber(raw any) (int, error) {
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, fmt.Errorf("pr_number is empty")
		}
		var number int
		if _, err := fmt.Sscanf(trimmed, "%d", &number); err != nil || number <= 0 || trimmed != fmt.Sprintf("%d", number) {
			return 0, fmt.Errorf("pr_number must be a positive integer")
		}
		return number, nil
	case float64:
		number := int(value)
		if float64(number) != value || number <= 0 {
			return 0, fmt.Errorf("pr_number must be a positive integer")
		}
		return number, nil
	default:
		return 0, fmt.Errorf("pr_number must be a positive integer")
	}
}

func decodeExactJSON(data []byte, target any) error {
	if len(data) == 0 {
		return fmt.Errorf("github event JSON is empty")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode github event JSON: %w", err)
	}
	return nil
}

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
	default:
		return Snapshot{}, fmt.Errorf("unsupported github event %q", eventName)
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

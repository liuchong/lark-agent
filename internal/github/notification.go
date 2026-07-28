package github

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Notification struct {
	MessageType string `json:"message_type"`
	Content     string `json:"content"`
}

// RenderNotification creates a deterministic Lark post. Every GitHub string is
// serialized as text data, never interpreted as a command.
func RenderNotification(snapshot Snapshot, signingKey string) (Notification, error) {
	if err := snapshot.Reference.Validate(); err != nil {
		return Notification{}, err
	}
	marker, err := EncodeReferenceMarker(snapshot.Reference, signingKey)
	if err != nil {
		return Notification{}, err
	}
	status := firstNonEmpty(snapshot.Conclusion, snapshot.Status, snapshot.Action, "unknown")
	title := fmt.Sprintf("GitHub %s: %s", snapshot.Reference.Repository, status)
	lines := []string{
		fmt.Sprintf("Workflow: %s", firstNonEmpty(snapshot.Name, snapshot.Title, string(snapshot.Reference.Kind))),
		fmt.Sprintf("Status: %s", status),
	}
	if snapshot.Reference.PullRequestNumber > 0 {
		lines = append(lines, fmt.Sprintf("PR: #%d", snapshot.Reference.PullRequestNumber))
	}
	if snapshot.Reference.HeadSHA != "" {
		lines = append(lines, "Commit: "+shortSHA(snapshot.Reference.HeadSHA))
	}
	for _, job := range snapshot.FailedJobs {
		lines = append(lines, fmt.Sprintf("Failed: %s (%s)", job.Name, firstNonEmpty(job.Conclusion, job.Status, "unknown")))
		for _, step := range job.Steps {
			if step.Conclusion == "failure" || step.Conclusion == "cancelled" || step.Conclusion == "timed_out" {
				lines = append(lines, fmt.Sprintf("  Step: %s (%s)", step.Name, step.Conclusion))
			}
		}
	}
	for _, annotation := range snapshot.Annotations {
		lines = append(lines, fmt.Sprintf(
			"Annotation: %s:%d %s",
			annotation.Path,
			annotation.StartLine,
			firstNonEmpty(annotation.Message, annotation.Title, annotation.AnnotationLevel),
		))
	}
	if snapshot.Omitted.Any() {
		lines = append(lines, "Details omitted: "+formatOmitted(snapshot.Omitted))
	}
	if snapshot.Partial {
		lines = append(lines, "Details: partial; some GitHub data was unavailable")
	}
	if snapshot.Reference.HTMLURL != "" {
		lines = append(lines, "Link: "+snapshot.Reference.HTMLURL)
	}
	lines = append(lines, marker)

	content := make([][]map[string]string, 0, len(lines))
	for _, line := range lines {
		content = append(content, []map[string]string{{"tag": "text", "text": line}})
	}
	post := map[string]any{
		"en_us": map[string]any{
			"title":   title,
			"content": content,
		},
	}
	data, err := json.Marshal(post)
	if err != nil {
		return Notification{}, fmt.Errorf("encode lark post: %w", err)
	}
	return Notification{MessageType: "post", Content: string(data)}, nil
}

func formatOmitted(omitted OmittedCounts) string {
	var fields []string
	for _, item := range []struct {
		name  string
		count int
	}{
		{name: "jobs", count: omitted.Jobs},
		{name: "files", count: omitted.Files},
		{name: "patch_bytes", count: omitted.PatchBytes},
		{name: "annotations", count: omitted.Annotations},
		{name: "reviews", count: omitted.Reviews},
	} {
		if item.count > 0 {
			fields = append(fields, fmt.Sprintf("%s=%d", item.name, item.count))
		}
	}
	return strings.Join(fields, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

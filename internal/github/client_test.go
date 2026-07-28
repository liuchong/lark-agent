package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientFetchContextUsesReferenceBoundPathsAndLimits(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/example/widgets/actions/runs/981":
			_, _ = w.Write([]byte(`{"id":981,"run_attempt":2,"name":"verify","status":"completed","conclusion":"failure","head_sha":"0123456789abcdef0123456789abcdef01234567","html_url":"https://github.example/run/981"}`))
		case "/repos/example/widgets/actions/runs/981/jobs":
			_, _ = w.Write([]byte(`{"total_count":2,"jobs":[{"id":1,"name":"unit","status":"completed","conclusion":"failure","html_url":"https://github.example/job/1","steps":[{"name":"test","status":"completed","conclusion":"failure"}]},{"id":2,"name":"lint","status":"completed","conclusion":"failure"}]}`))
		case "/repos/example/widgets/pulls/42/files":
			_, _ = w.Write([]byte(`[{"filename":"a.go","status":"modified","additions":4,"deletions":1,"changes":5,"patch":"line 1\nline 2"},{"filename":"b.go","status":"added","additions":3,"deletions":0,"changes":3,"patch":"line 3"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL: server.URL,
		Token:   "synthetic-token",
		Limits: Limits{
			MaxFiles:       1,
			MaxPatchBytes:  8,
			MaxAnnotations: 2,
			MaxReviews:     2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{
		SchemaVersion:      1,
		Repository:         "example/widgets",
		Kind:               ReferenceWorkflowRun,
		WorkflowRunID:      981,
		WorkflowRunAttempt: 2,
		PullRequestNumber:  42,
	}
	result, err := client.FetchContext(context.Background(), ref, []Section{SectionSummary, SectionChecks, SectionFiles})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 2 ||
		len(result.Files) != 1 ||
		!result.Truncated.Files ||
		!result.Truncated.Patches ||
		result.Omitted.Files != 1 ||
		result.Omitted.PatchBytes == 0 {
		t.Fatalf("result=%+v", result)
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/repos/example/widgets/") {
			t.Fatalf("unbound request path=%q", path)
		}
	}
}

func TestClientRejectsIncompleteSuccessfulJobsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/jobs") {
			_, _ = w.Write([]byte(`{"total_count":1}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{
		BaseURL: server.URL,
		Token:   "synthetic-token",
		Limits:  DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.FetchContext(context.Background(), Reference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          ReferenceWorkflowRun,
		WorkflowRunID: 981,
	}, []Section{SectionChecks})
	failure, ok := FailureOf(err)
	if !ok || failure.Kind != FailureInvalidData {
		t.Fatalf("failure=%+v ok=%v err=%v", failure, ok, err)
	}
}

func TestClientPreservesGitHubEnterpriseAPIPathPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/example/widgets/actions/runs/981" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":981,"name":"verify","status":"completed","conclusion":"success"}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{
		BaseURL: server.URL + "/api/v3",
		Token:   "synthetic-token",
		Limits:  DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.FetchContext(context.Background(), Reference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          ReferenceWorkflowRun,
		WorkflowRunID: 981,
	}, []Section{SectionSummary})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "verify" || result.Conclusion != "success" {
		t.Fatalf("result=%+v", result)
	}
}

func TestClientReturnsTypedTruthfulFailures(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		statusCode int
		want       FailureKind
	}{
		{name: "not found", statusCode: http.StatusNotFound, want: FailureNotFound},
		{name: "forbidden", statusCode: http.StatusForbidden, want: FailureForbidden},
		{name: "rate limit", statusCode: http.StatusTooManyRequests, want: FailureRateLimited},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.statusCode)
				_, _ = w.Write([]byte(`{"message":"synthetic failure"}`))
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, Token: "synthetic-token", Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.FetchContext(context.Background(), Reference{
				SchemaVersion: 1,
				Repository:    "example/widgets",
				Kind:          ReferenceWorkflowRun,
				WorkflowRunID: 981,
			}, []Section{SectionSummary})
			failure, ok := FailureOf(err)
			if !ok || failure.Kind != testCase.want {
				t.Fatalf("failure=%+v ok=%v err=%v", failure, ok, err)
			}
		})
	}
}

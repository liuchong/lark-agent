package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

type fakeGitHubContextFetcher struct {
	called    bool
	reference internalgithub.Reference
	sections  []internalgithub.Section
}

func (f *fakeGitHubContextFetcher) FetchContext(
	_ context.Context,
	ref internalgithub.Reference,
	sections []internalgithub.Section,
) (internalgithub.ContextResult, error) {
	f.called = true
	f.reference = ref
	f.sections = sections
	return internalgithub.ContextResult{
		Reference: ref,
		Jobs: []internalgithub.JobSummary{{
			Name: "unit", Conclusion: "failure",
		}},
	}, nil
}

func TestGitHubContextToolUsesInvocationReferenceNotModelRepository(t *testing.T) {
	fetcher := &fakeGitHubContextFetcher{}
	registry, err := NewRegistry(GitHubContextDefinition(fetcher))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.GitHubReference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          domain.GitHubReferenceWorkflowRun,
		WorkflowRunID: 981,
	}
	scope := InvocationScope{
		Owner:           false,
		ReadOnly:        true,
		ChatID:          "oc_synthetic",
		GitHubReference: &ref,
	}
	ctx := WithInvocationScope(context.Background(), scope)
	if _, err := registry.Execute(ctx, "get_github_context", json.RawMessage(
		`{"sections":["checks"],"repository":"attacker/override","workflow_run_id":999}`,
	)); err == nil {
		t.Fatal("model-supplied repository override was accepted")
	}
	if fetcher.called {
		t.Fatal("fetcher called for rejected repository override")
	}
	execution, err := registry.Execute(ctx, "get_github_context", json.RawMessage(
		`{"sections":["checks"]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !fetcher.called || fetcher.reference.Repository != "example/widgets" || fetcher.reference.WorkflowRunID != 981 {
		t.Fatalf("fetcher=%+v", fetcher)
	}
	if !strings.Contains(execution.Content, `"unit"`) ||
		len(execution.Sources) != 1 ||
		execution.Sources[0].Kind != "github" {
		t.Fatalf("execution=%+v", execution)
	}
}

func TestGitHubContextToolHiddenAndDeniedWithoutTrustedReference(t *testing.T) {
	fetcher := &fakeGitHubContextFetcher{}
	registry, err := NewRegistry(GitHubContextDefinition(fetcher))
	if err != nil {
		t.Fatal(err)
	}
	scope := InvocationScope{Owner: true, ChatID: "oc_synthetic"}
	if infos := registry.InfosFor(scope); len(infos) != 0 {
		t.Fatalf("tool visible without reference: %+v", infos)
	}
	ctx := WithInvocationScope(context.Background(), scope)
	if _, err := registry.Execute(ctx, "get_github_context", json.RawMessage(`{"sections":["summary"]}`)); err == nil {
		t.Fatal("tool executed without reference")
	}
	if fetcher.called {
		t.Fatal("fetcher called without reference")
	}
}

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

func TestGetGitHubFileRejectsParentPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("SC-58 HTTP was sent")
	}))
	t.Cleanup(server.Close)
	client, err := internalgithub.NewClient(internalgithub.ClientConfig{
		BaseURL: server.URL, Token: "synthetic-token", Limits: internalgithub.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(GitHubFileDefinition(client))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.GitHubReference{
		SchemaVersion:     1,
		Repository:        "example/widgets",
		Kind:              domain.GitHubReferencePullRequest,
		PullRequestNumber: 12,
		HeadSHA:           "0123456789abcdef0123456789abcdef01234567",
	}
	ctx := WithInvocationScope(context.Background(), InvocationScope{Owner: true, GitHubReference: &ref})
	if _, err := registry.Execute(ctx, "get_github_file", json.RawMessage(`{"path":"../secret"}`)); err == nil {
		t.Fatal("SC-58 expected denial")
	}
}

func TestGetGitHubContextRejectsUnsupportedSection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("SC-80 HTTP was sent")
	}))
	t.Cleanup(server.Close)
	client, err := internalgithub.NewClient(internalgithub.ClientConfig{
		BaseURL: server.URL, Token: "synthetic-token", Limits: internalgithub.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(GitHubContextDefinition(client))
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.GitHubReference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          domain.GitHubReferenceIssue,
		IssueNumber:   7,
	}
	ctx := WithInvocationScope(context.Background(), InvocationScope{Owner: true, GitHubReference: &ref})
	if _, err := registry.Execute(ctx, "get_github_context", json.RawMessage(`{"sections":["nope"]}`)); err == nil ||
		!strings.Contains(err.Error(), "unsupported github section") {
		t.Fatalf("SC-80 err=%v", err)
	}
}

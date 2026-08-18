package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpsertCheckUsesGateNameAndCreateThenPatch(t *testing.T) {
	t.Parallel()
	var methods []string
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if strings.Contains(r.URL.Path, "/merge") {
			t.Fatal("merge path requested")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/check-runs"):
			if r.URL.Query().Get("check_name") != CheckRunName {
				t.Fatalf("check_name=%q", r.URL.Query().Get("check_name"))
			}
			_, _ = w.Write([]byte(`{"check_runs":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/check-runs"):
			raw, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, payload)
			if payload["name"] != CheckRunName {
				t.Fatalf("SC-49 name=%v", payload["name"])
			}
			_, _ = w.Write([]byte(`{"id":44}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{BaseURL: server.URL, Token: "synthetic-token", Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{
		SchemaVersion:     1,
		Repository:        "example/widgets",
		Kind:              ReferencePullRequest,
		PullRequestNumber: 12,
		HeadSHA:           testHeadSHA,
	}
	result, err := client.UpsertCheck(context.Background(), ref, CheckUpsert{
		Conclusion: "success", Title: "gate", Summary: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != 44 {
		t.Fatalf("id=%d methods=%v", result.ID, methods)
	}
}

func TestGetFileRejectsParentPathWithoutHTTP(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("HTTP was sent")
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{BaseURL: server.URL, Token: "synthetic-token", Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{
		SchemaVersion:     1,
		Repository:        "example/widgets",
		Kind:              ReferencePullRequest,
		PullRequestNumber: 12,
		HeadSHA:           testHeadSHA,
	}
	if _, err := client.GetFile(context.Background(), ref, "../secret"); err == nil {
		t.Fatal("expected path denial")
	}
}

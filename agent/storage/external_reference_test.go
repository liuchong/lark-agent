package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestExternalReferencePersistsIdempotentlyAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenInspection(path)
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.ExternalReference{
		Provider:      "github",
		Kind:          string(domain.GitHubReferenceWorkflowRun),
		ExternalKey:   "example/widgets:workflow_run:981:2",
		LarkMessageID: "om_notification",
		ChatID:        "oc_synthetic",
		SenderAppID:   "cli_current",
		Reference: domain.GitHubReference{
			SchemaVersion:      1,
			Repository:         "example/widgets",
			Kind:               domain.GitHubReferenceWorkflowRun,
			WorkflowRunID:      981,
			WorkflowRunAttempt: 2,
		},
		VerifiedAt: time.Now().UTC(),
	}
	first, err := store.UpsertExternalReference(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertExternalReference(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ReferenceDigest == "" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenInspection(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	got, ok, err := store.GetExternalReference(context.Background(), "github", "om_notification")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Reference != ref.Reference || got.ChatID != ref.ChatID || got.SenderAppID != ref.SenderAppID {
		t.Fatalf("got=%+v", got)
	}
}

func TestExternalReferenceConflictFailsClosed(t *testing.T) {
	store, err := OpenInspection(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	ref := domain.ExternalReference{
		Provider: "github", Kind: string(domain.GitHubReferenceWorkflowRun),
		ExternalKey: "example/widgets:workflow_run:981:1", LarkMessageID: "om_notification",
		ChatID: "oc_synthetic", SenderAppID: "cli_current",
		Reference: domain.GitHubReference{
			SchemaVersion: 1, Repository: "example/widgets",
			Kind: domain.GitHubReferenceWorkflowRun, WorkflowRunID: 981,
		},
	}
	if _, err := store.UpsertExternalReference(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	ref.Reference.WorkflowRunID = 982
	ref.ExternalKey = "example/widgets:workflow_run:982:1"
	if _, err := store.UpsertExternalReference(context.Background(), ref); err == nil {
		t.Fatal("conflicting reference was accepted")
	}
}

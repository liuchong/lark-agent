package larkagent_test

import (
	"context"
	"path/filepath"
	"testing"

	agentmemory "github.com/liuchong/lark-agent/agent/memory"
	"github.com/liuchong/lark-agent/agent/storage"
)

func TestConfirmedMemoryAndFeedbackPersistAcrossRestart(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := store.AddMemory(ctx, agentmemory.Record{
		Kind:       agentmemory.KindProject,
		Scope:      "global",
		Status:     agentmemory.StatusConfirmed,
		Text:       "Sample service quota fix is merged to a test branch",
		Confidence: 0.96,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMemory(ctx, agentmemory.Record{
		Kind:       agentmemory.KindFact,
		Scope:      "global",
		Status:     agentmemory.StatusCandidate,
		Text:       "unconfirmed model guess about sample quota",
		Confidence: 0.99,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMemory(ctx, agentmemory.Record{
		Kind:       agentmemory.KindFact,
		Scope:      "other-project",
		Status:     agentmemory.StatusConfirmed,
		Text:       "sample quota belongs to another project",
		Confidence: 0.99,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordMemoryFeedback(ctx, agentmemory.Feedback{
		MemoryEntryID:   confirmed.ID,
		Verdict:         agentmemory.FeedbackHelpful,
		Note:            "This avoided repeating an obsolete investigation.",
		SourceMessageID: "om_feedback_1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	records, err := store.SearchMemories(ctx, agentmemory.Query{
		Text:          "sample quota",
		Scopes:        []string{"global"},
		Status:        agentmemory.StatusConfirmed,
		MinConfidence: 0.8,
		Limit:         8,
		MaxBytes:      8 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != confirmed.ID {
		t.Fatalf("records=%+v", records)
	}
	feedback, err := store.ListMemoryFeedback(ctx, confirmed.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(feedback) != 1 || feedback[0].Verdict != agentmemory.FeedbackHelpful {
		t.Fatalf("feedback=%+v", feedback)
	}
	if deleted, err := store.DeleteMemory(ctx, confirmed.ID); err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	records, err = store.SearchMemories(ctx, agentmemory.Query{
		Text:     "sample quota",
		Scopes:   []string{"global"},
		Status:   agentmemory.StatusConfirmed,
		Limit:    8,
		MaxBytes: 8 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("deleted memory leaked into retrieval: %+v", records)
	}
}

func TestMemoryRejectsCredentialLikeContent(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	for _, content := range []string{
		"Authorization: Bearer secret-token",
		"sk-proj-1234567890abcdefghijklmnop",
		"ghp_1234567890abcdefghijklmnopqrstuv",
		"AKIA1234567890ABCDEF",
		"password: correct-horse-battery-staple",
	} {
		if _, err := store.AddMemory(context.Background(), agentmemory.Record{
			Kind:       agentmemory.KindFact,
			Scope:      "global",
			Status:     agentmemory.StatusConfirmed,
			Text:       content,
			Confidence: 1,
		}); err == nil {
			t.Fatalf("expected credential-like memory to be rejected: %q", content)
		}
	}
}

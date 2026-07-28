package context

import (
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
	internalgithub "github.com/liuchong/lark-agent/internal/github"
)

func TestResolveGitHubReferenceTrustsOnlyCurrentAppDirectRelation(t *testing.T) {
	signingKey := "synthetic-signing-key"
	ref := domain.GitHubReference{
		SchemaVersion:      1,
		Repository:         "example/widgets",
		Kind:               domain.GitHubReferenceWorkflowRun,
		WorkflowRunID:      981,
		WorkflowRunAttempt: 2,
	}
	marker, err := internalgithub.EncodeReferenceMarker(ref, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	target := domain.NormalizedEvent{
		MessageID:        "om_question",
		ChatID:           "oc_synthetic",
		ReplyToMessageID: "om_notification",
	}
	conversation := []domain.NormalizedEvent{
		{
			MessageID:  "om_adjacent",
			ChatID:     "oc_synthetic",
			SenderID:   "cli_current",
			SenderType: "app",
			Content:    marker,
		},
		{
			MessageID:  "om_notification",
			ChatID:     "oc_synthetic",
			SenderID:   "cli_current",
			SenderType: "app",
			Content:    marker,
		},
		target,
	}
	got, ok, err := ResolveGitHubReference(
		target,
		conversation,
		"cli_current",
		[]string{"example/widgets"},
		signingKey,
	)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Reference != ref || got.LarkMessageID != "om_notification" || got.ChatID != "oc_synthetic" {
		t.Fatalf("reference=%+v", got)
	}
}

func TestResolveGitHubReferenceRejectsSpoofAndAdjacentMarker(t *testing.T) {
	signingKey := "synthetic-signing-key"
	ref := domain.GitHubReference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          domain.GitHubReferenceWorkflowRun,
		WorkflowRunID: 981,
	}
	marker, err := internalgithub.EncodeReferenceMarker(ref, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		target       domain.NormalizedEvent
		conversation []domain.NormalizedEvent
	}{
		{
			name:   "human spoof",
			target: domain.NormalizedEvent{MessageID: "om_q", ChatID: "oc_synthetic", ReplyToMessageID: "om_spoof"},
			conversation: []domain.NormalizedEvent{{
				MessageID: "om_spoof", ChatID: "oc_synthetic", SenderID: "ou_human", SenderType: "user", Content: marker,
			}},
		},
		{
			name:   "other app spoof",
			target: domain.NormalizedEvent{MessageID: "om_q", ChatID: "oc_synthetic", ReplyToMessageID: "om_spoof"},
			conversation: []domain.NormalizedEvent{{
				MessageID: "om_spoof", ChatID: "oc_synthetic", SenderID: "cli_other", SenderType: "app", Content: marker,
			}},
		},
		{
			name:   "unreferenced adjacent app",
			target: domain.NormalizedEvent{MessageID: "om_q", ChatID: "oc_synthetic"},
			conversation: []domain.NormalizedEvent{{
				MessageID: "om_adjacent", ChatID: "oc_synthetic", SenderID: "cli_current", SenderType: "app", Content: marker,
			}},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, ok, err := ResolveGitHubReference(
				testCase.target,
				testCase.conversation,
				"cli_current",
				[]string{"example/widgets"},
				signingKey,
			)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("spoofed marker became trusted")
			}
		})
	}
}

func TestResolveGitHubReferenceRejectsMarkerCopiedThroughCurrentApp(t *testing.T) {
	ref := domain.GitHubReference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          domain.GitHubReferenceWorkflowRun,
		WorkflowRunID: 981,
	}
	forged, err := internalgithub.EncodeReferenceMarker(ref, "attacker-controlled-key")
	if err != nil {
		t.Fatal(err)
	}
	target := domain.NormalizedEvent{
		MessageID:        "om_q",
		ChatID:           "oc_synthetic",
		ReplyToMessageID: "om_copied",
	}
	_, ok, err := ResolveGitHubReference(
		target,
		[]domain.NormalizedEvent{{
			MessageID: "om_copied", ChatID: "oc_synthetic",
			SenderID: "cli_current", SenderType: "app", Content: forged,
		}},
		"cli_current",
		[]string{"example/widgets"},
		"real-lark-app-secret",
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("marker copied through current app became trusted without a valid signature")
	}
}

func TestResolveGitHubReferenceRejectsConflictingTrustedChain(t *testing.T) {
	signingKey := "synthetic-signing-key"
	first := domain.GitHubReference{
		SchemaVersion: 1,
		Repository:    "example/widgets",
		Kind:          domain.GitHubReferenceWorkflowRun,
		WorkflowRunID: 981,
	}
	second := first
	second.WorkflowRunID = 982
	firstMarker, _ := internalgithub.EncodeReferenceMarker(first, signingKey)
	secondMarker, _ := internalgithub.EncodeReferenceMarker(second, signingKey)
	target := domain.NormalizedEvent{
		MessageID:        "om_q",
		ChatID:           "oc_synthetic",
		ReplyToMessageID: "om_parent",
		RootMessageID:    "om_root",
	}
	_, ok, err := ResolveGitHubReference(target, []domain.NormalizedEvent{
		{MessageID: "om_parent", ChatID: "oc_synthetic", SenderID: "cli_current", SenderType: "app", Content: firstMarker},
		{MessageID: "om_root", ChatID: "oc_synthetic", SenderID: "cli_current", SenderType: "app", Content: secondMarker},
	}, "cli_current", []string{"example/widgets"}, signingKey)
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

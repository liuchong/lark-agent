package policy

import (
	"context"
	"testing"

	"github.com/liuchong/lark-agent/agent/domain"
)

type fakeThreadState struct {
	ownerReplied bool
	withdrawn    bool
}

func (f fakeThreadState) OwnerAlreadyReplied(context.Context, domain.WorkItem) (bool, error) {
	return f.ownerReplied, nil
}

func (f fakeThreadState) MessageWithdrawn(context.Context, domain.WorkItem) (bool, error) {
	return f.withdrawn, nil
}

func TestAutoReplyCancelsWhenOwnerAlreadyReplied(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, OwnerOpenID: "ou_owner"}, fakeThreadState{ownerReplied: true})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{SenderID: "ou_owner"},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.98,
		Risk:       domain.RiskLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionCancelled || action.CancelReason != "owner_already_replied" {
		t.Fatalf("action=%+v", action)
	}
}

func TestOwnerRequestDoesNotCancelBecauseOwnerSentPrompt(t *testing.T) {
	gate := NewReplyGate(Config{
		Mode: domain.ModeAuto, OwnerOpenID: "ou_owner",
	}, fakeThreadState{ownerReplied: true})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{SenderID: "ou_owner"},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceOwnerRequest,
		Confidence: 0.98,
		Risk:       domain.RiskLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
}

func TestApprovalModeRequiresApproval(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeApproval}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.98,
		Risk:       domain.RiskLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionAwaitingApproval {
		t.Fatalf("action=%+v", action)
	}
}

func TestLowRiskDirectMentionReplyUsesConfiguredConfidenceFloor(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, ReplyConfidenceMin: 0.85}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceDirectMention,
		Confidence: 0.72,
		Risk:       domain.RiskLow,
		ReplyText:  "收到，我先确认后同步。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionAwaitingApproval || action.CancelReason != "low_confidence" {
		t.Fatalf("action=%+v", action)
	}
}

func TestMediumRiskDirectMentionStillRequiresApprovalBelowConfidenceFloor(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, ReplyConfidenceMin: 0.85}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceDirectMention,
		Confidence: 0.72,
		Risk:       domain.RiskMedium,
		ReplyText:  "收到，我先确认后同步。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionAwaitingApproval || action.CancelReason != "low_confidence" {
		t.Fatalf("action=%+v", action)
	}
}

func TestHardRiskBlocksAutoReply(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskForbidden,
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionBlocked || action.CancelReason != "forbidden_risk" {
		t.Fatalf("action=%+v", action)
	}
}

func TestBlockedUserBlocksAtFinalGate(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, BlockUsers: []string{"ou_blocked"}}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{SenderID: "ou_blocked"},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionBlocked || action.CancelReason != "blocked_target" {
		t.Fatalf("action=%+v", action)
	}
}

func TestAssistantFacingReplyFromNonOwnerBlocksAtFinalGate(t *testing.T) {
	for _, relevance := range []domain.Relevance{
		domain.RelevanceAssistantRequest,
		domain.RelevanceOwnerRequest,
	} {
		gate := NewReplyGate(Config{
			Mode:        domain.ModeAuto,
			OwnerOpenID: "ou_owner",
		}, fakeThreadState{})
		action, err := gate.Prepare(context.Background(), domain.WorkItem{
			Event: domain.NormalizedEvent{SenderID: "ou_other"},
		}, domain.Decision{
			Kind:       domain.DecisionReply,
			Relevance:  relevance,
			Confidence: 1,
			Risk:       domain.RiskLow,
			ReplyText:  "不应发送",
		})
		if err != nil {
			t.Fatal(err)
		}
		if action.Status != domain.ActionBlocked ||
			action.CancelReason != "assistant_request_from_non_owner" {
			t.Fatalf("relevance=%s action=%+v", relevance, action)
		}
	}
}

func TestConfiguredGroupsScopeBlocksOutsideConfiguredGroup(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, ReplyScope: domain.ReplyScopeConfiguredGroups}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{MessageID: "om_1", InTestScope: false},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionBlocked || action.CancelReason != "outside_reply_scope" {
		t.Fatalf("action=%+v", action)
	}
}

func TestAllGroupsScopeAllowsOutsideConfiguredGroup(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, ReplyScope: domain.ReplyScopeAllGroups}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{MessageID: "om_all_groups", InTestScope: false},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceDirectMention,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
}

func TestConfiguredAssistantScopeBlocksOutsideConfiguredGroup(t *testing.T) {
	gate := NewReplyGate(Config{
		Mode:                domain.ModeAuto,
		OwnerOpenID:         "ou_owner",
		AssistantReplyScope: domain.ReplyScopeConfiguredGroups,
	}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{
			MessageID: "om_assistant", ChatType: "group", SenderID: "ou_owner",
			InTestScope: true, InAssistantScope: false,
		},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceAssistantRequest,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionBlocked || action.CancelReason != "outside_assistant_reply_scope" {
		t.Fatalf("action=%+v", action)
	}
}

func TestConfiguredAssistantScopeAllowsBotResolvedGroup(t *testing.T) {
	gate := NewReplyGate(Config{
		Mode:                domain.ModeAuto,
		OwnerOpenID:         "ou_owner",
		AssistantReplyScope: domain.ReplyScopeConfiguredGroups,
	}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{
			MessageID: "om_assistant", ChatType: "group", SenderID: "ou_owner",
			InTestScope: false, InAssistantScope: true,
		},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceAssistantRequest,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
}

func TestAllGroupsAssistantScopeAllowsOutsideConfiguredGroup(t *testing.T) {
	gate := NewReplyGate(Config{
		Mode:                domain.ModeAuto,
		OwnerOpenID:         "ou_owner",
		AssistantReplyScope: domain.ReplyScopeAllGroups,
	}, fakeThreadState{ownerReplied: true})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{
			MessageID: "om_assistant", ChatType: "group", SenderID: "ou_owner", InTestScope: false,
		},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceAssistantRequest,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
}

func TestOwnerRequestBypassesConfiguredGroupsReplyLimit(t *testing.T) {
	gate := NewReplyGate(Config{
		Mode:        domain.ModeAuto,
		OwnerOpenID: "ou_owner",
		ReplyScope:  domain.ReplyScopeConfiguredGroups,
	}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{
			MessageID: "om_owner_request", SenderID: "ou_owner", InTestScope: false,
		},
	}, domain.Decision{
		Kind:       domain.DecisionReply,
		Relevance:  domain.RelevanceOwnerRequest,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
		ReplyText:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady {
		t.Fatalf("action=%+v", action)
	}
}

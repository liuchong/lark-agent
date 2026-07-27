package policy

import (
	"context"
	"testing"
	"time"

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
	gate := NewReplyGate(Config{Mode: domain.ModeAuto}, fakeThreadState{ownerReplied: true})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
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
	gate := NewReplyGate(Config{Mode: domain.ModeAuto}, fakeThreadState{ownerReplied: true})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
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

func TestLowRiskDirectMentionReplyUsesLowerConfidenceFloor(t *testing.T) {
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
	if action.Status != domain.ActionReady {
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

func TestOwnerRequestBypassesConfiguredGroupsReplyLimit(t *testing.T) {
	gate := NewReplyGate(Config{Mode: domain.ModeAuto, ReplyScope: domain.ReplyScopeConfiguredGroups}, fakeThreadState{})
	action, err := gate.Prepare(context.Background(), domain.WorkItem{
		Event: domain.NormalizedEvent{MessageID: "om_owner_request", InTestScope: false},
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

func TestOwnerWaitRunsBeforeFinalStateCheck(t *testing.T) {
	waited := false
	state := &fakeThreadState{}
	gate := NewReplyGate(Config{
		Mode:      domain.ModeAuto,
		OwnerWait: time.Second,
		Sleeper: func(context.Context, time.Duration) error {
			waited = true
			state.ownerReplied = true
			return nil
		},
	}, state)
	action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
		Kind:       domain.DecisionReply,
		Confidence: 0.99,
		Risk:       domain.RiskLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !waited || action.Status != domain.ActionCancelled || action.CancelReason != "owner_already_replied" {
		t.Fatalf("waited=%v action=%+v", waited, action)
	}
}

func TestInteractiveOwnerRequestsDoNotWait(t *testing.T) {
	for _, kind := range []domain.WorkKind{domain.WorkKindFastPath, domain.WorkKindSimpleQuestion} {
		waited := false
		gate := NewReplyGate(Config{
			Mode:      domain.ModeAuto,
			OwnerWait: time.Minute,
			Sleeper: func(context.Context, time.Duration) error {
				waited = true
				return nil
			},
		}, fakeThreadState{})
		action, err := gate.Prepare(context.Background(), domain.WorkItem{}, domain.Decision{
			Kind:       domain.DecisionReply,
			Relevance:  domain.RelevanceOwnerRequest,
			WorkKind:   kind,
			Confidence: 1,
			Risk:       domain.RiskLow,
			ReplyText:  "reply",
		})
		if err != nil {
			t.Fatal(err)
		}
		if waited || action.Status != domain.ActionReady {
			t.Fatalf("kind=%s waited=%v action=%+v", kind, waited, action)
		}
	}
}

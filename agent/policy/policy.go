// Package policy gates side-effecting actions proposed by the agent.
package policy

import (
	"context"

	"github.com/liuchong/lark-agent/agent/domain"
)

// ThreadState checks live message state before an owner reply is sent.
type ThreadState interface {
	OwnerAlreadyReplied(context.Context, domain.WorkItem) (bool, error)
	MessageWithdrawn(context.Context, domain.WorkItem) (bool, error)
}

// Config controls reply gates.
type Config struct {
	Mode                domain.Mode
	OwnerOpenID         string
	ReplyScope          domain.ReplyScope
	AssistantReplyScope domain.ReplyScope
	ReplyConfidenceMin  float64
	BlockChats          []string
	BlockUsers          []string
}

// ReplyGate prepares a reply action or blocks/cancels it.
type ReplyGate struct {
	cfg   Config
	state ThreadState
}

// NewReplyGate creates a gate for reply actions.
func NewReplyGate(cfg Config, state ThreadState) *ReplyGate {
	if cfg.Mode == "" {
		cfg.Mode = domain.ModeAuto
	}
	if cfg.ReplyScope == "" {
		cfg.ReplyScope = domain.ReplyScopeAllGroups
	}
	if cfg.AssistantReplyScope == "" {
		cfg.AssistantReplyScope = domain.ReplyScopeAllGroups
	}
	if cfg.ReplyConfidenceMin == 0 {
		cfg.ReplyConfidenceMin = 0.70
	}
	return &ReplyGate{cfg: cfg, state: state}
}

// Prepare applies hard policy gates before sending.
func (g *ReplyGate) Prepare(ctx context.Context, item domain.WorkItem, decision domain.Decision) (domain.Action, error) {
	action := domain.Action{Kind: "reply_message", Status: domain.ActionReady}
	if decision.Kind != domain.DecisionReply {
		action.Status = domain.ActionCancelled
		action.CancelReason = "not_reply_decision"
		return action, nil
	}
	if g.cfg.Mode == domain.ModePaused {
		action.Status = domain.ActionCancelled
		action.CancelReason = "agent_paused"
		return action, nil
	}
	if isAssistantFacingRequest(decision.Relevance) &&
		(g.cfg.OwnerOpenID == "" || item.Event.SenderID != g.cfg.OwnerOpenID) {
		action.Status = domain.ActionBlocked
		action.CancelReason = "assistant_request_from_non_owner"
		return action, nil
	}
	if decision.Risk == domain.RiskForbidden {
		action.Status = domain.ActionBlocked
		action.CancelReason = "forbidden_risk"
		return action, nil
	}
	if contains(g.cfg.BlockChats, item.Event.ChatID) || contains(g.cfg.BlockUsers, item.Event.SenderID) {
		action.Status = domain.ActionBlocked
		action.CancelReason = "blocked_target"
		return action, nil
	}
	if g.cfg.ReplyScope == domain.ReplyScopeConfiguredGroups &&
		!item.Event.InTestScope &&
		decision.Relevance != domain.RelevanceOwnerRequest &&
		decision.Relevance != domain.RelevanceAssistantRequest {
		action.Status = domain.ActionBlocked
		action.CancelReason = "outside_reply_scope"
		return action, nil
	}
	if g.cfg.AssistantReplyScope == domain.ReplyScopeConfiguredGroups &&
		!item.Event.InAssistantScope &&
		decision.Relevance == domain.RelevanceAssistantRequest {
		action.Status = domain.ActionBlocked
		action.CancelReason = "outside_assistant_reply_scope"
		return action, nil
	}
	if g.state != nil {
		withdrawn, err := g.state.MessageWithdrawn(ctx, item)
		if err != nil {
			return action, err
		}
		if withdrawn {
			action.Status = domain.ActionCancelled
			action.CancelReason = "message_withdrawn"
			return action, nil
		}
		if !isAssistantFacingRequest(decision.Relevance) {
			replied, err := g.state.OwnerAlreadyReplied(ctx, item)
			if err != nil {
				return action, err
			}
			if replied {
				action.Status = domain.ActionCancelled
				action.CancelReason = "owner_already_replied"
				return action, nil
			}
		}
	}
	if decision.Risk == domain.RiskMedium || decision.Risk == domain.RiskHigh {
		action.Status = domain.ActionAwaitingApproval
		action.CancelReason = "risk_requires_approval"
		return action, nil
	}
	if decision.Confidence < g.replyConfidenceMin(decision) {
		action.Status = domain.ActionAwaitingApproval
		action.CancelReason = "low_confidence"
		return action, nil
	}
	if g.cfg.Mode == domain.ModeApproval {
		action.Status = domain.ActionAwaitingApproval
		return action, nil
	}
	action.Status = domain.ActionReady
	action.Idempotency = domain.DedupKey(item.Event) + ":reply"
	return action, nil
}

// RequiresApproval reports deterministic approval holds that can be known
// before the final live thread-state checks run.
func (g *ReplyGate) RequiresApproval(decision domain.Decision) bool {
	return decision.Kind == domain.DecisionReply &&
		(decision.Risk == domain.RiskMedium ||
			decision.Risk == domain.RiskHigh ||
			decision.Confidence < g.replyConfidenceMin(decision) ||
			g.cfg.Mode == domain.ModeApproval)
}

func (g *ReplyGate) replyConfidenceMin(decision domain.Decision) float64 {
	return g.cfg.ReplyConfidenceMin
}

func isAssistantFacingRequest(relevance domain.Relevance) bool {
	return relevance == domain.RelevanceOwnerRequest ||
		relevance == domain.RelevanceAssistantRequest
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

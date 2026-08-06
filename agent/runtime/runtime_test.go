package runtime

import (
	"context"
	"strings"
	"testing"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	"github.com/liuchong/lark-agent/agent/domain"
)

func TestConfigRequiresModelAndKey(t *testing.T) {
	if _, err := NewEinoRunner(context.Background(), EinoConfig{Model: "gpt-test"}); err == nil {
		t.Fatal("NewEinoRunner accepted missing API key")
	}
	if _, err := NewEinoRunner(context.Background(), EinoConfig{APIKey: "key"}); err == nil {
		t.Fatal("NewEinoRunner accepted missing model")
	}
}

func TestConfigDefaultsMaxIterations(t *testing.T) {
	cfg := EinoConfig{APIKey: "key", Model: "gpt-test"}
	cfg = cfg.withDefaults()
	if cfg.MaxIterations != 8 {
		t.Fatalf("MaxIterations=%d", cfg.MaxIterations)
	}
}

func TestParseDecisionJSON(t *testing.T) {
	decision, err := ParseDecision(`{
		"decision":"reply",
		"relevance_confidence":0.91,
		"reply_confidence":0.93,
		"risk":"low",
		"evidence_status":"insufficient",
		"reply_outcome":"partial",
		"progress":{
			"completed_checks":["读取消息入口"],
			"initial_finding":"当前只能确认入口存在",
			"unknowns":["生产配置是否启用"],
			"next_step":"提供生产配置路径"
		},
		"reply_text":"我来跟进",
		"owner_action":"确认后端通知契约",
		"reason":"direct mention"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionReply || decision.Confidence != 0.93 ||
		decision.EvidenceStatus != domain.EvidenceInsufficient ||
		decision.ReplyOutcome != domain.ReplyOutcomePartial ||
		len(decision.Progress.CompletedChecks) != 1 ||
		decision.Progress.InitialFinding != "当前只能确认入口存在" ||
		len(decision.Progress.Unknowns) != 1 ||
		decision.Progress.NextStep != "提供生产配置路径" ||
		decision.ReplyText != "我来跟进" || decision.OwnerAction != "确认后端通知契约" {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestParseDecisionDefaultsLegacyReplyToCompleteOutcome(t *testing.T) {
	decision, err := ParseDecision(`{
		"decision":"reply",
		"relevance_confidence":0.91,
		"reply_confidence":0.93,
		"risk":"low",
		"evidence_status":"verified",
		"reply_text":"结论明确",
		"reason":"source backed"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.ReplyOutcome != domain.ReplyOutcomeComplete {
		t.Fatalf("outcome=%q", decision.ReplyOutcome)
	}
}

func TestParseDecisionRejectsInvalidReplyOutcome(t *testing.T) {
	_, err := ParseDecision(`{
		"decision":"reply",
		"relevance_confidence":0.91,
		"reply_confidence":0.93,
		"risk":"low",
		"reply_outcome":"best_effort",
		"reply_text":"结论明确",
		"reason":"unsupported outcome"
	}`)
	if err == nil || !strings.Contains(err.Error(), "invalid reply_outcome") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseDecisionRejectsInvalidEvidenceStatus(t *testing.T) {
	_, err := ParseDecision(`{
		"decision":"reply",
		"relevance_confidence":0.91,
		"reply_confidence":0.93,
		"risk":"low",
		"evidence_status":"guessed",
		"reply_text":"猜测结论",
		"reason":"unsupported"
	}`)
	if err == nil || !strings.Contains(err.Error(), "invalid evidence_status") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseDecisionExtractsJSONFromModelPreamble(t *testing.T) {
	raw := "我会按下面的 JSON 决策执行：\n```json\n{\"decision\":\"record\",\"relevance_confidence\":0.75,\"risk\":\"low\",\"reason\":\"related\"}\n```"
	decision, err := ParseDecision(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionRecord || decision.Confidence != 0.75 {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestParseDecisionRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseDecision("not json"); err == nil {
		t.Fatal("ParseDecision accepted invalid JSON")
	}
	if _, err := ParseDecision(`{"decision":"reply","risk":"low","reply_confidence":0.9}`); err == nil {
		t.Fatal("ParseDecision accepted reply without reply_text")
	}
	if _, err := ParseDecision(`{"decision":"request_approval","risk":"high","reason":"commitment"}`); err == nil {
		t.Fatal("ParseDecision accepted request_approval without an exact reply_text")
	}
}

func TestParseDecisionRejectsReplyWithoutExplicitConfidence(t *testing.T) {
	_, err := ParseDecision(`{
		"decision":"reply",
		"relevance_confidence":0.98,
		"risk":"low",
		"reply_text":"结论明确且有源码依据。",
		"reason":"source backed"
	}`)
	if err == nil || !strings.Contains(err.Error(), "missing reply_confidence") {
		t.Fatalf("err=%v", err)
	}
}

type fakeQueryRunner struct {
	prompt string
}

func (r *fakeQueryRunner) Query(_ context.Context, prompt string) (Result, error) {
	r.prompt = prompt
	return Result{FinalText: `{"decision":"record","relevance_confidence":0.7,"risk":"low","reason":"related"}`}, nil
}

func TestDecisionAgentUsesPromptAndParsesDecision(t *testing.T) {
	runner := &fakeQueryRunner{}
	agent := DecisionAgent{Runner: runner}
	decision, err := agent.Decide(context.Background(), testBundle("phoenix status?"))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != domain.DecisionRecord {
		t.Fatalf("decision=%+v", decision)
	}
	if runner.prompt == "" || !strings.Contains(runner.prompt, "phoenix status?") {
		t.Fatalf("prompt=%q", runner.prompt)
	}
}

func testBundle(content string) agentcontext.Bundle {
	return agentcontext.Bundle{
		Event: domain.NormalizedEvent{
			MessageID: "om_1",
			Content:   content,
		},
		User: agentcontext.UserProfile{OpenID: "ou_owner"},
	}
}

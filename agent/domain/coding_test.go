package domain

import "testing"

func TestNewCodingGoalRequiresCompletionAndBlockingConditions(t *testing.T) {
	if _, err := NewCodingGoal(CodingGoalSpec{
		WorkItemID:            42,
		OriginalMessageID:     "om_goal",
		Question:              "持续跟进 示例客户端回调类型",
		CompletionConditions:  []string{"群里同步 示例客户端回调设计结论"},
		BlockingConditions:    []string{"缺少 owner 确认"},
		MaxInvestigationTurns: 12,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCodingGoal(CodingGoalSpec{
		WorkItemID:            42,
		OriginalMessageID:     "om_goal",
		Question:              "持续跟进 示例客户端回调类型",
		CompletionConditions:  []string{"群里同步 示例客户端回调设计结论"},
		MaxInvestigationTurns: 12,
	}); err == nil {
		t.Fatal("accepted coding goal without blocking conditions")
	}
}

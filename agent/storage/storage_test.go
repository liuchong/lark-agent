package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/replymatch"
)

func TestOpenSerializesSQLiteAccessWithBoundedBusyWait(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections = %d, want 1", got)
	}
	var busyTimeoutMilliseconds int
	if err := store.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeoutMilliseconds); err != nil {
		t.Fatal(err)
	}
	if busyTimeoutMilliseconds != 5000 {
		t.Fatalf("busy timeout = %dms, want 5000ms", busyTimeoutMilliseconds)
	}
}

func TestInboxDeduplicatesByMessageID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ev := domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt-1",
		MessageID: "om_dup",
		ChatID:    "oc_1",
		Content:   "ping",
	}
	first, err := store.EnqueueEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	if !first || second {
		t.Fatalf("dedupe mismatch: first=%v second=%v", first, second)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
}

func TestPendingDelegatedWorkAndSemanticResolutionAudit(t *testing.T) {
	store := openStore(t)
	for _, event := range []domain.NormalizedEvent{
		{MessageID: "om_a", ChatID: "oc_group", Content: "发布日期？"},
		{MessageID: "om_b", ChatID: "oc_group", Content: "负责人？"},
		{MessageID: "om_other", ChatID: "oc_other", Content: "另一个群"},
	} {
		if _, err := store.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := store.UpdateWorkItemScheduling(
			item.ID,
			domain.WorkKindDirectMention,
			domain.PriorityDirectMention,
			time.Minute,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(
			`UPDATE work_items SET status = ? WHERE id = ?`,
			domain.StatusWaitingUser,
			item.ID,
		); err != nil {
			t.Fatal(err)
		}
	}

	pending, err := store.ListPendingDelegatedWork("oc_group")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].Event.MessageID != "om_a" ||
		pending[1].Event.MessageID != "om_b" {
		t.Fatalf("pending=%+v", pending)
	}

	cutoff := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
	resolution := replymatch.Resolution{
		TargetMessageID:        "om_a",
		Result:                 replymatch.ResultAnswered,
		MatchedOwnerMessageIDs: []string{"om_owner"},
		Confidence:             0.97,
		Reason:                 "owner supplied the requested date",
		ContextCutoff:          cutoff,
	}
	if err := store.RecordOwnerReplyResolution(pending[0].ID, resolution); err != nil {
		t.Fatal(err)
	}
	audits, err := store.ListOwnerReplyResolutions(pending[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].TargetMessageID != "om_a" ||
		audits[0].Result != replymatch.ResultAnswered ||
		len(audits[0].MatchedOwnerMessageIDs) != 1 ||
		audits[0].MatchedOwnerMessageIDs[0] != "om_owner" ||
		!audits[0].ContextCutoff.Equal(cutoff) {
		t.Fatalf("audits=%+v", audits)
	}
}

func TestEnqueueEventHydratesDuplicateEmptyContentWithoutReplayingTerminalWork(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.EnqueueEvent(domain.NormalizedEvent{Source: domain.SourcePoll, EventID: "evt-empty", MessageID: "om_hydrate"})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.Complete(item.ID, domain.Decision{Kind: domain.DecisionIgnore, Reason: "empty"}); err != nil {
		t.Fatal(err)
	}
	inserted, err := store.EnqueueEvent(domain.NormalizedEvent{Source: domain.SourcePoll, EventID: "evt-empty", MessageID: "om_hydrate", Content: "@Owner 需要 示例客户端回调吗？"})
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("duplicate event should not be inserted")
	}
	item2, ok, err := store.ClaimNext("worker")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("terminal duplicate must not be replayed: %+v", item2)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.Content != "@Owner 需要 示例客户端回调吗？" {
		t.Fatalf("hydrated items=%+v", items)
	}
	if items[0].Status != domain.StatusIgnored {
		t.Fatalf("status=%s want=%s", items[0].Status, domain.StatusIgnored)
	}
}

func TestClaimNextUsesPriorityBeforeInsertionOrder(t *testing.T) {
	store := openStore(t)
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om_slow", Content: "帮我查代码"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om_fast", Content: "几点了"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if err := store.UpdateWorkItemScheduling(items[0].ID, domain.WorkKindCodingQuestion, domain.PriorityCodingQuestion, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkItemScheduling(items[1].ID, domain.WorkKindFastPath, domain.PriorityFastPath, time.Minute); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if claimed.Event.MessageID != "om_fast" || claimed.WorkKind != domain.WorkKindFastPath {
		t.Fatalf("claimed=%+v", claimed)
	}
}

func TestEquivalentOwnerRequestsLinkDuplicateWork(t *testing.T) {
	store := openStore(t)
	firstEvent := domain.NormalizedEvent{
		MessageID: "om_group",
		ChatID:    "oc_group",
		SenderID:  "ou_owner",
		Content:   "@Assistant Bot 帮我看一下接口为什么每次都访问 SampleDB",
		Mentions:  []domain.Mention{{OpenID: "ou_bot", Name: "Assistant Bot"}},
		CreatedAt: time.Now().UTC(),
	}
	secondEvent := domain.NormalizedEvent{
		MessageID: "om_private",
		ChatID:    "oc_private",
		ChatType:  "p2p",
		SenderID:  "ou_owner",
		Content:   "帮我看一下接口为什么每次都访问 SampleDB",
		CreatedAt: firstEvent.CreatedAt.Add(20 * time.Second),
	}
	if inserted, err := store.EnqueueEvent(firstEvent); err != nil || !inserted {
		t.Fatalf("first inserted=%v err=%v", inserted, err)
	}
	if inserted, err := store.EnqueueEvent(secondEvent); err != nil || !inserted {
		t.Fatalf("second inserted=%v err=%v", inserted, err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].DuplicateOf != items[0].ID || items[1].Status != domain.StatusIgnored {
		t.Fatalf("items=%+v", items)
	}
	first, ok, err := store.ClaimNext("worker")
	if err != nil || !ok || first.ID != items[0].ID {
		t.Fatalf("first claim=%+v ok=%v err=%v", first, ok, err)
	}
	if second, ok, err := store.ClaimNext("worker"); err != nil || ok {
		t.Fatalf("duplicate should not be claimable: second=%+v ok=%v err=%v", second, ok, err)
	}
}

func TestLeaseHeartbeatPreventsStaleRecovery(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_heartbeat", Content: "long coding task"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	if _, err := store.StartAgentRun(context.Background(), event, "model", "config"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE work_items SET lease_time = ? WHERE id = ?`, old, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshLease(item.ID, item.LeaseBy); err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueExpiredLeases(time.Minute); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusProcessing || items[0].LeaseTime.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("items=%+v", items)
	}
}

func TestQueueSummaryReportsLanesAndFastPathHits(t *testing.T) {
	store := openStore(t)
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om_fast", Content: "几点了"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkItemScheduling(items[0].ID, domain.WorkKindFastPath, domain.PriorityFastPath, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(items[0].ID, domain.Decision{Kind: domain.DecisionReply, WorkKind: domain.WorkKindFastPath, ReplyText: "现在是 04:13"}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.QueueSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.LaneCounts[string(domain.WorkKindFastPath)] != 1 || summary.FastPathHits != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	if len(summary.Recent) != 1 ||
		summary.Recent[0].MessageID != "om_fast" ||
		!summary.Recent[0].FastPath ||
		summary.Recent[0].ModelTurns != 0 ||
		summary.Recent[0].ToolCalls != 0 {
		t.Fatalf("recent metrics=%+v", summary.Recent)
	}
}

func TestReplyActionAuditIsIdempotentAndKeepsExternalMessageID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_source"})
	_, err = store.EnqueueWorkItem(item)
	if err != nil {
		t.Fatal(err)
	}
	actionID, key, externalID, completed, err := store.BeginReplyAction(
		context.Background(), item.DedupKey, "🤖收到。",
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed || externalID != "" || key != item.DedupKey+":reply" {
		t.Fatalf("action=%d key=%q external=%q completed=%v", actionID, key, externalID, completed)
	}
	if err := store.CompleteReplyAction(context.Background(), actionID, "om_reply", ""); err != nil {
		t.Fatal(err)
	}
	secondID, secondKey, externalID, completed, err := store.BeginReplyAction(
		context.Background(), item.DedupKey, "🤖收到。",
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondID != actionID || secondKey != key || externalID != "om_reply" || !completed {
		t.Fatalf("action=%d key=%q external=%q completed=%v", secondID, secondKey, externalID, completed)
	}
}

func TestOwnerActivityAuditResumesReactionCleanup(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_owner"})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	actionID, reactionID, completed, err := store.BeginOwnerActivity(
		context.Background(), item.DedupKey, item.Event.MessageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if actionID == 0 || reactionID != "" || completed {
		t.Fatalf("action=%d reaction=%q completed=%v", actionID, reactionID, completed)
	}
	if err := store.RecordOwnerActivityReaction(context.Background(), actionID, "reaction_typing"); err != nil {
		t.Fatal(err)
	}
	secondID, reactionID, completed, err := store.BeginOwnerActivity(
		context.Background(), item.DedupKey, item.Event.MessageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondID != actionID || reactionID != "reaction_typing" || completed {
		t.Fatalf("action=%d reaction=%q completed=%v", secondID, reactionID, completed)
	}
	if err := store.CompleteOwnerActivity(context.Background(), actionID, reactionID, ""); err != nil {
		t.Fatal(err)
	}
	_, _, completed, err = store.BeginOwnerActivity(
		context.Background(), item.DedupKey, item.Event.MessageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("completed owner activity was not preserved")
	}
}

func TestBlockedOwnerActivityCleanupIsClaimedIndependently(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_cleanup"})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	actionID, _, _, err := store.BeginOwnerActivity(
		context.Background(), item.DedupKey, item.Event.MessageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOwnerActivityReaction(context.Background(), actionID, "reaction_cleanup"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteOwnerActivity(
		context.Background(), actionID, "reaction_cleanup", "temporary delete failure",
	); err != nil {
		t.Fatal(err)
	}
	claimedID, messageID, reactionID, found, err := store.ClaimOwnerActivityCleanup(
		context.Background(), time.Now().Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || claimedID != actionID || messageID != "om_cleanup" || reactionID != "reaction_cleanup" {
		t.Fatalf(
			"action=%d message=%q reaction=%q found=%v",
			claimedID, messageID, reactionID, found,
		)
	}
	_, _, _, found, err = store.ClaimOwnerActivityCleanup(
		context.Background(), time.Now().Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("claimed the same owner activity twice")
	}
}

func TestTerminalWorkClaimsInterruptedOwnerActivityCleanup(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	item := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_interrupted_cleanup"})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	actionID, _, _, err := store.BeginOwnerActivity(
		context.Background(), item.DedupKey, item.Event.MessageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOwnerActivityReaction(context.Background(), actionID, "reaction_interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(claimed.ID, domain.Decision{Kind: domain.DecisionIgnore}); err != nil {
		t.Fatal(err)
	}
	gotID, messageID, reactionID, found, err := store.ClaimOwnerActivityCleanup(
		context.Background(), time.Now().Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || gotID != actionID ||
		messageID != "om_interrupted_cleanup" || reactionID != "reaction_interrupted" {
		t.Fatalf("action=%d message=%q reaction=%q found=%v", gotID, messageID, reactionID, found)
	}
}

func TestSaveCodingGoalMovesWorkToBackgroundLane(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_goal", Content: "持续跟进这个重构"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	goal, err := domain.NewCodingGoal(domain.CodingGoalSpec{
		WorkItemID:            items[0].ID,
		OriginalMessageID:     event.MessageID,
		Question:              event.Content,
		CompletionConditions:  []string{"完成重构计划"},
		BlockingConditions:    []string{"缺少仓库权限"},
		MaxInvestigationTurns: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodingGoal(goal); err != nil {
		t.Fatal(err)
	}
	goals, err := store.ListCodingGoals(domain.CodingGoalActive)
	if err != nil {
		t.Fatal(err)
	}
	items, err = store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 1 ||
		goals[0].WorkItemID != items[0].ID ||
		items[0].WorkKind != domain.WorkKindCodingGoal ||
		items[0].Priority != domain.PriorityBackground {
		t.Fatalf("goals=%+v items=%+v", goals, items)
	}
}

func TestClaimNextForLaneSeparatesForegroundAndBackground(t *testing.T) {
	store := openStore(t)
	fast := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_lane_fast", Content: "ping"})
	fast.WorkKind = domain.WorkKindFastPath
	fast.Priority = domain.PriorityFastPath
	goal := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_lane_goal", Content: "background"})
	goal.WorkKind = domain.WorkKindCodingGoal
	goal.Priority = domain.PriorityBackground
	if _, err := store.EnqueueWorkItem(goal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueWorkItem(fast); err != nil {
		t.Fatal(err)
	}
	background, ok, err := store.ClaimNextForLane("background", domain.SchedulerLaneBackground)
	if err != nil || !ok || background.WorkKind != domain.WorkKindCodingGoal {
		t.Fatalf("background=%+v ok=%v err=%v", background, ok, err)
	}
	foreground, ok, err := store.ClaimNextForLane("foreground", domain.SchedulerLaneForeground)
	if err != nil || !ok || foreground.WorkKind != domain.WorkKindFastPath {
		t.Fatalf("foreground=%+v ok=%v err=%v", foreground, ok, err)
	}
}

func TestInteractiveLaneReservesCapacityFromCodingQuestions(t *testing.T) {
	store := openStore(t)
	coding := domain.NewWorkItem(domain.NormalizedEvent{MessageID: "om_coding", Content: "code"})
	coding.WorkKind = domain.WorkKindCodingQuestion
	coding.Priority = domain.PriorityCodingQuestion
	if _, err := store.EnqueueWorkItem(coding); err != nil {
		t.Fatal(err)
	}
	if item, ok, err := store.ClaimNextForLane("interactive", domain.SchedulerLaneInteractive); err != nil || ok {
		t.Fatalf("interactive claimed coding item=%+v ok=%v err=%v", item, ok, err)
	}
	if item, ok, err := store.ClaimNextForLane("foreground", domain.SchedulerLaneForeground); err != nil || !ok ||
		item.WorkKind != domain.WorkKindCodingQuestion {
		t.Fatalf("foreground item=%+v ok=%v err=%v", item, ok, err)
	}
}

func TestOldRunWithFreshLeaseIsNotAbandoned(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_fresh_lease", Content: "long task"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("heartbeat-worker")
	if err != nil || !ok {
		t.Fatalf("item=%+v ok=%v err=%v", item, ok, err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "model", "config")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE agent_runs SET started_at = ? WHERE id = ?`, old, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshLease(item.ID, item.LeaseBy); err != nil {
		t.Fatal(err)
	}
	requeued, err := store.RequeueAbandonedRuns(time.Minute)
	if err != nil || requeued != 0 {
		t.Fatalf("requeued=%d err=%v", requeued, err)
	}
}

func TestDuplicateUsesConfiguredWindowAndCrossEntryOnly(t *testing.T) {
	store := openStore(t)
	store.ConfigureScheduler(time.Minute, 3)
	group := domain.NormalizedEvent{
		MessageID: "om_group_old", ChatID: "group", ChatType: "group",
		SenderID: "owner", Content: "same request",
	}
	if _, err := store.EnqueueEvent(group); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE work_items SET created_at = ? WHERE dedup_key = ?`, old, domain.DedupKey(group)); err != nil {
		t.Fatal(err)
	}
	private := domain.NormalizedEvent{
		MessageID: "om_private_new", ChatID: "private", ChatType: "p2p",
		SenderID: "owner", Content: "same request",
	}
	inserted, err := store.EnqueueEvent(private)
	if err != nil || !inserted {
		t.Fatalf("configured window should keep new request inserted=%v err=%v", inserted, err)
	}
	sameChat := domain.NormalizedEvent{
		MessageID: "om_private_repeat", ChatID: "private", ChatType: "p2p",
		SenderID: "owner", Content: "same request",
	}
	inserted, err = store.EnqueueEvent(sameChat)
	if err != nil || !inserted {
		t.Fatalf("same-entry repeat should not be cross-entry duplicate inserted=%v err=%v", inserted, err)
	}
}

func TestLostLeaseCannotCompleteOrRetryNewClaim(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_fenced", Content: "task"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	oldClaim, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("old claim=%+v ok=%v err=%v", oldClaim, ok, err)
	}
	if err := store.RequeueExpiredLeases(-1); err != nil {
		t.Fatal(err)
	}
	newClaim, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("new claim=%+v ok=%v err=%v", newClaim, ok, err)
	}
	if oldClaim.LeaseBy == newClaim.LeaseBy {
		t.Fatalf("claim token was reused: %q", oldClaim.LeaseBy)
	}
	decision := domain.Decision{Kind: domain.DecisionReply, WorkKind: domain.WorkKindSimpleQuestion}
	if err := store.CompleteClaim(oldClaim.ID, oldClaim.LeaseBy, decision); err == nil {
		t.Fatal("lost lease completed a newer claim")
	}
	if err := store.MarkRetryClaim(oldClaim.ID, oldClaim.LeaseBy, "stale", 0); err == nil {
		t.Fatal("lost lease retried a newer claim")
	}
	if err := store.CompleteClaim(newClaim.ID, newClaim.LeaseBy, decision); err != nil {
		t.Fatal(err)
	}
}

func TestCodingGoalTurnsAccumulateAcrossRunsAndSave(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_goal_budget", Content: "goal"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("goal-worker")
	if err != nil || !ok {
		t.Fatalf("item=%+v ok=%v err=%v", item, ok, err)
	}
	goal, err := domain.NewCodingGoal(domain.CodingGoalSpec{
		WorkItemID: item.ID, OriginalMessageID: event.MessageID, Question: event.Content,
		CompletionConditions: []string{"done"}, BlockingConditions: []string{"blocked"},
		MaxInvestigationTurns: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodingGoal(goal); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "model", "config")
	if err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 3; sequence++ {
		if err := store.AppendAgentStep(context.Background(), domain.AgentStep{
			RunID: run.ID, Sequence: sequence, Kind: "model",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinishAgentRun(context.Background(), run.ID, domain.AgentRunFailed, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodingGoal(goal); err != nil {
		t.Fatal(err)
	}
	used, maxTurns, err := store.CodingGoalBudget(item.ID)
	if err != nil || used != 3 || maxTurns != 20 {
		t.Fatalf("used=%d max=%d err=%v", used, maxTurns, err)
	}
	summary, err := store.QueueSummary()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Recent) != 1 || summary.Recent[0].ModelTurns != 3 {
		t.Fatalf("recent=%+v", summary.Recent)
	}
}

func TestAgentRunPersistsTrajectoryAndCompletion(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_run", Content: "inspect code"}
	if inserted, err := store.EnqueueEvent(event); err != nil || !inserted {
		t.Fatalf("enqueue inserted=%v err=%v", inserted, err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "ark-code-latest", "cfg-v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAgentStep(context.Background(), domain.AgentStep{
		RunID:            run.ID,
		Sequence:         1,
		Kind:             "model",
		OutputJSON:       `{"tool_calls":[]}`,
		RequestID:        "req_1",
		PromptTokens:     10,
		CompletionTokens: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAgentStep(context.Background(), domain.AgentStep{
		RunID:      run.ID,
		Sequence:   2,
		Kind:       "tool",
		ToolCallID: "call_1",
		ToolName:   "read_workspace",
		InputJSON:  `{"path":"router.go"}`,
		OutputJSON: `{"ok":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAgentRun(context.Background(), run.ID, domain.AgentRunCompleted, ""); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListAgentRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != domain.AgentRunCompleted || runs[0].ModelFingerprint != "ark-code-latest" {
		t.Fatalf("runs=%+v", runs)
	}
	steps, err := store.ListAgentSteps(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[1].ToolCallID != "call_1" || steps[0].RequestID != "req_1" {
		t.Fatalf("steps=%+v", steps)
	}
}

func TestRequeueAbandonedRunsRestoresProcessingWork(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_abandoned", Content: "retry me"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "model", "config")
	if err != nil {
		t.Fatal(err)
	}
	goal, err := domain.NewCodingGoal(domain.CodingGoalSpec{
		WorkItemID: item.ID, OriginalMessageID: event.MessageID, Question: event.Content,
		CompletionConditions: []string{"done"}, BlockingConditions: []string{"blocked"},
		MaxInvestigationTurns: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCodingGoalClaim(goal, item.LeaseBy); err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 2; sequence++ {
		if err := store.AppendAgentStep(context.Background(), domain.AgentStep{
			RunID: run.ID, Sequence: sequence, Kind: "model",
		}); err != nil {
			t.Fatal(err)
		}
	}
	actionID, previous, uncertain, err := store.BeginShellAction(context.Background(), item.DedupKey, "go test ./...", ".")
	if err != nil || actionID == 0 || previous != "" || uncertain {
		t.Fatalf("begin action id=%d previous=%q uncertain=%v err=%v", actionID, previous, uncertain, err)
	}
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE agent_runs SET started_at = ? WHERE id = ?`, old, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE work_items SET lease_time = ?, lease_timeout_seconds = 60 WHERE id = ?`,
		old, item.ID,
	); err != nil {
		t.Fatal(err)
	}
	requeued, err := store.RequeueAbandonedRuns(time.Minute)
	if err != nil || requeued != 1 {
		t.Fatalf("requeued=%d err=%v", requeued, err)
	}
	used, _, err := store.CodingGoalBudget(item.ID)
	if err != nil || used != 2 {
		t.Fatalf("abandoned turns used=%d err=%v", used, err)
	}
	next, ok, err := store.ClaimNext("worker-2")
	if err != nil || !ok || next.ID != item.ID {
		t.Fatalf("next=%+v ok=%v err=%v", next, ok, err)
	}
	runs, err := store.ListAgentRuns()
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != domain.AgentRunAbandoned {
		t.Fatalf("runs=%+v", runs)
	}
	replayedID, previous, uncertain, err := store.BeginShellAction(context.Background(), item.DedupKey, "go test ./...", ".")
	if err != nil || replayedID != actionID || previous != "" || !uncertain {
		t.Fatalf("replayed action id=%d previous=%q uncertain=%v err=%v", replayedID, previous, uncertain, err)
	}
}

func TestShellApprovalIsExactOneTimeAndRequeuesWork(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_approval", Content: "run formatter"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	actionID, err := store.RequestShellApproval(context.Background(), item.DedupKey, "gofmt -w .", ".")
	if err != nil {
		t.Fatal(err)
	}
	if _, approved, err := store.ConsumeShellApproval(context.Background(), item.DedupKey, "gofmt -w .", "."); err != nil || approved {
		t.Fatalf("approval consumed before decision approved=%v err=%v", approved, err)
	}
	if err := store.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	consumedID, approved, err := store.ConsumeShellApproval(context.Background(), item.DedupKey, "gofmt -w .", ".")
	if err != nil || !approved || consumedID != actionID {
		t.Fatalf("consume id=%d approved=%v err=%v", consumedID, approved, err)
	}
	if _, approved, err := store.ConsumeShellApproval(context.Background(), item.DedupKey, "gofmt -w .", "."); err != nil || approved {
		t.Fatalf("approval was not one-time approved=%v err=%v", approved, err)
	}
	if err := store.CompleteShellApproval(context.Background(), actionID, `{"exit_code":0}`, ""); err != nil {
		t.Fatal(err)
	}
	action, err := store.GetActionAttempt(actionID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionCompleted {
		t.Fatalf("action=%+v", action)
	}
}

func TestDecideActionWaitsForConcurrentWriterWithoutSnapshotFailure(t *testing.T) {
	for _, tc := range []struct {
		name           string
		approve        bool
		wantAction     domain.ActionStatus
		wantWorkStatus domain.WorkItemStatus
	}{
		{
			name: "approve", approve: true,
			wantAction: domain.ActionReady, wantWorkStatus: domain.StatusReceived,
		},
		{
			name: "reject", approve: false,
			wantAction: domain.ActionCancelled, wantWorkStatus: domain.StatusCancelled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.db")
			daemonStore, err := Open(statePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = daemonStore.Close() })
			if _, err := daemonStore.MarkCurrentSessionReady(context.Background()); err != nil {
				t.Fatal(err)
			}
			operatorStore, err := OpenInspection(statePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = operatorStore.Close() })

			event := domain.NormalizedEvent{
				MessageID: "om_concurrent_approval_" + tc.name,
				Content:   "decide me",
			}
			if _, err := daemonStore.EnqueueEvent(event); err != nil {
				t.Fatal(err)
			}
			item, ok, err := daemonStore.ClaimNext("daemon-worker")
			if err != nil || !ok {
				t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
			}
			actionID, err := daemonStore.RequestShellApproval(
				context.Background(), item.DedupKey, "gofmt -w .", ".",
			)
			if err != nil {
				t.Fatal(err)
			}

			writer, err := daemonStore.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Exec(
				`UPDATE work_items SET updated_at = ? WHERE id = ?`,
				time.Now().UTC().Format(time.RFC3339Nano), item.ID,
			); err != nil {
				_ = writer.Rollback()
				t.Fatal(err)
			}

			decided := make(chan error, 1)
			go func() {
				decided <- operatorStore.DecideAction(actionID, tc.approve)
			}()
			time.Sleep(100 * time.Millisecond)
			if err := writer.Commit(); err != nil {
				t.Fatal(err)
			}

			select {
			case err := <-decided:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("approval did not continue after the concurrent writer released its lock")
			}
			action, err := daemonStore.GetActionAttempt(actionID)
			if err != nil {
				t.Fatal(err)
			}
			if action.Status != tc.wantAction {
				t.Fatalf("action=%+v", action)
			}
			items, err := daemonStore.ListWorkItems()
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].ID != item.ID ||
				items[0].Status != tc.wantWorkStatus {
				t.Fatalf("items=%+v", items)
			}
		})
	}
}

func TestRetryLimitDeadLettersAndRuntimeUpgradeRequeues(t *testing.T) {
	store := openStore(t)
	store.ConfigureRecovery(2)
	event := domain.NormalizedEvent{MessageID: "om_poison", Content: "poison"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	run, err := store.StartAgentRun(context.Background(), event, "old-model", "old-config")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAgentRun(context.Background(), run.ID, domain.AgentRunFailed, "bad response"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRetry(item.ID, "bad response"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRetry(item.ID, "bad response"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusDeadLetter {
		t.Fatalf("items=%+v", items)
	}
	changed, err := store.RequeueChangedRuntimeFailures("new-model", "new-config")
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	items, err = store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusReceived || items[0].RetryCount != 0 {
		t.Fatalf("items=%+v", items)
	}
}

func TestMarkDeadLetterClaimFencesLeaseAndPreservesReason(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{
		MessageID: "om_model_non_convergence",
		Content:   "@Owner 看看删号报 10005",
	}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	reason := "model did not submit a terminal decision after 3 attempts"
	if err := store.MarkDeadLetterClaim(item.ID, "wrong-lease", reason); err == nil {
		t.Fatal("wrong lease moved work to dead letter")
	}
	if err := store.MarkDeadLetterClaim(item.ID, item.LeaseBy, reason); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusDeadLetter ||
		items[0].RetryCount != 1 {
		t.Fatalf("items=%+v", items)
	}
	var storedReason string
	if err := store.db.QueryRow(
		`SELECT reason FROM dead_letters WHERE work_item_id = ?`,
		item.ID,
	).Scan(&storedReason); err != nil {
		t.Fatal(err)
	}
	if storedReason != reason {
		t.Fatalf("stored reason=%q", storedReason)
	}
}

func TestMarkRetryAfterHonorsProviderWindowWithinCeiling(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_retry_after", Content: "rate limited"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	before := time.Now().UTC()
	if err := store.MarkRetryAfter(item.ID, "429", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	delay := items[0].NextAttemptAt.Sub(before)
	if delay < 9*time.Minute+59*time.Second || delay > 10*time.Minute+5*time.Second {
		t.Fatalf("delay=%s item=%+v", delay, items[0])
	}
}

func TestReadyApprovedReplyReturnsPersistedExactDraft(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_reply_resume", Content: "question"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	actionID, err := store.RequestReplyApproval(
		context.Background(),
		item.DedupKey,
		"exact persisted draft",
		"code evidence",
		"confirm backend contract",
		domain.RelevanceAssistantRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	decision, found, err := store.ReadyApprovedReply(item.ID)
	if err != nil || !found {
		t.Fatalf("decision=%+v found=%v err=%v", decision, found, err)
	}
	if decision.ReplyText != "exact persisted draft" ||
		decision.OwnerAction != "confirm backend contract" ||
		decision.Relevance != domain.RelevanceAssistantRequest ||
		decision.Kind != domain.DecisionReply {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestReadyApprovedReplyRestoresAndConsumesLegacyIdentity(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_legacy_reply_resume", Content: "question"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	decision := domain.Decision{
		Kind:        domain.DecisionRequestApproval,
		Mode:        domain.ModeApproval,
		Relevance:   domain.RelevanceAssistantRequest,
		Confidence:  0.6,
		Risk:        domain.RiskLow,
		Reason:      "legacy evidence",
		ReplyText:   "legacy exact draft",
		OwnerAction: "confirm legacy contract",
	}
	actionID, err := store.RequestReplyApproval(
		context.Background(),
		item.DedupKey,
		decision.ReplyText,
		decision.Reason,
		decision.OwnerAction,
		decision.Relevance,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(item.ID, decision); err != nil {
		t.Fatal(err)
	}
	legacyRequest, err := json.Marshal(map[string]string{
		"text":         decision.ReplyText,
		"reason":       decision.Reason,
		"owner_action": decision.OwnerAction,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(
		item.DedupKey + "\x00" + decision.ReplyText + "\x00" +
			decision.Reason + "\x00" + decision.OwnerAction,
	))
	legacyKey := fmt.Sprintf("reply:%x", sum[:])
	if _, err := store.db.Exec(
		`UPDATE action_attempts SET request_json = ?, idempotency_key = ? WHERE id = ?`,
		string(legacyRequest), legacyKey, actionID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	approved, found, err := store.ReadyApprovedReply(item.ID)
	if err != nil || !found {
		t.Fatalf("approved=%+v found=%v err=%v", approved, found, err)
	}
	if approved.Relevance != domain.RelevanceAssistantRequest {
		t.Fatalf("approved=%+v", approved)
	}
	consumedID, consumed, err := store.ConsumeReplyApproval(
		context.Background(),
		item.DedupKey,
		approved.ReplyText,
		approved.Reason,
		approved.OwnerAction,
		approved.Relevance,
	)
	if err != nil || !consumed || consumedID != actionID {
		t.Fatalf("consumedID=%d consumed=%v actionID=%d err=%v", consumedID, consumed, actionID, err)
	}
}

func TestReadyApprovedReplyRejectsLegacyWithoutDurableIdentity(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_legacy_reply_without_identity", Content: "question"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	actionID, err := store.RequestReplyApproval(
		context.Background(),
		item.DedupKey,
		"legacy exact draft",
		"legacy evidence",
		"",
		domain.RelevanceAssistantRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	legacyRequest, err := json.Marshal(map[string]string{
		"text":   "legacy exact draft",
		"reason": "legacy evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE action_attempts SET request_json = ? WHERE id = ?`,
		string(legacyRequest), actionID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReadyApprovedReply(item.ID); err == nil {
		t.Fatal("legacy approval without durable relevance was accepted")
	}
}

func TestReplyApprovalIdentityIncludesReasonAndOwnerAction(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_distinct_reply_approvals", Content: "question"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	firstID, err := store.RequestReplyApproval(
		context.Background(), item.DedupKey, "same reply", "first reason", "first owner action",
		domain.RelevanceAssistantRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.RequestReplyApproval(
		context.Background(), item.DedupKey, "same reply", "second reason", "second owner action",
		domain.RelevanceDirectMention,
	)
	if err != nil {
		t.Fatal(err)
	}
	thirdID, err := store.RequestReplyApproval(
		context.Background(), item.DedupKey, "same reply", "second reason", "second owner action",
		domain.RelevanceAssistantRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatalf("distinct exact drafts reused action id %d", firstID)
	}
	if secondID == thirdID {
		t.Fatalf("distinct reply identities reused action id %d", secondID)
	}
	if err := store.DecideAction(secondID, true); err != nil {
		t.Fatal(err)
	}
	actionID, consumed, err := store.ConsumeReplyApproval(
		context.Background(), item.DedupKey, "same reply", "second reason", "second owner action",
		domain.RelevanceDirectMention,
	)
	if err != nil || !consumed || actionID != secondID {
		t.Fatalf("actionID=%d consumed=%v err=%v", actionID, consumed, err)
	}
}

func TestConsumeReplyApprovalReturnsNotFoundWhenNoApprovalExists(t *testing.T) {
	store := openStore(t)
	item := domain.NewWorkItem(domain.NormalizedEvent{
		MessageID: "om_auto_reply_without_approval",
		Content:   "question",
	})
	if _, err := store.EnqueueWorkItem(item); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	if err := store.MarkRetry(item.ID, "previous transient failure"); err != nil {
		t.Fatal(err)
	}
	actionID, consumed, err := store.ConsumeReplyApproval(
		context.Background(),
		item.DedupKey,
		"evidence-backed answer",
		"code evidence",
		"",
		domain.RelevanceOwnerRequest,
	)
	if err != nil || consumed || actionID != 0 {
		t.Fatalf("actionID=%d consumed=%v err=%v", actionID, consumed, err)
	}
}

func TestPostReplyNotificationRecoversWithoutReplyReplay(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{MessageID: "om_post_reply_notice", Content: "coordinate"}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	decision := domain.Decision{
		Kind:        domain.DecisionReply,
		ReplyText:   "已收到，我会按确认后的契约接入",
		OwnerAction: "确认后端示例状态变更通知契约",
		Reason:      "coordination handoff",
	}
	replyActionID, _, _, completedReply, err := store.BeginReplyAction(
		context.Background(),
		item.DedupKey,
		decision.ReplyText,
	)
	if err != nil || completedReply {
		t.Fatalf("reply action id=%d completed=%v err=%v", replyActionID, completedReply, err)
	}
	if err := store.CompleteReplyAction(
		context.Background(),
		replyActionID,
		"om_sent_reply",
		"",
	); err != nil {
		t.Fatal(err)
	}
	actionID, key, completed, err := store.BeginPostReplyNotification(
		context.Background(), item.DedupKey, decision,
	)
	if err != nil || completed || actionID == 0 || key == "" || len(key) > 50 {
		t.Fatalf("actionID=%d key=%q completed=%v err=%v", actionID, key, completed, err)
	}
	if err := store.CompletePostReplyNotification(context.Background(), actionID, "temporary failure"); err != nil {
		t.Fatal(err)
	}
	readyID, readyKey, recovered, found, err := store.ReadyPostReplyNotification(item.ID)
	if err != nil || !found || readyID != actionID || readyKey != key ||
		recovered.ReplyText != decision.ReplyText || recovered.OwnerAction != decision.OwnerAction {
		t.Fatalf("readyID=%d key=%q recovered=%+v found=%v err=%v", readyID, readyKey, recovered, found, err)
	}
	retryID, retryKey, completed, err := store.BeginPostReplyNotification(
		context.Background(), item.DedupKey, recovered,
	)
	if err != nil || completed || retryID != actionID || retryKey != key {
		t.Fatalf("retryID=%d key=%q completed=%v err=%v", retryID, retryKey, completed, err)
	}
	if err := store.CompletePostReplyNotification(context.Background(), retryID, ""); err != nil {
		t.Fatal(err)
	}
	completedID, completedKey, completedDecision, found, err := store.ReadyPostReplyNotification(item.ID)
	if err != nil || !found || completedID != actionID || completedKey != key ||
		completedDecision.ReplyText != decision.ReplyText {
		t.Fatalf("completedID=%d key=%q decision=%+v found=%v err=%v", completedID, completedKey, completedDecision, found, err)
	}
	_, _, completed, err = store.BeginPostReplyNotification(context.Background(), item.DedupKey, decision)
	if err != nil || !completed {
		t.Fatalf("completed=%v err=%v", completed, err)
	}
}

func TestCompletedPreReplyNoticeDoesNotSkipUnsentSenderReply(t *testing.T) {
	store := openStore(t)
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{
		MessageID: "om_pre_reply_notice",
		Content:   "coordinate",
	}); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	actionID, _, completed, err := store.BeginPostReplyNotification(
		context.Background(),
		item.DedupKey,
		domain.Decision{
			Kind:      domain.DecisionReply,
			ReplyText: "智能助手准备发送的答复",
		},
	)
	if err != nil || completed {
		t.Fatalf("actionID=%d completed=%v err=%v", actionID, completed, err)
	}
	if err := store.CompletePostReplyNotification(context.Background(), actionID, ""); err != nil {
		t.Fatal(err)
	}
	_, _, _, found, err := store.ReadyPostReplyNotification(item.ID)
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestRequeueLegacyCompletedMentionsOnlyWithoutRunOrAction(t *testing.T) {
	store := openStore(t)
	owner := "ou_owner"
	legacy := domain.NormalizedEvent{
		MessageID: "om_legacy_mention",
		Content:   "@owner investigate",
		Mentions:  []domain.Mention{{OpenID: owner}},
	}
	plain := domain.NormalizedEvent{MessageID: "om_legacy_plain", Content: "plain"}
	for _, event := range []domain.NormalizedEvent{legacy, plain} {
		if _, err := store.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
		item, ok, err := store.ClaimNext("worker")
		if err != nil || !ok {
			t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
		}
		if err := store.Complete(item.ID, domain.Decision{Kind: domain.DecisionRecord}); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := store.RequeueLegacyCompletedMentions(owner)
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusReceived || items[1].Status != domain.StatusCompleted {
		t.Fatalf("items=%+v", items)
	}
	changed, err = store.RequeueLegacyCompletedMentions(owner)
	if err != nil || changed != 0 {
		t.Fatalf("second changed=%d err=%v", changed, err)
	}
}

func TestLegacyRecoveryDoesNotBlindlyReplayCompletedOwnerRequest(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{
		MessageID: "om_private_unsent", ChatID: "oc_private", ChatType: "p2p",
		ChatPartnerID: "ou_bot", SenderID: "ou_owner", Content: "几点了？",
	}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	if err := store.Complete(item.ID, domain.Decision{
		Kind: domain.DecisionReply, Relevance: domain.RelevanceOwnerRequest,
		WorkKind: domain.WorkKindFastPath, ReplyText: "现在是 08:09。",
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := store.RequeueLegacyCompletedMentions("ou_owner")
	if err != nil || changed != 0 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusCompleted {
		t.Fatalf("items=%+v", items)
	}
}

func TestRequeueChangedRuntimeDirectMentionsIncludesNotifyWithoutReply(t *testing.T) {
	store := openStore(t)
	owner := "ou_owner"
	events := []domain.NormalizedEvent{
		{
			MessageID: "om_ignored_mention",
			Content:   "@owner coordinate the follow-up",
			Mentions:  []domain.Mention{{OpenID: owner}},
		},
		{MessageID: "om_ignored_plain", Content: "unrelated chatter"},
		{
			MessageID: "om_ignored_mention_replied",
			Content:   "@owner already handled",
			Mentions:  []domain.Mention{{OpenID: owner}},
		},
		{
			MessageID: "om_notified_mention",
			Content:   "@owner answer this technical question",
			Mentions:  []domain.Mention{{OpenID: owner}},
		},
	}
	for index, event := range events {
		if _, err := store.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
		item, ok, err := store.ClaimNext("worker")
		if err != nil || !ok {
			t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
		}
		run, err := store.StartAgentRun(context.Background(), event, "old-model", "old-contract")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishAgentRun(context.Background(), run.ID, domain.AgentRunCompleted, ""); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			actionID, _, _, err := store.BeginShellAction(context.Background(), item.DedupKey, "git status --short", ".")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteShellApproval(context.Background(), actionID, `{"exit_code":0}`, ""); err != nil {
				t.Fatal(err)
			}
		}
		if index == 2 {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(
				`INSERT INTO action_attempts(
					work_item_id, kind, idempotency_key, status, created_at, updated_at
				 ) VALUES (?, 'reply', ?, ?, ?, ?)`,
				item.ID, "reply:"+item.DedupKey, domain.ActionCompleted, now, now); err != nil {
				t.Fatal(err)
			}
		}
		decision := domain.Decision{Kind: domain.DecisionIgnore}
		if index == 3 {
			decision.Kind = domain.DecisionNotify
		}
		if err := store.Complete(item.ID, decision); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := store.RequeueChangedRuntimeDirectMentions(owner, "old-model", "new-contract")
	if err != nil || changed != 2 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusReceived ||
		items[1].Status != domain.StatusIgnored ||
		items[2].Status != domain.StatusIgnored ||
		items[3].Status != domain.StatusReceived {
		t.Fatalf("items=%+v", items)
	}
	changed, err = store.RequeueChangedRuntimeDirectMentions(owner, "old-model", "new-contract")
	if err != nil || changed != 0 {
		t.Fatalf("second changed=%d err=%v", changed, err)
	}
}

func TestRequeueLowRiskDirectMentionApprovals(t *testing.T) {
	store := openStore(t)
	owner := "ou_owner"
	events := []domain.NormalizedEvent{
		{MessageID: "om_low", Mentions: []domain.Mention{{OpenID: owner}}},
		{MessageID: "om_medium", Mentions: []domain.Mention{{OpenID: owner}}},
	}
	for index, event := range events {
		if _, err := store.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
		item, ok, err := store.ClaimNext("worker")
		if err != nil || !ok {
			t.Fatalf("claim ok=%v err=%v", ok, err)
		}
		decision := domain.Decision{
			Kind:        domain.DecisionRequestApproval,
			Relevance:   domain.RelevanceInferred,
			Confidence:  0.72,
			Risk:        domain.RiskLow,
			ReplyText:   "收到，我先确认后同步。",
			OwnerAction: "确认后同步。",
		}
		if index == 1 {
			decision.Risk = domain.RiskMedium
		}
		if err := store.Complete(item.ID, decision); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RequestReplyApproval(
			context.Background(),
			item.DedupKey,
			decision.ReplyText,
			decision.Reason,
			decision.OwnerAction,
			decision.Relevance,
		); err != nil {
			t.Fatal(err)
		}
	}

	changed, err := store.RequeueLowRiskDirectMentionApprovals(owner)
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusReceived || items[1].Status != domain.StatusAwaitingApproval {
		t.Fatalf("items=%+v", items)
	}
	actions, err := store.ListActionAttempts()
	if err != nil {
		t.Fatal(err)
	}
	if actions[0].Status != domain.ActionReady || actions[1].Status != domain.ActionAwaitingApproval {
		t.Fatalf("actions=%+v", actions)
	}
}

func TestRequeueLowRiskDirectMentionApprovalsRecoversCancelledUpgradeAttempt(t *testing.T) {
	store := openStore(t)
	owner := "ou_owner"
	event := domain.NormalizedEvent{MessageID: "om_cancelled_upgrade", Mentions: []domain.Mention{{OpenID: owner}}}
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	decision := domain.Decision{
		Kind:        domain.DecisionRequestApproval,
		Relevance:   domain.RelevanceInferred,
		Confidence:  0.72,
		Risk:        domain.RiskLow,
		ReplyText:   "收到，我确认后同步。",
		OwnerAction: "确认示例状态变更通知契约。",
	}
	if err := store.Complete(item.ID, decision); err != nil {
		t.Fatal(err)
	}
	actionID, err := store.RequestReplyApproval(
		context.Background(),
		item.DedupKey,
		decision.ReplyText,
		decision.Reason,
		decision.OwnerAction,
		decision.Relevance,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE action_attempts SET status = ?, error = ? WHERE id = ?`,
		domain.ActionCancelled, lowRiskDirectMentionApprovalRecoveryError, actionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, decision_json = NULL WHERE id = ?`,
		domain.StatusProcessing, item.ID); err != nil {
		t.Fatal(err)
	}

	changed, err := store.RequeueLowRiskDirectMentionApprovals(owner)
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	approved, found, err := store.ReadyApprovedReply(item.ID)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if approved.ReplyText != decision.ReplyText || approved.OwnerAction != decision.OwnerAction {
		t.Fatalf("approved=%+v", approved)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusReceived {
		t.Fatalf("items=%+v", items)
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCheckpointStoreRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.Set(ctx, "cp-1", []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	data, ok, err := store.Get(ctx, "cp-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(data) != "checkpoint" {
		t.Fatalf("ok=%v data=%q", ok, data)
	}
	if err := store.Delete(ctx, "cp-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(ctx, "cp-1"); err != nil || ok {
		t.Fatalf("after delete ok=%v err=%v", ok, err)
	}
}

func TestCompleteStoresDecisionStatus(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.EnqueueEvent(domain.NormalizedEvent{Source: domain.SourceRealtime, EventID: "evt", MessageID: "om_complete"})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if err := store.Complete(item.ID, domain.Decision{Kind: domain.DecisionIgnore}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.StatusIgnored {
		t.Fatalf("status=%q", items[0].Status)
	}
}

func TestCompleteRejectsUnknownWorkItem(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Complete(999, domain.Decision{Kind: domain.DecisionIgnore}); err == nil {
		t.Fatal("Complete accepted unknown work item")
	}
}

func TestClaimRequeuesExpiredProcessingItem(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.EnqueueEvent(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "evt-2",
		MessageID: "om_retry",
		ChatID:    "oc_1",
		Content:   "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected item")
	}
	if err := store.RequeueExpiredLeases(-1); err != nil {
		t.Fatal(err)
	}
	item2, ok, err := store.ClaimNext("worker-b")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || item2.ID != item.ID {
		t.Fatalf("expected same item, got ok=%v item=%+v", ok, item2)
	}
}

func TestMarkRetryWaitsBeforeNextClaim(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.EnqueueEvent(domain.NormalizedEvent{Source: domain.SourcePoll, EventID: "evt-retry", MessageID: "om_retry_wait"})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker-a")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.MarkRetry(item.ID, "model failed"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNext("worker-b"); err != nil || ok {
		t.Fatalf("retry_wait should not be immediately claimable: ok=%v err=%v", ok, err)
	}
	if changed, err := store.RetryWorkItems([]int64{item.ID}); err != nil || changed != 1 {
		t.Fatalf("RetryWorkItems changed=%d err=%v", changed, err)
	}
	item2, ok, err := store.ClaimNext("worker-c")
	if err != nil || !ok || item2.ID != item.ID {
		t.Fatalf("manual retry did not make item claimable: ok=%v item=%+v err=%v", ok, item2, err)
	}
}

func TestRetryWorkItemsByIDCannotReplayCompletedItem(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.EnqueueEvent(domain.NormalizedEvent{Source: domain.SourcePoll, EventID: "evt-completed-retry", MessageID: "om_completed_retry"})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker-a")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := store.Complete(item.ID, domain.Decision{Kind: domain.DecisionRecord, Reason: "completed too early"}); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.RetryWorkItems([]int64{item.ID}); err == nil || changed != 0 {
		t.Fatalf("RetryWorkItems changed=%d err=%v", changed, err)
	}
	if item2, ok, err := store.ClaimNext("worker-b"); err != nil || ok {
		t.Fatalf("completed item was replayed: ok=%v item=%+v err=%v", ok, item2, err)
	}
}

func TestRetryWorkItemsCannotBypassPriorSessionInterruption(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnqueueEvent(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "evt-prior-session-retry",
		MessageID: "om_prior_session_retry",
	}); err != nil {
		t.Fatal(err)
	}
	item, ok, err := first.ClaimNext("worker-a")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	if changed, err := current.RetryWorkItems([]int64{item.ID}); err == nil || changed != 0 {
		t.Fatalf("RetryWorkItems changed=%d err=%v", changed, err)
	}
	inspection, err := current.InspectWork(context.Background(), domain.WorkInspectionQuery{
		WorkItemID: item.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.WorkItem == nil || inspection.WorkItem.Status != domain.StatusInterrupted {
		t.Fatalf("prior-session item=%+v", inspection.WorkItem)
	}
}

func TestRequeueExpiredLeasesHandlesMissingLeaseTime(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.EnqueueEvent(domain.NormalizedEvent{Source: domain.SourcePoll, EventID: "evt-missing-lease", MessageID: "om_missing_lease"})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker-a")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if _, err := store.db.Exec(`UPDATE work_items SET lease_time = NULL WHERE id = ?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueExpiredLeases(time.Minute); err != nil {
		t.Fatal(err)
	}
	item2, ok, err := store.ClaimNext("worker-b")
	if err != nil || !ok || item2.ID != item.ID {
		t.Fatalf("missing lease item was not requeued: ok=%v item=%+v err=%v", ok, item2, err)
	}
}

func TestPollCursorRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, ok, err := store.GetPollCursor("all"); err != nil || ok {
		t.Fatalf("empty cursor ok=%v err=%v", ok, err)
	}
	want := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	if err := store.SetPollCursor("all", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetPollCursor("all")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !got.Equal(want) {
		t.Fatalf("cursor ok=%v got=%s want=%s", ok, got, want)
	}
}

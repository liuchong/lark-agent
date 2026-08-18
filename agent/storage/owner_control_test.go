package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestOwnerTaskActionViewIsBoundedAndAcknowledgementIsIdempotent(t *testing.T) {
	store := openStore(t)
	first := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_actionable", Content: "需要处理的历史工作",
	})
	second := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_completed", Content: "已经完成的工作",
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter, now, first.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusCompleted, now, second.ID,
	); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListOwnerTasks(context.Background(), domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewAction, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].WorkItem.ID != first.ID {
		t.Fatalf("page=%+v", page)
	}

	command := domain.OwnerControlCommand{
		Name: domain.OwnerControlTaskAcknowledge, WorkItemID: first.ID, Reason: "已人工检查",
	}
	result, err := store.ExecuteOwnerMutation(context.Background(), "om_ack_command", command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.WorkItemID != first.ID ||
		result.Disposition != domain.OwnerResolutionAcknowledged {
		t.Fatalf("result=%+v", result)
	}
	replayed, err := store.ExecuteOwnerMutation(context.Background(), "om_ack_command", command)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.WorkItemID != result.WorkItemID {
		t.Fatalf("replayed=%+v result=%+v", replayed, result)
	}
	page, err = store.ListOwnerTasks(context.Background(), domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewAction, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("acknowledged work remained actionable: %+v", page)
	}
}

func TestOwnerReconciliationNeverBlindlyReplaysUncertainAction(t *testing.T) {
	store := openStore(t)
	item := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_uncertain", Content: "外部发送结果不确定",
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter, now, item.ID,
	); err != nil {
		t.Fatal(err)
	}
	requestJSON, _ := json.Marshal(map[string]string{"text": "发送消息"})
	actionResult, err := store.db.Exec(
		`INSERT INTO action_attempts(
			work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
		 ) VALUES (?, 'reply', ?, ?, ?, ?, ?)`,
		item.ID, "uncertain-action", domain.ActionExecuting, string(requestJSON), now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	actionID, err := actionResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO work_interruptions(
			work_item_id, stage, action_kind, action_status, reason, interrupted_at
		 ) VALUES (?, 'action', 'reply', ?, 'process stopped during send', ?)`,
		item.ID, domain.ActionExecuting, now,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ExecuteOwnerMutation(context.Background(), "om_resume_uncertain", domain.OwnerControlCommand{
		Name: domain.OwnerControlTaskResume, WorkItemID: item.ID, Confirm: true,
	}); err == nil {
		t.Fatal("uncertain work was resumed without reconciliation")
	}
	result, err := store.ExecuteOwnerMutation(context.Background(), "om_reconcile_completed", domain.OwnerControlCommand{
		Name:        domain.OwnerControlTaskReconcile,
		WorkItemID:  item.ID,
		Disposition: domain.OwnerResolutionCompleted,
		Reason:      "已在 Lark 中确认消息存在",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkItemID != item.ID || result.ActionID != actionID ||
		result.Disposition != domain.OwnerResolutionCompleted {
		t.Fatalf("result=%+v", result)
	}
	inspection, err := store.InspectWork(context.Background(), domain.WorkInspectionQuery{
		WorkItemID: item.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.WorkItem == nil || inspection.WorkItem.Status != domain.StatusCompleted ||
		inspection.LatestAction == nil || inspection.LatestAction.Status != domain.ActionCompleted ||
		inspection.State.Uncertain {
		t.Fatalf("inspection=%+v", inspection)
	}
}

func TestOwnerResumeStartsNewCommunicationGeneration(t *testing.T) {
	store := openStore(t)
	if _, err := store.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_owner_resume_generation",
		Content:   "@测试负责人 修复后更新状态",
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter, now, item.ID,
	); err != nil {
		t.Fatal(err)
	}
	oldDecision := domain.Decision{
		Kind:        domain.DecisionReply,
		Relevance:   domain.RelevanceDirectMention,
		ReplyText:   "上一代错误回复",
		OwnerAction: "上一代错误通知",
	}
	replyID, _, _, completed, err := store.BeginReplyAction(
		context.Background(), item.DedupKey, oldDecision.ReplyText,
	)
	if err != nil || completed {
		t.Fatalf("reply id=%d completed=%v err=%v", replyID, completed, err)
	}
	if err := store.CompleteReplyAction(
		context.Background(), replyID, "om_old_owner_reply", "",
	); err != nil {
		t.Fatal(err)
	}
	noticeID, _, completed, err := store.BeginPostReplyNotification(
		context.Background(), item.DedupKey, oldDecision,
	)
	if err != nil || completed {
		t.Fatalf("notice id=%d completed=%v err=%v", noticeID, completed, err)
	}
	if err := store.CompletePostReplyNotification(
		context.Background(), noticeID, "",
	); err != nil {
		t.Fatal(err)
	}

	result, err := store.ExecuteOwnerMutation(
		context.Background(),
		"om_owner_resume_generation_command",
		domain.OwnerControlCommand{
			Name:       domain.OwnerControlTaskResume,
			WorkItemID: item.ID,
			Confirm:    true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed != 1 {
		t.Fatalf("result=%+v", result)
	}
	inspection, err := store.InspectWork(context.Background(), domain.WorkInspectionQuery{
		WorkItemID: item.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.WorkItem == nil ||
		inspection.WorkItem.Generation != 2 ||
		inspection.WorkItem.Status != domain.StatusReceived {
		t.Fatalf("inspection=%+v", inspection)
	}
	if _, _, _, found, err := store.ReadyPostReplyNotification(item.ID); err != nil || found {
		t.Fatalf("stale post-reply notification found=%v err=%v", found, err)
	}
}

func enqueueOwnerControlTestItem(
	t *testing.T,
	store *Store,
	event domain.NormalizedEvent,
) domain.WorkItem {
	t.Helper()
	if _, err := store.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Event.MessageID == event.MessageID {
			return item
		}
	}
	t.Fatalf("message %s was not enqueued", event.MessageID)
	return domain.WorkItem{}
}

func TestOwnerControlMigrationCreatesJournalAndResolutionTables(t *testing.T) {
	store := openStore(t)
	for _, table := range []string{"owner_control_commands", "owner_work_resolutions"} {
		var name string
		if err := store.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 12 {
		t.Fatalf("schema version=%d", version)
	}
	var columnName string
	if err := store.db.QueryRow(
		`SELECT name FROM pragma_table_info('owner_work_resolutions')
		 WHERE name = 'work_updated_at'`,
	).Scan(&columnName); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerMutationWaitsForConcurrentWriterWithoutReadUpgradeFailure(t *testing.T) {
	store := openStore(t)
	item := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_concurrent_owner_mutation", Content: "并发写入时仍要可靠收口",
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter, now, item.ID,
	); err != nil {
		t.Fatal(err)
	}

	blocker, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(
		`UPDATE work_items SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano),
		item.ID,
	); err != nil {
		t.Fatal(err)
	}

	type mutationOutcome struct {
		result domain.OwnerMutationResult
		err    error
	}
	outcome := make(chan mutationOutcome, 1)
	go func() {
		result, mutationErr := store.ExecuteOwnerMutation(
			context.Background(),
			"om_concurrent_ack",
			domain.OwnerControlCommand{
				Name:       domain.OwnerControlTaskAcknowledge,
				WorkItemID: item.ID,
				Reason:     "已核对",
			},
		)
		outcome <- mutationOutcome{result: result, err: mutationErr}
	}()
	time.Sleep(100 * time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}

	got := <-outcome
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.WorkItemID != item.ID ||
		got.result.Disposition != domain.OwnerResolutionAcknowledged {
		t.Fatalf("result=%+v", got.result)
	}
}

func TestResumedResolvedTaskBecomesActionableAgainAfterNewFailure(t *testing.T) {
	store := openStore(t)
	item := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_resolved_then_resumed", Content: "恢复后再次失败",
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter, now, item.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecuteOwnerMutation(
		context.Background(),
		"om_ack_before_resume",
		domain.OwnerControlCommand{
			Name:       domain.OwnerControlTaskAcknowledge,
			WorkItemID: item.ID,
			Reason:     "第一次已核对",
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecuteOwnerMutation(
		context.Background(),
		"om_resume_after_ack",
		domain.OwnerControlCommand{
			Name:       domain.OwnerControlTaskResume,
			WorkItemID: item.ID,
			Confirm:    true,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter,
		time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano),
		item.ID,
	); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListOwnerTasks(context.Background(), domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewAction, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].WorkItem.ID != item.ID {
		t.Fatalf("resumed task was hidden by stale resolution: %+v", page)
	}
}

func TestOwnerTaskActionViewDoesNotOrderRFC3339NanoTextAsTime(t *testing.T) {
	store := openStore(t)
	item := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_fractional_epoch", Content: "不同小数位的时间版本",
	})
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, updated_at = ? WHERE id = ?`,
		domain.StatusDeadLetter,
		"2026-07-30T09:00:00.11Z",
		item.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO owner_work_resolutions(
			work_item_id, command_message_id, disposition, reason,
			work_updated_at, resolved_at
		 ) VALUES (?, ?, ?, ?, ?, ?)`,
		item.ID,
		"om_fractional_epoch_resolution",
		domain.OwnerResolutionAcknowledged,
		"旧工作版本已收口",
		"2026-07-30T09:00:00.1Z",
		"2026-07-30T09:00:00.1Z",
	); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListOwnerTasks(context.Background(), domain.OwnerTaskQuery{
		View: domain.OwnerTaskViewAction, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].WorkItem.ID != item.ID {
		t.Fatalf("older resolution hid newer work epoch: %+v", page)
	}
}

func TestListPendingOwnerApprovalsOmitsRequestBodiesAndIsBounded(t *testing.T) {
	store := openStore(t)
	item := enqueueOwnerControlTestItem(t, store, domain.NormalizedEvent{
		MessageID: "om_pending_public", Content: "需要审批",
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	secret := `{"text":"SECRET_DRAFT_BODY"}`
	for i := 0; i < 8; i++ {
		status := domain.ActionCompleted
		if i >= 6 {
			status = domain.ActionAwaitingApproval
		}
		if _, err := store.db.Exec(
			`INSERT INTO action_attempts(
				work_item_id, kind, idempotency_key, status, request_json, created_at, updated_at
			 ) VALUES (?, 'reply', ?, ?, ?, ?, ?)`,
			item.ID, fmt.Sprintf("pending-public-%d", i), status, secret, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}

	counts, total, err := store.ActionAttemptCounts()
	if err != nil {
		t.Fatal(err)
	}
	if total != 8 || counts[domain.ActionCompleted] != 6 || counts[domain.ActionAwaitingApproval] != 2 {
		t.Fatalf("counts=%v total=%d", counts, total)
	}

	page, err := store.ListPendingOwnerApprovals(context.Background(), 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("page=%+v", page)
	}
	for _, action := range page.Items {
		if action.RequestJSON != "" || action.ResponseJSON != "" {
			t.Fatalf("pending list leaked request bodies: %+v", action)
		}
		if action.Status != domain.ActionAwaitingApproval || action.ID <= 0 {
			t.Fatalf("action=%+v", action)
		}
	}
}

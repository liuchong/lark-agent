package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
)

func TestCancelInterruptedWorkExceptKeptItemsPreservesAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"om_cancel_approval", "om_cancel_plain", "om_cancel_keep"} {
		if _, err := first.EnqueueEvent(domain.NormalizedEvent{
			MessageID: messageID,
			Content:   messageID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64, len(items))
	for _, item := range items {
		ids[item.Event.MessageID] = item.ID
	}
	approvalItem, ok, err := first.ClaimNext("approval-worker")
	if err != nil || !ok || approvalItem.Event.MessageID != "om_cancel_approval" {
		t.Fatalf("approval claim item=%+v ok=%v err=%v", approvalItem, ok, err)
	}
	approvalID, err := first.RequestShellApproval(
		context.Background(), approvalItem.DedupKey, "gofmt -w .", ".",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	result, err := current.CancelWork(context.Background(), domain.CancelWorkRequest{
		AllInterrupted: true,
		KeepWorkItemIDs: []int64{
			ids["om_cancel_keep"],
		},
		Reason: "audited as stale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CancelledWorkItemIDs) != 2 ||
		result.CancelledWorkItemIDs[0] != ids["om_cancel_approval"] ||
		result.CancelledWorkItemIDs[1] != ids["om_cancel_plain"] {
		t.Fatalf("result=%+v ids=%+v", result, ids)
	}
	for messageID, id := range ids {
		inspection, err := current.InspectWork(
			context.Background(),
			domain.WorkInspectionQuery{WorkItemID: id},
		)
		if err != nil {
			t.Fatal(err)
		}
		if messageID == "om_cancel_keep" {
			if inspection.WorkItem == nil || inspection.WorkItem.Status != domain.StatusInterrupted {
				t.Fatalf("kept inspection=%+v", inspection)
			}
			continue
		}
		if inspection.WorkItem == nil || inspection.WorkItem.Status != domain.StatusCancelled ||
			inspection.LatestInterruption == nil || inspection.LatestInterruption.ResumedAt.IsZero() ||
			inspection.LatestAction == nil || inspection.LatestAction.Kind != "operator_cancel" ||
			inspection.LatestAction.Status != domain.ActionCompleted ||
			!strings.Contains(inspection.LatestAction.RequestJSON, "audited as stale") {
			t.Fatalf("cancelled %s inspection=%+v", messageID, inspection)
		}
	}
	approval, err := current.GetActionAttempt(approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != domain.ActionCancelled {
		t.Fatalf("approval=%+v", approval)
	}
}

func TestCancelInterruptedWorkIsAtomicForUncertainAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{"om_cancel_safe", "om_cancel_uncertain"} {
		if _, err := first.EnqueueEvent(domain.NormalizedEvent{
			MessageID: messageID,
			Content:   messageID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64, len(items))
	for _, item := range items {
		ids[item.Event.MessageID] = item.ID
	}
	unsafe, ok, err := first.ClaimNext("unsafe-worker")
	if err != nil || !ok || unsafe.Event.MessageID != "om_cancel_safe" {
		t.Fatalf("claim item=%+v ok=%v err=%v", unsafe, ok, err)
	}
	if _, _, _, err := first.BeginShellAction(
		context.Background(), unsafe.DedupKey, "go test ./...", ".",
	); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	_, err = current.CancelWork(context.Background(), domain.CancelWorkRequest{
		AllInterrupted: true,
		Reason:         "must fail atomically",
	})
	if err == nil {
		t.Fatal("uncertain interrupted action was cancelled")
	}
	for messageID, id := range ids {
		inspection, inspectErr := current.InspectWork(
			context.Background(),
			domain.WorkInspectionQuery{WorkItemID: id},
		)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if inspection.WorkItem == nil || inspection.WorkItem.Status != domain.StatusInterrupted {
			t.Fatalf("%s changed after failed batch: %+v", messageID, inspection)
		}
	}
}

func TestApprovalMovesOlderWorkToLatestReadySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_old_approval", Content: "run formatter"}
	if _, err := first.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	actionID, err := first.RequestShellApproval(
		context.Background(), domain.DedupKey(event), "gofmt -w .", ".",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	ready, err := current.MarkCurrentSessionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operator, err := OpenInspection(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operator.Close() })
	if err := operator.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	items, err := current.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusReceived ||
		items[0].SessionID != ready.ID {
		t.Fatalf("items=%+v ready=%+v", items, ready)
	}
}

func TestApprovalWithoutReadySessionRemainsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	runtimeStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_paused_approval", Content: "run formatter"}
	if _, err := runtimeStore.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	actionID, err := runtimeStore.RequestShellApproval(
		context.Background(), domain.DedupKey(event), "gofmt -w .", ".",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeStore.PauseCurrentSessionWork(context.Background(), "test stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeStore.StopCurrentSession(context.Background(), "test stop"); err != nil {
		t.Fatal(err)
	}
	if err := runtimeStore.Close(); err != nil {
		t.Fatal(err)
	}

	operator, err := OpenInspection(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operator.Close() })
	if err := operator.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	action, err := operator.GetActionAttempt(actionID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := operator.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionReady || len(items) != 1 ||
		items[0].Status != domain.StatusInterrupted {
		t.Fatalf("action=%+v items=%+v", action, items)
	}
}

func TestApprovalDoesNotReturnWorkToOlderReadySessionDuringStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	older, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = older.Close() })
	if _, err := older.MarkCurrentSessionReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	event := domain.NormalizedEvent{MessageID: "om_starting_approval", Content: "run formatter"}
	if _, err := older.EnqueueEvent(event); err != nil {
		t.Fatal(err)
	}
	actionID, err := older.RequestShellApproval(
		context.Background(), domain.DedupKey(event), "gofmt -w .", ".",
	)
	if err != nil {
		t.Fatal(err)
	}

	starting, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = starting.Close() })
	if starting.CurrentSession().Status != domain.OnlineSessionStarting {
		t.Fatalf("starting session=%+v", starting.CurrentSession())
	}
	operator, err := OpenInspection(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operator.Close() })
	if err := operator.DecideAction(actionID, true); err != nil {
		t.Fatal(err)
	}
	items, err := operator.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusInterrupted {
		t.Fatalf("items=%+v older=%+v starting=%+v", items, older.CurrentSession(), starting.CurrentSession())
	}
}

func TestOnlineSessionLifecycleIsPersistedAndUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	firstSession := first.CurrentSession()
	if firstSession.ID == "" || firstSession.Status != domain.OnlineSessionStarting || firstSession.StartedAt.IsZero() {
		t.Fatalf("first session=%+v", firstSession)
	}
	ready, err := first.MarkCurrentSessionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != domain.OnlineSessionReady || ready.ReadyAt.IsZero() {
		t.Fatalf("ready session=%+v", ready)
	}
	stopped, err := first.StopCurrentSession(context.Background(), "test shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != domain.OnlineSessionStopped || stopped.EndedAt.IsZero() || stopped.Reason != "test shutdown" {
		t.Fatalf("stopped session=%+v", stopped)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if second.CurrentSession().ID == firstSession.ID {
		t.Fatalf("session id was reused: %q", firstSession.ID)
	}
	persisted, err := second.GetOnlineSession(context.Background(), firstSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domain.OnlineSessionStopped || persisted.Reason != "test shutdown" {
		t.Fatalf("persisted session=%+v", persisted)
	}
}

func TestInspectionConnectionDoesNotCreateOrStopDaemonSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	runtimeStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeStore.Close() })
	ready, err := runtimeStore.MarkCurrentSessionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	inspectionStore, err := OpenInspection(path)
	if err != nil {
		t.Fatal(err)
	}
	if inspectionStore.CurrentSession().ID != ready.ID {
		t.Fatalf("inspection session=%+v ready=%+v", inspectionStore.CurrentSession(), ready)
	}
	if err := inspectionStore.Close(); err != nil {
		t.Fatal(err)
	}
	persisted, err := runtimeStore.GetOnlineSession(context.Background(), ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domain.OnlineSessionReady {
		t.Fatalf("inspection stopped daemon session: %+v", persisted)
	}
}

func TestStartupRecoveryInterruptsOldProcessingWithPreciseSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	oldSession := first.CurrentSession()
	event := domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt-interrupted",
		MessageID: "om_interrupted",
		Content:   "inspect the repository",
		CreatedAt: oldSession.StartedAt.Add(time.Second),
	}
	receipt, err := first.RecordIntake(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := first.ClaimNext("old-worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	run, err := first.StartAgentRun(context.Background(), event, "model", "config")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AppendAgentStep(context.Background(), domain.AgentStep{
		RunID:      run.ID,
		Sequence:   7,
		Kind:       "tool",
		ToolCallID: "call-7",
		ToolName:   "read_workspace",
	}); err != nil {
		t.Fatal(err)
	}
	actionID, _, uncertain, err := first.BeginShellAction(context.Background(), item.DedupKey, "go test ./agent/storage", ".")
	if err != nil || actionID == 0 || uncertain {
		t.Fatalf("action id=%d uncertain=%v err=%v", actionID, uncertain, err)
	}
	if receipt.WorkItemID != item.ID {
		t.Fatalf("receipt=%+v item=%+v", receipt, item)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	inspection, err := second.InspectWork(context.Background(), domain.WorkInspectionQuery{MessageID: event.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.WorkItem == nil || inspection.WorkItem.Status != domain.StatusInterrupted ||
		inspection.WorkItem.SessionID != oldSession.ID {
		t.Fatalf("work item=%+v", inspection.WorkItem)
	}
	if inspection.LatestRun == nil || inspection.LatestRun.Status != domain.AgentRunAbandoned {
		t.Fatalf("run=%+v", inspection.LatestRun)
	}
	if inspection.LatestAction == nil || inspection.LatestAction.Status != domain.ActionBlocked || !inspection.State.Uncertain {
		t.Fatalf("action=%+v state=%+v", inspection.LatestAction, inspection.State)
	}
	if inspection.LatestInterruption == nil {
		t.Fatal("missing interruption snapshot")
	}
	snapshot := inspection.LatestInterruption
	if snapshot.WorkItemID != item.ID || snapshot.RunID != run.ID || snapshot.SessionID != oldSession.ID ||
		snapshot.Stage != domain.InterruptionStageActionExecution || snapshot.LastSequence != 7 ||
		snapshot.LastKind != "tool" || snapshot.LastTool != "read_workspace" ||
		snapshot.ActionKind != "shell" || snapshot.ActionStatus != domain.ActionExecuting ||
		snapshot.Reason == "" || snapshot.InterruptedAt.IsZero() || !snapshot.ResumedAt.IsZero() {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestStartupRecoveryPausesOldReceivedAndRetryWaitButCurrentRetryWaitRemainsAutomatic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnqueueEvent(domain.NormalizedEvent{MessageID: "om-old-received"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnqueueEvent(domain.NormalizedEvent{MessageID: "om-old-retry"}); err != nil {
		t.Fatal(err)
	}
	retryItem, ok, err := first.ClaimNext("old-worker")
	if err != nil || !ok {
		t.Fatalf("claim retry item=%+v ok=%v err=%v", retryItem, ok, err)
	}
	if retryItem.Event.MessageID != "om-old-received" {
		t.Fatalf("unexpected first claim=%+v", retryItem)
	}
	if err := first.MarkRetry(retryItem.ID, "temporary"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	items, err := second.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Status != domain.StatusInterrupted {
			t.Fatalf("old work remained claimable: %+v", item)
		}
	}
	if item, ok, err := second.ClaimNext("new-worker"); err != nil || ok {
		t.Fatalf("old work claimed: item=%+v ok=%v err=%v", item, ok, err)
	}

	if _, err := second.EnqueueEvent(domain.NormalizedEvent{MessageID: "om-current-retry"}); err != nil {
		t.Fatal(err)
	}
	current, ok, err := second.ClaimNext("new-worker")
	if err != nil || !ok {
		t.Fatalf("claim current item=%+v ok=%v err=%v", current, ok, err)
	}
	if err := second.MarkRetry(current.ID, "temporary"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.db.Exec(
		`UPDATE work_items SET next_attempt_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), current.ID,
	); err != nil {
		t.Fatal(err)
	}
	retried, ok, err := second.ClaimNext("new-worker")
	if err != nil || !ok || retried.ID != current.ID {
		t.Fatalf("current retry_wait item=%+v ok=%v err=%v", retried, ok, err)
	}
}

func TestRecordIntakeSeparatesOfflineBacklogFromCurrentDelayedDelivery(t *testing.T) {
	store := openStore(t)
	session := store.CurrentSession()
	oldReceipt, err := store.RecordIntake(context.Background(), domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt-old",
		MessageID: "om-old",
		CreatedAt: session.StartedAt.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	missingReceipt, err := store.RecordIntake(context.Background(), domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt-missing",
		MessageID: "om-missing",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentReceipt, err := store.RecordIntake(context.Background(), domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt-current",
		MessageID: "om-current",
		CreatedAt: session.StartedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldReceipt.Disposition != domain.IntakeOfflineBacklog || oldReceipt.WorkItemID != 0 ||
		missingReceipt.Disposition != domain.IntakeOfflineBacklog || missingReceipt.WorkItemID != 0 {
		t.Fatalf("old=%+v missing=%+v", oldReceipt, missingReceipt)
	}
	if currentReceipt.Disposition != domain.IntakeAdmitted || currentReceipt.WorkItemID == 0 {
		t.Fatalf("current=%+v", currentReceipt)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.MessageID != "om-current" ||
		items[0].SessionID != session.ID {
		t.Fatalf("items=%+v", items)
	}
}

func TestRestartReadmitsOnlyPristineWaitingUserWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := first.CurrentSession().StartedAt.Add(time.Second)
	for _, messageID := range []string{"om_wait_pristine", "om_wait_with_run"} {
		item := domain.NewWorkItem(domain.NormalizedEvent{
			Source:    domain.SourcePoll,
			EventID:   "poll:" + messageID,
			MessageID: messageID,
			ChatID:    "oc_group",
			CreatedAt: createdAt,
		})
		item.Status = domain.StatusWaitingUser
		item.WorkKind = domain.WorkKindDirectMention
		item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
		if _, err := first.RecordWorkIntake(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Event.MessageID != "om_wait_with_run" {
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := first.db.Exec(
			`INSERT INTO agent_runs(
				id, work_item_id, dedup_key, status, started_at
			 ) VALUES ('run-waiting', ?, ?, ?, ?)`,
			item.ID,
			item.DedupKey,
			domain.AgentRunCompleted,
			now,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	items, err = second.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]domain.WorkItemStatus{}
	sessions := map[string]string{}
	for _, item := range items {
		statuses[item.Event.MessageID] = item.Status
		sessions[item.Event.MessageID] = item.SessionID
	}
	if statuses["om_wait_pristine"] != domain.StatusWaitingUser ||
		sessions["om_wait_pristine"] != second.CurrentSession().ID {
		t.Fatalf("pristine waiting item was not readmitted: items=%+v", items)
	}
	if statuses["om_wait_with_run"] != domain.StatusInterrupted {
		t.Fatalf("model-started waiting item was replayable: items=%+v", items)
	}
	claimed, ok, err := second.ClaimNext("new-worker")
	if err != nil || !ok || claimed.Event.MessageID != "om_wait_pristine" {
		t.Fatalf("claim item=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestExistingCompletedMessageAddsDuplicateReceiptWithoutReplay(t *testing.T) {
	store := openStore(t)
	event := domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt-completed",
		MessageID: "om-completed",
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	}
	first, err := store.RecordIntake(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	actionID, _, _, _, err := store.BeginReplyAction(context.Background(), item.DedupKey, "done")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteReplyAction(context.Background(), actionID, "om-reply", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(item.ID, domain.Decision{Kind: domain.DecisionReply, ReplyText: "done"}); err != nil {
		t.Fatal(err)
	}
	second, err := store.RecordIntake(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Disposition != domain.IntakeDuplicate ||
		second.WorkItemID != first.WorkItemID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if replay, ok, err := store.ClaimNext("worker"); err != nil || ok {
		t.Fatalf("completed work replayed: item=%+v ok=%v err=%v", replay, ok, err)
	}
	inspection, err := store.InspectWork(context.Background(), domain.WorkInspectionQuery{MessageID: event.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.State.Observed || !inspection.State.Admitted || !inspection.State.Replied || !inspection.State.Completed {
		t.Fatalf("inspection state=%+v", inspection.State)
	}
}

func TestExplicitResumeHandlesOfflineInterruptedAndTerminalPolicy(t *testing.T) {
	store := openStore(t)
	backlog, err := store.RecordIntake(context.Background(), domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		MessageID: "om-backlog-resume",
		CreatedAt: store.CurrentSession().StartedAt.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	resumedBacklog, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{MessageID: backlog.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	if resumedBacklog.Receipt == nil || resumedBacklog.Receipt.Disposition != domain.IntakeAdmitted ||
		resumedBacklog.WorkItem == nil || resumedBacklog.WorkItem.Status != domain.StatusReceived {
		t.Fatalf("resumed backlog=%+v", resumedBacklog)
	}

	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om-interrupted-resume"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	var interruptedID int64
	for _, item := range items {
		if item.Event.MessageID == "om-interrupted-resume" {
			interruptedID = item.ID
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(
		`INSERT INTO online_sessions(id, status, started_at, ended_at, reason)
		 VALUES ('old-session', ?, ?, ?, 'test fixture')`,
		domain.OnlineSessionStopped, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE work_items SET status = ?, session_id = 'old-session' WHERE id = ?`,
		domain.StatusInterrupted, interruptedID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO work_interruptions(work_item_id, session_id, stage, reason, interrupted_at)
		 VALUES (?, 'old-session', ?, 'restart', ?)`,
		interruptedID, domain.InterruptionStageQueue, now,
	); err != nil {
		t.Fatal(err)
	}
	resumedInterrupted, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{WorkItemID: interruptedID})
	if err != nil {
		t.Fatal(err)
	}
	if resumedInterrupted.WorkItem == nil || resumedInterrupted.WorkItem.Status != domain.StatusReceived ||
		resumedInterrupted.WorkItem.SessionID != store.CurrentSession().ID ||
		resumedInterrupted.LatestInterruption == nil || resumedInterrupted.LatestInterruption.ResumedAt.IsZero() {
		t.Fatalf("resumed interrupted=%+v", resumedInterrupted)
	}

	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om-terminal"}); err != nil {
		t.Fatal(err)
	}
	terminal, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("terminal claim item=%+v ok=%v err=%v", terminal, ok, err)
	}
	if err := store.Complete(terminal.ID, domain.Decision{Kind: domain.DecisionRecord}); err != nil {
		t.Fatal(err)
	}
	_, err = store.ResumeWork(context.Background(), domain.ResumeWorkRequest{WorkItemID: terminal.ID})
	if err == nil {
		t.Fatal("terminal resume without force was accepted")
	}
	problem, ok := errs.ProblemOf(err)
	if !ok || problem.Subtype != errs.SubtypeFailedPrecondition {
		t.Fatalf("terminal resume error=%v problem=%+v", err, problem)
	}
	forced, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID: terminal.ID, ForceTerminal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if forced.WorkItem == nil || forced.WorkItem.Status != domain.StatusReceived {
		t.Fatalf("forced resume=%+v", forced)
	}
}

func TestLifecycleActionsFenceCompletedAndUncertainWrites(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	completedID, status, send, err := store.BeginLifecycleNotification(
		ctx,
		"lifecycle:online:completed",
		"online",
	)
	if err != nil || !send || status != domain.ActionExecuting {
		t.Fatalf("begin completed id=%d status=%s send=%v err=%v", completedID, status, send, err)
	}
	if err := store.CompleteLifecycleNotification(ctx, completedID, ""); err != nil {
		t.Fatal(err)
	}
	sameID, status, send, err := store.BeginLifecycleNotification(
		ctx,
		"lifecycle:online:completed",
		"online",
	)
	if err != nil || send || sameID != completedID || status != domain.ActionCompleted {
		t.Fatalf("repeat completed id=%d status=%s send=%v err=%v", sameID, status, send, err)
	}

	uncertainID, status, send, err := store.BeginLifecycleNotification(
		ctx,
		"lifecycle:offline:uncertain",
		"offline",
	)
	if err != nil || !send || status != domain.ActionExecuting {
		t.Fatalf("begin uncertain id=%d status=%s send=%v err=%v", uncertainID, status, send, err)
	}
	sameID, status, send, err = store.BeginLifecycleNotification(
		ctx,
		"lifecycle:offline:uncertain",
		"offline",
	)
	if err != nil || send || sameID != uncertainID || status != domain.ActionExecuting {
		t.Fatalf("repeat uncertain id=%d status=%s send=%v err=%v", sameID, status, send, err)
	}
	_, uncertain, err := store.RecoverySummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uncertain != 1 {
		t.Fatalf("uncertain=%d want=1", uncertain)
	}
}

func TestConvergeInterruptedWorkResumesSafeWaitsForApprovalAndTerminalizesUncertain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	events := []domain.NormalizedEvent{
		{MessageID: "om_safe_recovery", Content: "read current code"},
		{MessageID: "om_waiting_approval", Content: "send exact approved reply"},
		{MessageID: "om_uncertain_action", Content: "run external action"},
	}
	for _, event := range events {
		if _, err := first.EnqueueEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	items, err := first.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	byMessage := make(map[string]domain.WorkItem, len(items))
	for _, item := range items {
		byMessage[item.Event.MessageID] = item
	}
	waiting := byMessage["om_waiting_approval"]
	if _, err := first.RequestReplyApproval(
		context.Background(),
		waiting.DedupKey,
		"已完成前期核对",
		"需要本人批准",
		"确认是否发送",
		domain.RelevanceDirectMention,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.Exec(
		`UPDATE work_items SET status = ? WHERE id = ?`,
		domain.StatusAwaitingApproval,
		waiting.ID,
	); err != nil {
		t.Fatal(err)
	}
	uncertain := byMessage["om_uncertain_action"]
	if _, _, _, err := first.BeginShellAction(
		context.Background(),
		uncertain.DedupKey,
		"go test ./...",
		".",
	); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	ready, err := current.MarkCurrentSessionReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := current.ConvergeInterruptedWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Resumed != 1 || report.WaitingOwner != 1 ||
		report.Terminalized != 1 || report.Uncertain != 1 ||
		len(report.Notices) != 2 {
		t.Fatalf("report=%+v", report)
	}
	items, err = current.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]domain.WorkItem, len(items))
	for _, item := range items {
		statuses[item.Event.MessageID] = item
	}
	if safe := statuses["om_safe_recovery"]; safe.Status != domain.StatusReceived ||
		safe.SessionID != ready.ID {
		t.Fatalf("safe=%+v ready=%+v", safe, ready)
	}
	if waiting := statuses["om_waiting_approval"]; waiting.Status != domain.StatusAwaitingApproval {
		t.Fatalf("waiting=%+v", waiting)
	}
	if uncertain := statuses["om_uncertain_action"]; uncertain.Status != domain.StatusDeadLetter {
		t.Fatalf("uncertain=%+v", uncertain)
	}
	interrupted, _, err := current.RecoverySummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if interrupted != 0 {
		t.Fatalf("interrupted=%d", interrupted)
	}
}

func TestOwnerResolutionNotificationIsDurableAndIdempotent(t *testing.T) {
	store := openStore(t)
	if _, err := store.EnqueueEvent(domain.NormalizedEvent{MessageID: "om_resolution"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	id, key, send, err := store.BeginOwnerResolutionNotification(
		context.Background(),
		item.ID,
		"需要核对外部动作结果",
	)
	if err != nil || !send || id == 0 || key == "" || len(key) > 50 {
		t.Fatalf("id=%d key=%q send=%v err=%v", id, key, send, err)
	}
	if err := store.CompleteOwnerResolutionNotification(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	sameID, sameKey, send, err := store.BeginOwnerResolutionNotification(
		context.Background(),
		item.ID,
		"需要核对外部动作结果",
	)
	if err != nil || send || sameID != id || sameKey != key {
		t.Fatalf("id=%d key=%q send=%v err=%v", sameID, sameKey, send, err)
	}
}

package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
)

func TestOfflineBacklogIsAuditedButNotClaimable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := store.CurrentSession()
	receipt, err := store.RecordIntake(context.Background(), domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt_offline",
		MessageID: "om_offline",
		CreatedAt: session.StartedAt.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != domain.IntakeOfflineBacklog || receipt.WorkItemID != 0 {
		t.Fatalf("receipt=%+v", receipt)
	}
	if item, ok, err := store.ClaimNext("worker"); err != nil || ok {
		t.Fatalf("offline work became claimable: item=%+v ok=%v err=%v", item, ok, err)
	}
}

func TestBackfillAdmitsHistoricalOwnerMention(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := store.CurrentSession()
	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_backfill",
		MessageID: "om_backfill",
		CreatedAt: session.StartedAt.Add(-time.Hour),
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	receipt, err := store.RecordBackfillWorkIntake(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != domain.IntakeAdmitted || receipt.WorkItemID == 0 {
		t.Fatalf("receipt=%+v", receipt)
	}
	claimed, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.WorkKind != domain.WorkKindDirectMention {
		t.Fatalf("claimed=%+v", claimed)
	}
}

func TestBackfillAdmitsExistingOfflineBacklogReceiptWithCurrentClassification(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := store.CurrentSession()
	event := domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_backlog_backfill",
		MessageID: "om_backlog_backfill",
		CreatedAt: session.StartedAt.Add(-time.Hour),
	}
	backlog, err := store.RecordIntake(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.Disposition != domain.IntakeOfflineBacklog {
		t.Fatalf("backlog=%+v", backlog)
	}
	duplicateWithoutWork, err := store.RecordIntake(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if duplicateWithoutWork.Disposition != domain.IntakeDuplicate ||
		duplicateWithoutWork.WorkItemID != 0 {
		t.Fatalf("duplicateWithoutWork=%+v", duplicateWithoutWork)
	}
	item := domain.NewWorkItem(event)
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	receipt, err := store.RecordBackfillWorkIntake(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != duplicateWithoutWork.ID ||
		receipt.Disposition != domain.IntakeAdmitted ||
		receipt.WorkItemID == 0 {
		t.Fatalf("receipt=%+v backlog=%+v duplicateWithoutWork=%+v", receipt, backlog, duplicateWithoutWork)
	}
	claimed, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.WorkKind != domain.WorkKindDirectMention {
		t.Fatalf("claimed=%+v", claimed)
	}
}

func TestCurrentSessionDelayedEventRemainsEligible(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := store.CurrentSession()
	receipt, err := store.RecordIntake(context.Background(), domain.NormalizedEvent{
		Source:    domain.SourceRealtime,
		EventID:   "evt_current",
		MessageID: "om_current",
		CreatedAt: session.StartedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Disposition != domain.IntakeAdmitted || receipt.WorkItemID == 0 {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestWaitingUserWorkBecomesClaimableOnlyAfterNextAttemptAt(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := store.CurrentSession()
	now := time.Now().UTC()
	future := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_wait_future",
		MessageID: "om_wait_future",
		CreatedAt: session.StartedAt.Add(time.Second),
	})
	future.Status = domain.StatusWaitingUser
	future.WorkKind = domain.WorkKindDirectMention
	future.Priority = domain.PriorityDirectMention
	future.NextAttemptAt = now.Add(time.Hour)
	if _, err := store.RecordWorkIntake(context.Background(), future); err != nil {
		t.Fatal(err)
	}

	if item, ok, err := store.ClaimNext("worker"); err != nil || ok {
		t.Fatalf("future waiting work became claimable: item=%+v ok=%v err=%v", item, ok, err)
	}

	past := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_wait_past",
		MessageID: "om_wait_past",
		CreatedAt: session.StartedAt.Add(2 * time.Second),
	})
	past.Status = domain.StatusWaitingUser
	past.WorkKind = domain.WorkKindDirectMention
	past.Priority = domain.PriorityDirectMention
	past.NextAttemptAt = now.Add(-time.Second)
	if _, err := store.RecordWorkIntake(context.Background(), past); err != nil {
		t.Fatal(err)
	}

	item, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("past waiting work was not claimable: item=%+v ok=%v err=%v", item, ok, err)
	}
	if item.Event.MessageID != past.Event.MessageID ||
		item.Status != domain.StatusProcessing ||
		!item.NextAttemptAt.Equal(past.NextAttemptAt) {
		t.Fatalf("claimed=%+v want next_attempt_at=%s", item, past.NextAttemptAt)
	}
}

func TestWaitingUserDeferralReachesRetryCeilingAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)

	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_waiting_ceiling",
		MessageID: "om_waiting_ceiling",
		ChatID:    "oc_group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请看前文",
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
	})
	item.Status = domain.StatusWaitingUser
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	receipt, err := store.RecordWorkIntake(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("worker")
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", claimed, ok, err)
	}

	const reason = "semantic context remained incomplete"
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		reason,
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetWorkItem(context.Background(), receipt.WorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusDeadLetter ||
		current.RetryCount != 1 ||
		!current.NextAttemptAt.IsZero() ||
		current.LeaseBy != "" {
		t.Fatalf("current=%+v", current)
	}
	var deadReason string
	if err := store.db.QueryRow(
		`SELECT reason FROM dead_letters WHERE work_item_id = ?`,
		current.ID,
	).Scan(&deadReason); err != nil {
		t.Fatal(err)
	}
	if deadReason != reason {
		t.Fatalf("dead-letter reason=%q want=%q", deadReason, reason)
	}
	required, err := store.ListRequiredOwnerResolutionNotifications(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 ||
		required[0].ID == 0 ||
		required[0].WorkItemID != current.ID ||
		required[0].Reason != reason {
		t.Fatalf("required owner resolutions=%+v", required)
	}
}

func TestTerminalResolutionRequirementIsCancelledByExplicitResume(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)

	item := terminalizeWaitingUserWork(t, store, "om_cancel_stale_terminal_notice")
	required, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 || required[0].ID == 0 {
		t.Fatalf("required=%+v", required)
	}

	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    item.ID,
		ForceTerminal: true,
	}); err != nil {
		t.Fatal(err)
	}
	required, err = store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 0 {
		t.Fatalf("stale terminal requirements survived resume: %+v", required)
	}
	action, err := store.GetActionAttempt(requiredActionID(t, store, item.ID))
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionCancelled {
		t.Fatalf("requirement action=%+v", action)
	}
}

func TestLaterTerminalGenerationHasIndependentOwnerResolution(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)

	firstItem := terminalizeWaitingUserWork(t, store, "om_terminal_generation")
	firstRequired, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRequired) != 1 || firstRequired[0].ID == 0 {
		t.Fatalf("first required=%+v", firstRequired)
	}
	firstActionID, firstKey, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		firstRequired[0].ID,
		firstItem.ID,
		"first terminal summary",
	)
	if err != nil || !send {
		t.Fatalf("first action=%d key=%q send=%v err=%v", firstActionID, firstKey, send, err)
	}
	if len(firstKey) > 50 {
		t.Fatalf("first public key is too long: %d %q", len(firstKey), firstKey)
	}
	if err := store.CompleteOwnerResolutionNotification(
		context.Background(),
		firstActionID,
		"",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    firstItem.ID,
		ForceTerminal: true,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("second-terminal-generation")
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		"second terminal generation",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}

	if err := store.ConvergeOwnerResolutionRequirements(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondRequired, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(secondRequired) != 1 ||
		secondRequired[0].ID == firstRequired[0].ID ||
		secondRequired[0].WorkItemID != firstItem.ID {
		t.Fatalf("second required=%+v first=%+v", secondRequired, firstRequired)
	}
	secondActionID, secondKey, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		secondRequired[0].ID,
		firstItem.ID,
		"second terminal summary",
	)
	if err != nil || !send {
		t.Fatalf("second action=%d key=%q send=%v err=%v", secondActionID, secondKey, send, err)
	}
	if len(secondKey) > 50 {
		t.Fatalf("second public key is too long: %d %q", len(secondKey), secondKey)
	}
	if firstActionID == secondActionID || firstKey == secondKey {
		t.Fatalf(
			"terminal generations reused action identity: first=(%d,%q) second=(%d,%q)",
			firstActionID,
			firstKey,
			secondActionID,
			secondKey,
		)
	}
}

func TestExplicitResumeRejectsExecutingTerminalNotification(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)

	item := terminalizeWaitingUserWork(t, store, "om_executing_terminal_notice")
	required, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 {
		t.Fatalf("required=%+v", required)
	}
	if _, _, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		required[0].ID,
		item.ID,
		"terminal summary now executing",
	); err != nil || !send {
		t.Fatalf("send=%v err=%v", send, err)
	}

	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    item.ID,
		ForceTerminal: true,
	}); err == nil {
		t.Fatal("resume accepted an executing terminal notification")
	}
	current, err := store.GetWorkItem(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusDeadLetter {
		t.Fatalf("current=%+v", current)
	}
}

func TestExplicitResumeCancelsKnownFailedTerminalNotification(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.ConfigureRecovery(1)

	item := terminalizeWaitingUserWork(t, store, "om_failed_terminal_notice")
	required, err := store.ListRequiredOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(required) != 1 {
		t.Fatalf("required=%+v", required)
	}
	actionID, _, send, err := store.BeginOwnerResolutionNotificationForRequirement(
		context.Background(),
		required[0].ID,
		item.ID,
		"terminal summary with known send failure",
	)
	if err != nil || !send {
		t.Fatalf("action=%d send=%v err=%v", actionID, send, err)
	}
	if err := store.CompleteOwnerResolutionNotification(
		context.Background(),
		actionID,
		"Lark rejected the send",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ResumeWork(context.Background(), domain.ResumeWorkRequest{
		WorkItemID:    item.ID,
		ForceTerminal: true,
	}); err != nil {
		t.Fatal(err)
	}
	action, err := store.GetActionAttempt(actionID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Status != domain.ActionCancelled {
		t.Fatalf("failed terminal notice was not cancelled: %+v", action)
	}
	pending, err := store.ListPendingOwnerResolutionNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("stale failed notices remain pending: %+v", pending)
	}
}

func terminalizeWaitingUserWork(t *testing.T, store *Store, messageID string) domain.WorkItem {
	t.Helper()
	item := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:" + messageID,
		MessageID: messageID,
		ChatID:    "oc_group",
		ChatType:  "group",
		SenderID:  "ou_teammate",
		Content:   "@测试负责人 请结合前文处理",
		Mentions:  []domain.Mention{{OpenID: "ou_owner"}},
		CreatedAt: store.CurrentSession().StartedAt.Add(time.Second),
	})
	item.Status = domain.StatusWaitingUser
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	item.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	if _, err := store.RecordWorkIntake(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext("terminal-generation-test")
	if err != nil || !ok {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := store.DeferWaitingUserClaim(
		claimed.ID,
		claimed.LeaseBy,
		"semantic context remained incomplete",
		time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	return claimed
}

func requiredActionID(t *testing.T, store *Store, workItemID int64) int64 {
	t.Helper()
	actions, err := store.ListActionAttempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action.WorkItemID == workItemID && action.Kind == "owner_resolution_required" {
			return action.ID
		}
	}
	t.Fatalf("owner resolution requirement for work %d was not found", workItemID)
	return 0
}

func TestEditedWaitingUserMessageReplacesContentAndResetsDeadline(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	createdAt := store.CurrentSession().StartedAt.Add(time.Second)
	original := domain.NewWorkItem(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "poll:om_edit_wait",
		MessageID: "om_edit_wait",
		ChatID:    "oc_group",
		Content:   "旧问题",
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	original.Status = domain.StatusWaitingUser
	original.WorkKind = domain.WorkKindDirectMention
	original.Priority = domain.PriorityDirectMention
	original.NextAttemptAt = createdAt.Add(3 * time.Minute)
	if _, err := store.RecordWorkIntake(context.Background(), original); err != nil {
		t.Fatal(err)
	}

	editedAt := createdAt.Add(2 * time.Minute)
	edited := original
	edited.Event.Content = "编辑后的问题"
	edited.Event.UpdatedAt = editedAt
	edited.NextAttemptAt = editedAt.Add(3 * time.Minute)
	if _, err := store.RecordWorkIntake(context.Background(), edited); err != nil {
		t.Fatal(err)
	}

	items, err := store.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Event.Content != "编辑后的问题" ||
		!items[0].Event.UpdatedAt.Equal(editedAt) ||
		!items[0].NextAttemptAt.Equal(edited.NextAttemptAt) ||
		items[0].Status != domain.StatusWaitingUser {
		t.Fatalf("items=%+v", items)
	}
}

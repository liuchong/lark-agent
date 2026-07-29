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

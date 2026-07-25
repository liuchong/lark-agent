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
	item := domain.NewWorkItem(event)
	item.WorkKind = domain.WorkKindDirectMention
	item.Priority = domain.PriorityDirectMention
	receipt, err := store.RecordBackfillWorkIntake(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != backlog.ID ||
		receipt.Disposition != domain.IntakeAdmitted ||
		receipt.WorkItemID == 0 {
		t.Fatalf("receipt=%+v backlog=%+v", receipt, backlog)
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

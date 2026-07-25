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
